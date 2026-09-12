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
	records, err := collectChangeRecords(ctx, nativechange.New(file))
	if err != nil {
		return err
	}
	set, err := wal.CreateSegmentSetWithCapacity(filepath.Join(staging, changeWALDirectory), 0, walRingBytes)
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
			_, err = index.Bootstrap(1, records)
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
	if err != nil || after != uint64(len(records))+1 {
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
	set, err := wal.OpenSegmentSetWithCapacity(filepath.Join(directory, changeWALDirectory), 0, walRingBytes)
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

// collectChangeRecords reads the whole change log in one pass, envelopes
// included. It used to ask the log for each sequence in turn, which went
// through that log's process-resident index; one pass is what lets the index go
// away without making the build quadratic.
func collectChangeRecords(
	ctx context.Context,
	source *nativechange.Repository,
) ([]changeindex.Record, error) {
	result := make([]changeindex.Record, 0)
	err := source.WalkSince(0, func(_ uint64, envelope change.Envelope, payload []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		result = append(result, changeindex.Record{
			Locator: locatorForEnvelope(envelope), Body: payload,
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
	}
	return result, nil
}

// behind reports whether the index has committed changes still to catch up on.
//
// It is the cheap half of reconcile — two high-water reads, no writes — so a
// reader can find out whether it needs the write lock at all before taking it.
// sourceHighWater is the last commit sequence the record log holds.
//
// The Tree's high-water is a durable floor: the index is derived from the log
// and can never lead it, so the end of the log is found by probing forward from
// there. Normally that is one point read — the Tree is level with the log except
// between a write and the catch-up that follows it.
//
// The floor being taken is checked, not assumed. An index high-water the log
// does not hold is the index leading its source, which is corruption; it used to
// be caught by comparing two numbers after sweeping the whole log, and is now
// caught by one point read.
func (tree *authorityChangeTree) sourceHighWater() (indexed, source uint64, err error) {
	if tree == nil || tree.index == nil || tree.source == nil {
		return 0, 0, fmt.Errorf("%w: committed change reconcile", ErrInvalid)
	}
	indexed, err = tree.index.HighWater()
	if err != nil {
		return 0, 0, fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
	}
	if indexed != 0 {
		found, err := tree.source.Exists(indexed)
		if err != nil {
			return 0, 0, fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
		}
		if !found {
			return 0, 0, fmt.Errorf("%w: committed change index leads immutable source", ErrTargetCorrupt)
		}
	}
	next, err := tree.source.NextSequence(indexed)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: inspect committed change source: %v", ErrTargetCorrupt, err)
	}
	return indexed, next - 1, nil
}

// currentHighWater is the last commit sequence the index holds.
//
// Every publication catches the index up before it returns and a reopen
// reconciles before anything is allocated, so this is the log's high-water too —
// which is what lets the commit sequence allocator stop probing the log.
func (tree *authorityChangeTree) currentHighWater() (uint64, bool, error) {
	if tree == nil || tree.index == nil {
		return 0, false, nil
	}
	indexed, err := tree.index.HighWater()
	if err != nil {
		return 0, false, fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
	}
	return indexed, true, nil
}

// behind reports whether the index may have changes still to catch up on.
//
// It compares the log length with how far into the log the index has read. That
// can say "maybe" when the growth was records of other kinds, which costs an
// extra walk of a tail holding no changes — cheap, and the alternative is asking
// the log for its last sequence, which is a point read the log can no longer
// answer without a pass over itself.
func (tree *authorityChangeTree) behind() (bool, error) {
	if tree == nil || tree.index == nil || tree.source == nil {
		return false, fmt.Errorf("%w: committed change reconcile", ErrInvalid)
	}
	mark, err := tree.index.LogOffset()
	if err != nil {
		return false, fmt.Errorf("%w: committed change log offset: %v", ErrTargetCorrupt, err)
	}
	size, err := tree.sourceSize()
	if err != nil {
		return false, err
	}
	return mark < size, nil
}

func (tree *authorityChangeTree) reconcile(ctx context.Context, verifyExisting bool) error {
	if tree == nil || tree.index == nil || tree.source == nil || ctx == nil {
		return fmt.Errorf("%w: committed change reconcile", ErrInvalid)
	}
	// The Tree says where it got to; the walk below says what came after. Asking
	// the log where it ends was a point read per probe, and the log no longer
	// keeps a process-resident index to answer one cheaply.
	indexedHighWater, err := tree.index.HighWater()
	if err != nil {
		return fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
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
	// One pass over the tail, flushed in batches. Asking the log for each
	// sequence in turn went through its process-resident index; walking per
	// sequence instead would be quadratic, so the walk happens once and the
	// batch is what bounds memory.
	batch := make([]changeindex.Record, 0, changeReconcileBatch)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		// The change index has its own log, so it has its own ring and its
		// own last chance to free space before the append meets it.
		if err := tree.relieveRedoRing(); err != nil {
			return err
		}
		transactionID, err := tree.nextTransactionID()
		if err == nil {
			_, err = tree.index.Append(transactionID, batch)
		}
		if err != nil {
			return fmt.Errorf("%w: append committed change index: %v", ErrTargetCorrupt, err)
		}
		batch = batch[:0]
		return nil
	}
	// Start from the offset this index has already read, not from the beginning:
	// catching up on the write path would otherwise be a pass over the whole log
	// every time.
	from, err := tree.index.LogOffset()
	if err != nil {
		return fmt.Errorf("%w: committed change log offset: %v", ErrTargetCorrupt, err)
	}
	logSize, err := tree.sourceSize()
	if err != nil {
		return err
	}
	walkErr := tree.source.WalkFrom(from, indexedHighWater,
		func(sequence uint64, envelope change.Envelope, payload []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			batch = append(batch, changeindex.Record{
				Locator: locatorForEnvelope(envelope), Body: payload,
			})
			if len(batch) < changeReconcileBatch {
				return nil
			}
			return flush()
		})
	if walkErr != nil {
		return walkErr
	}
	if err := flush(); err != nil {
		return err
	}
	// The offset goes last, in a commit of its own, so it can never claim more
	// than the Tree holds.
	if logSize > from {
		transactionID, err := tree.nextTransactionID()
		if err == nil {
			_, err = tree.index.SetLogOffset(transactionID, logSize)
		}
		if err != nil {
			return fmt.Errorf("%w: record committed change log offset: %v", ErrTargetCorrupt, err)
		}
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
	err := maintainRedoLog(tree.set, tree.redoBarrier())
	if err != nil {
		return fmt.Errorf("%w: committed change index redo log: %v", ErrTargetCorrupt, err)
	}
	return nil
}

// relieveRedoRing frees ring space before an append when the change index's
// ring is already full, for the same reason the generation does it.
func (tree *authorityChangeTree) relieveRedoRing() error {
	if tree == nil || tree.set == nil {
		return nil
	}
	if err := relieveRedoRing(tree.set, tree.redoBarrier()); err != nil {
		return fmt.Errorf("%w: committed change index redo ring: %v", ErrTargetCorrupt, err)
	}
	return nil
}

func (tree *authorityChangeTree) redoBarrier() redoBarrier {
	return redoBarrier{targets: []flushTarget{{
		kind: "changes", runtime: tree.runtime, manager: tree.manager,
	}}}
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
	envelope, err := tree.envelopeFor(locator)
	if err != nil || !sameChange(locatorForEnvelope(envelope), locator) {
		return fmt.Errorf("%w: committed change locator/body mismatch at %d", ErrTargetCorrupt, locator.CommitSequence)
	}
	return nil
}

// envelopeFor resolves a Locator to its envelope.
//
// The Tree carries the envelope, so this is a read of the same structure the
// Locator came from. A Locator written before envelopes moved into the Tree
// carries no body; those fall back to the record log, which is the only reason
// that log is still point-read for this kind.
func (tree *authorityChangeTree) envelopeFor(locator changeindex.Locator) (change.Envelope, error) {
	body, stored, err := tree.index.Body(locator.CommitSequence)
	if err != nil {
		return change.Envelope{}, err
	}
	if !stored {
		return tree.source.Get(locator.CommitSequence)
	}
	return nativechange.Decode(body, locator.CommitSequence)
}

func (tree *authorityChangeTree) get(transactionID string) (change.Envelope, error) {
	locator, err := tree.index.LookupTransaction(transactionID)
	if err != nil {
		return change.Envelope{}, err
	}
	envelope, err := tree.envelopeFor(locator)
	if err != nil || !sameChange(locatorForEnvelope(envelope), locator) {
		return change.Envelope{}, fmt.Errorf("%w: committed change locator/body mismatch", ErrTargetCorrupt)
	}
	return envelope, nil
}

// getBySequence resolves a transaction's envelope from the change sequence a Row
// revision carries.
//
// It reads the Tree, which carries envelopes. The Tree used to be reconciled
// lazily — only when someone listed or got a change — so it lagged the log
// between a write and the next such read, and this had to read the log instead.
// Every publication now catches the index up before it returns, bounded by a
// durable record of how far into the log the index has read, so the Tree is the
// answer.
//
// The log is still consulted for a sequence the Tree does not have: the window
// after a crash and before the reopen reconcile, and changes indexed before
// envelopes moved into the Tree.
func (tree *authorityChangeTree) getBySequence(sequence uint64) (change.Envelope, error) {
	if tree == nil || tree.source == nil || sequence == 0 {
		return change.Envelope{}, changeindex.ErrNotFound
	}
	locator, err := tree.index.LookupSequence(sequence)
	if err == nil {
		return tree.envelopeFor(locator)
	}
	if !errors.Is(err, changeindex.ErrNotFound) {
		return change.Envelope{}, err
	}
	// The Tree does not have it. Every publication catches the index up before
	// it returns, so this is the window after a crash and before the reopen
	// reconcile — rare, and the record log still has the answer.
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
			envelope, err := tree.envelopeFor(locator)
			if err != nil || !sameChange(locatorForEnvelope(envelope), locator) {
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

// sameChange compares what names a change, not how it is stored.
//
// BodyChunks is a storage detail the Tree fills in, so a Locator read back from
// the Tree carries it and one derived from an envelope does not. Comparing whole
// Locators made every verification fail.
func sameChange(left, right changeindex.Locator) bool {
	return left.CommitSequence == right.CommitSequence &&
		left.TransactionID == right.TransactionID &&
		left.Checksum == right.Checksum
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

// sourceSize is how long the record log is now. A length taken here is a record
// boundary, so the index can store it and later ask only for what came after.
func (tree *authorityChangeTree) sourceSize() (int64, error) {
	size, err := tree.source.LogSize()
	if err != nil {
		return 0, fmt.Errorf("%w: measure committed change log: %v", ErrTargetCorrupt, err)
	}
	return size, nil
}

// catchUpChangesAfterWrite brings the committed change index level with the log
// before a publication returns.
//
// The index used to be reconciled only when someone listed or got a change, so
// between a write and the next such read it lagged the log — and a history read
// that wanted a Row's attribution had to go to the log directly, a point read
// through the log's process-resident index. Catching up here is what lets that
// read go to the Tree instead.
//
// This was tried once without a durable offset and backed out: the catch-up read
// the log from the beginning, so every write became a pass over the whole file.
// The index now records how far it has read, so the catch-up costs the records
// this write added.
//
// The caller holds the Authority write lock. A failure here never fails the
// write — the log already has the change and the next reconcile picks it up —
// but it is recorded rather than swallowed.
func (authority *Authority) catchUpChangesAfterWrite(ctx context.Context) {
	if authority == nil || authority.changes == nil {
		return
	}
	if err := authority.changes.reconcile(ctx, false); err != nil {
		authority.changeCatchUpErr = err
		return
	}
	authority.changeCatchUpErr = nil
}

// ChangeCatchUpError reports the last failed committed change catch-up, or nil.
// A failure here never failed a write.
func (authority *Authority) ChangeCatchUpError() error {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.changeCatchUpErr
}
