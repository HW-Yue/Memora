package pagestoremigration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const (
	changeIndexDirectory = "change-index-v1"
	changePageFilename   = "changes.pages"
	changeWALDirectory   = "changes.wal"
	changeSpaceID        = uint64(0x4d454d434847) // MEMCHG
	changeOpenFrames     = uint64(512)
	changeReconcileBatch = 256
)

type changeIndexPhase string

const (
	phaseChangeStagingCreated changeIndexPhase = "staging-created"
	phaseChangeBuilt          changeIndexPhase = "tree-built"
	phaseChangeBeforeRename   changeIndexPhase = "before-rename"
	phaseChangeAfterRename    changeIndexPhase = "after-rename"
)

type changeIndexOperations struct {
	mkdirTemp     func(string, string) (string, error)
	rename        func(string, string) error
	removeAll     func(string) error
	syncDirectory func(string) error
	checkpoint    func(changeIndexPhase) error
}

type authorityChangeTree struct {
	directory string
	set       *wal.SegmentSet
	manager   *page.Manager
	runtime   *treecommit.Runtime
	index     *changeindex.Index
	source    *nativechange.Repository
}

func openAuthorityChangeTree(
	ctx context.Context,
	databaseDirectory string,
	file *nativestore.File,
) (*authorityChangeTree, error) {
	if ctx == nil || databaseDirectory == "" || file == nil {
		return nil, fmt.Errorf("%w: committed change index request", ErrInvalid)
	}
	target := filepath.Join(databaseDirectory, changeIndexDirectory)
	if _, err := os.Lstat(target); errors.Is(err, os.ErrNotExist) {
		if err := buildAuthorityChangeTree(ctx, databaseDirectory, target, file); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("%w: inspect committed change index: %v", ErrTargetCorrupt, err)
	}
	if err := validateChangeIndexEntries(target); err != nil {
		return nil, err
	}
	tree, err := openChangeTree(target, file)
	if err != nil {
		return nil, fmt.Errorf("%w: open committed change index: %v", ErrTargetCorrupt, err)
	}
	if err := tree.reconcile(ctx, true); err != nil {
		_ = tree.Close()
		return nil, err
	}
	return tree, nil
}

func buildAuthorityChangeTree(
	ctx context.Context,
	databaseDirectory, target string,
	file *nativestore.File,
) (result error) {
	return buildAuthorityChangeTreeWithOperations(ctx, databaseDirectory, target, file, changeIndexOperations{
		mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
		syncDirectory: syncGenerationDirectory,
	})
}

func buildAuthorityChangeTreeWithOperations(
	ctx context.Context,
	databaseDirectory, target string,
	file *nativestore.File,
	operations changeIndexOperations,
) (result error) {
	if operations.mkdirTemp == nil || operations.rename == nil || operations.removeAll == nil ||
		operations.syncDirectory == nil {
		return fmt.Errorf("%w: committed change index operations", ErrInvalid)
	}
	checkpoint := func(phase changeIndexPhase) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if operations.checkpoint == nil {
			return nil
		}
		return operations.checkpoint(phase)
	}
	staging, err := operations.mkdirTemp(databaseDirectory, ".change-index-v1.staging-")
	if err != nil {
		return fmt.Errorf("create committed change staging directory: %w", err)
	}
	defer func() {
		if err := operations.removeAll(staging); err != nil {
			result = errors.Join(result, fmt.Errorf("clean committed change staging directory: %w", err))
		}
	}()
	if err := checkpoint(phaseChangeStagingCreated); err != nil {
		return err
	}
	locators, err := collectChangeLocators(ctx, nativechange.New(file))
	if err != nil {
		return err
	}
	set, err := wal.CreateSegmentSet(filepath.Join(staging, changeWALDirectory), 0)
	if err != nil {
		return err
	}
	manager, err := page.Create(filepath.Join(staging, changePageFilename), changeSpaceID)
	if err != nil {
		_ = set.Close()
		return err
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: changeSpaceID, Capacity: changeOpenFrames, OldFrames: changeOpenFrames / 2,
	})
	if err == nil {
		var index *changeindex.Index
		index, err = changeindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(1, locators)
		}
	}
	if err == nil {
		flushed, flushErr := runtime.FlushDirty(math.MaxUint64)
		err = flushErr
		if err == nil && flushed.Remaining != 0 {
			err = fmt.Errorf("committed change index dirty Pages remaining: %d", flushed.Remaining)
		}
	}
	if err == nil {
		err = manager.Sync()
	}
	err = errors.Join(err, set.Close(), manager.Close())
	if err != nil {
		return err
	}
	if err := checkpoint(phaseChangeBuilt); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Verify that the immutable source did not advance while the candidate was built.
	after, err := nativechange.New(file).NextSequence(0)
	if err != nil || after != uint64(len(locators))+1 {
		if err == nil {
			err = ErrSourceChanged
		}
		return err
	}
	if err := checkpoint(phaseChangeBeforeRename); err != nil {
		return err
	}
	if err := operations.rename(staging, target); err != nil {
		if _, inspectErr := os.Lstat(target); inspectErr != nil {
			return fmt.Errorf("publish committed change index: %w", err)
		}
	}
	if err := checkpoint(phaseChangeAfterRename); err != nil {
		return fmt.Errorf("%w: committed change index renamed before checkpoint: %v", ErrOutcomeUnknown, err)
	}
	if err := operations.syncDirectory(databaseDirectory); err != nil {
		return fmt.Errorf("%w: sync committed change index parent: %v", ErrOutcomeUnknown, err)
	}
	return nil
}

func openChangeTree(directory string, file *nativestore.File) (*authorityChangeTree, error) {
	set, err := wal.OpenSegmentSet(filepath.Join(directory, changeWALDirectory), 0)
	if err != nil {
		return nil, err
	}
	manager, err := page.Open(filepath.Join(directory, changePageFilename), changeSpaceID)
	if err != nil {
		_ = set.Close()
		return nil, err
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: changeSpaceID, Capacity: changeOpenFrames, OldFrames: changeOpenFrames / 2,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return nil, err
	}
	index, err := changeindex.Open(runtime)
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return nil, err
	}
	return &authorityChangeTree{
		directory: directory, set: set, manager: manager, runtime: runtime,
		index: index, source: nativechange.New(file),
	}, nil
}

func validateChangeIndexEntries(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: committed change index directory", ErrTargetCorrupt)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		return fmt.Errorf("%w: committed change index entries", ErrTargetCorrupt)
	}
	want := map[string]bool{changePageFilename: false, changeWALDirectory: true}
	for _, entry := range entries {
		directoryExpected, ok := want[entry.Name()]
		entryInfo, statErr := os.Lstat(filepath.Join(directory, entry.Name()))
		if !ok || statErr != nil || entryInfo.Mode()&os.ModeSymlink != 0 ||
			(directoryExpected && !entryInfo.IsDir()) || (!directoryExpected && !entryInfo.Mode().IsRegular()) {
			return fmt.Errorf("%w: committed change index entry %q", ErrTargetCorrupt, entry.Name())
		}
	}
	return nil
}

func collectChangeLocators(
	ctx context.Context,
	source *nativechange.Repository,
) ([]changeindex.Locator, error) {
	next, err := source.NextSequence(0)
	if err != nil {
		return nil, fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
	}
	result := make([]changeindex.Locator, 0)
	for sequence := uint64(1); sequence < next; sequence++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		envelope, err := source.Get(sequence)
		if err != nil {
			return nil, fmt.Errorf("%w: committed change sequence %d: %v", ErrTargetCorrupt, sequence, err)
		}
		result = append(result, locatorForEnvelope(envelope))
	}
	return result, nil
}

// behind reports whether the index has committed changes still to catch up on.
//
// It is the cheap half of reconcile — two high-water reads, no writes — so a
// reader can find out whether it needs the write lock at all before taking it.
func (tree *authorityChangeTree) behind() (bool, error) {
	if tree == nil || tree.index == nil || tree.source == nil {
		return false, fmt.Errorf("%w: committed change reconcile", ErrInvalid)
	}
	next, err := tree.source.NextSequence(0)
	if err != nil {
		return false, fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
	}
	indexed, err := tree.index.HighWater()
	if err != nil {
		return false, fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
	}
	if indexed > next-1 {
		return false, fmt.Errorf("%w: committed change index leads immutable source", ErrTargetCorrupt)
	}
	return indexed < next-1, nil
}

func (tree *authorityChangeTree) reconcile(ctx context.Context, verifyExisting bool) error {
	if tree == nil || tree.index == nil || tree.source == nil || ctx == nil {
		return fmt.Errorf("%w: committed change reconcile", ErrInvalid)
	}
	next, err := tree.source.NextSequence(0)
	if err != nil {
		return fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
	}
	bodyHighWater := next - 1
	indexedHighWater, err := tree.index.HighWater()
	if err != nil {
		return fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
	}
	if indexedHighWater > bodyHighWater {
		return fmt.Errorf("%w: committed change index leads immutable source", ErrTargetCorrupt)
	}
	if verifyExisting {
		for after := uint64(0); after < indexedHighWater; {
			through := min(indexedHighWater, after+changeReconcileBatch)
			locators, err := tree.index.Range(after, through, changeReconcileBatch)
			if err != nil || len(locators) != int(through-after) {
				return fmt.Errorf("%w: committed change index range: %v", ErrTargetCorrupt, err)
			}
			for _, locator := range locators {
				if err := tree.verifyLocator(locator); err != nil {
					return err
				}
			}
			after = through
		}
	}
	for first := indexedHighWater + 1; first <= bodyHighWater; {
		last := min(bodyHighWater, first+changeReconcileBatch-1)
		locators := make([]changeindex.Locator, 0, last-first+1)
		for sequence := first; sequence <= last; sequence++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			envelope, err := tree.source.Get(sequence)
			if err != nil {
				return fmt.Errorf("%w: committed change sequence %d: %v", ErrTargetCorrupt, sequence, err)
			}
			locators = append(locators, locatorForEnvelope(envelope))
		}
		transactionID, err := tree.nextTransactionID()
		if err == nil {
			_, err = tree.index.Append(transactionID, locators)
		}
		if err != nil {
			return fmt.Errorf("%w: append committed change index: %v", ErrTargetCorrupt, err)
		}
		first = last + 1
	}
	return tree.maintainRedoLog()
}

// maintainRedoLog runs one maintenance round over the change index's own redo
// log. That log is separate from the generation's — the change index is a
// separate durability unit with its own Page file — so it needs its own round,
// or it grows without bound exactly as the generation's used to.
//
// Unlike the generation's, a failure here is returned: reconcile is not
// following a committed user write, so failing it costs nothing already done.
func (tree *authorityChangeTree) maintainRedoLog() error {
	if tree == nil || tree.set == nil {
		return nil
	}
	err := maintainRedoLog(tree.set, redoBarrier{targets: []flushTarget{{
		kind: "changes", runtime: tree.runtime, manager: tree.manager,
	}}})
	if err != nil {
		return fmt.Errorf("%w: committed change index redo log: %v", ErrTargetCorrupt, err)
	}
	return nil
}

func (tree *authorityChangeTree) nextTransactionID() (uint64, error) {
	frontier, err := tree.set.DurableFrontier()
	if err != nil {
		return 0, err
	}
	if frontier.LastTransactionID == math.MaxUint64 {
		return 0, fmt.Errorf("transaction ID exhausted")
	}
	return frontier.LastTransactionID + 1, nil
}

func (tree *authorityChangeTree) verifyLocator(locator changeindex.Locator) error {
	envelope, err := tree.source.Get(locator.CommitSequence)
	if err != nil || locatorForEnvelope(envelope) != locator {
		return fmt.Errorf("%w: committed change locator/body mismatch at %d", ErrTargetCorrupt, locator.CommitSequence)
	}
	return nil
}

func (tree *authorityChangeTree) get(transactionID string) (change.Envelope, error) {
	locator, err := tree.index.LookupTransaction(transactionID)
	if err != nil {
		return change.Envelope{}, err
	}
	envelope, err := tree.source.Get(locator.CommitSequence)
	if err != nil || locatorForEnvelope(envelope) != locator {
		return change.Envelope{}, fmt.Errorf("%w: committed change locator/body mismatch", ErrTargetCorrupt)
	}
	return envelope, nil
}

// getBySequence resolves a transaction's envelope from the change sequence a Row
// revision carries. It reads the immutable source directly rather than the Page
// index: that index is reconciled lazily — only when someone lists or gets a
// change — so it lags the source in between, and a history read must not depend
// on that having happened. The source read is a point lookup by sequence and the
// envelope validates its own checksum on decode.
func (tree *authorityChangeTree) getBySequence(sequence uint64) (change.Envelope, error) {
	if tree == nil || tree.source == nil || sequence == 0 {
		return change.Envelope{}, changeindex.ErrNotFound
	}
	return tree.source.Get(sequence)
}

func (tree *authorityChangeTree) list(
	ctx context.Context,
	databaseID string,
	after, snapshot uint64,
	limit int,
) ([]change.Envelope, bool, error) {
	result := make([]change.Envelope, 0, limit+1)
	position := after
	for position < snapshot && len(result) <= limit {
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		through := min(snapshot, position+changeReconcileBatch)
		locators, err := tree.index.Range(position, through, changeReconcileBatch)
		if err != nil {
			return nil, false, err
		}
		for _, locator := range locators {
			envelope, err := tree.source.Get(locator.CommitSequence)
			if err != nil || locatorForEnvelope(envelope) != locator {
				return nil, false, fmt.Errorf("%w: committed change locator/body mismatch", ErrTargetCorrupt)
			}
			if databaseID == "" || envelopeHasDatabase(envelope, databaseID) {
				result = append(result, envelope)
				if len(result) > limit {
					return result[:limit], true, nil
				}
			}
		}
		position = through
	}
	return result, false, nil
}

func locatorForEnvelope(envelope change.Envelope) changeindex.Locator {
	return changeindex.Locator{
		CommitSequence: envelope.CommitSequence,
		TransactionID:  envelope.TransactionID,
		Checksum:       envelope.Checksum,
	}
}

func envelopeHasDatabase(envelope change.Envelope, databaseID string) bool {
	index := sort.SearchStrings(envelope.DatabaseIDs, databaseID)
	return index < len(envelope.DatabaseIDs) && envelope.DatabaseIDs[index] == databaseID
}

func (tree *authorityChangeTree) Close() error {
	if tree == nil {
		return nil
	}
	return errors.Join(tree.set.Close(), tree.manager.Close())
}
