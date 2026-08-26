package pagestoremigration

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/buffer"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

const openGenerationFrames = uint64(512)

// walSegmentRollBytes is how large the active redo log Segment may grow before
// a maintenance round rolls, checkpoints and reclaims.
//
// 4 MiB is deliberately small. A threshold sized for a server — 64 MiB, say —
// would never be reached by a personal-scale database, which means the
// maintenance would be code that is written, tested, and never runs. That is
// precisely the failure mode this work exists to remove.
//
// It is a var only so tests can lower it; nothing configures it at runtime.
var walSegmentRollBytes = uint64(4 << 20)

// flushTarget is one Tree's contribution to a durability barrier.
type flushTarget struct {
	kind    string
	runtime *treecommit.Runtime
	manager *page.Manager
}

// redoBarrier is the wal.DurabilityBarrier a checkpoint needs: it puts every
// Tree backed by the log on disk before the checkpoint claims they are there.
type redoBarrier struct {
	targets []flushTarget
}

// FlushThrough puts every Tree's dirty Pages on disk, using recoveryLSN as the
// durability bound.
//
// Two things are going on here. The buffer Pool's frames carry no LSN index, so
// there is no way to select "only the Pages below this LSN" — every dirty Page
// is flushed, which satisfies any bound and is therefore correct, just more
// work than strictly required. The cost is bounded by the Pools' capacity. This
// is a classic sharp checkpoint.
//
// recoveryLSN is used rather than ignored: it is the no-steal bound each Page
// is checked against. It has to be passed in rather than read back from the
// log, because the log holds its own lock while calling this — asking it for
// its durable LSN here deadlocks. That is what FlushDirtyThrough exists for.
func (barrier redoBarrier) FlushThrough(recoveryLSN uint64) error {
	for _, target := range barrier.targets {
		report, err := target.runtime.FlushDirtyThrough(math.MaxUint64, recoveryLSN)
		if err != nil {
			return fmt.Errorf("flush %s Tree for checkpoint: %w", target.kind, err)
		}
		if report.Remaining != 0 {
			return fmt.Errorf(
				"%w: %s Tree kept %d dirty Pages at checkpoint",
				ErrTargetCorrupt, target.kind, report.Remaining,
			)
		}
		if err := target.manager.Sync(); err != nil {
			return fmt.Errorf("sync %s Tree Pages for checkpoint: %w", target.kind, err)
		}
	}
	return nil
}

// maintainRedoLog rolls, checkpoints and reclaims a redo log once its active
// Segment has outgrown walSegmentRollBytes.
//
// It runs synchronously after a successful write, never before: the write is
// already committed, and maintenance must not be able to undo it. Synchronous
// because one round's flush is bounded by the Pools' capacity, and because a
// background worker would cost a lifecycle and a shutdown ordering this
// repository does not otherwise need.
//
// Three outcomes are swallowed — nothing to roll, no new commit since the last
// checkpoint, nothing reclaimable. They mean "no work", not "failure".
func maintainRedoLog(log *wal.SegmentSet, barrier redoBarrier) error {
	if log == nil {
		return nil
	}
	due, err := rollIsDue(log)
	if err != nil || !due {
		return err
	}
	if _, err := log.Roll(); err != nil {
		if errors.Is(err, wal.ErrEmptySegment) {
			return nil
		}
		return fmt.Errorf("roll redo log: %w", err)
	}
	if _, err := log.PublishCheckpoint(barrier); err != nil {
		if errors.Is(err, wal.ErrNoCheckpointProgress) {
			return nil
		}
		return fmt.Errorf("publish redo log checkpoint: %w", err)
	}
	if _, err := log.Reclaim(); err != nil {
		if errors.Is(err, wal.ErrNoReclaimableSegments) {
			return nil
		}
		return fmt.Errorf("reclaim redo log Segments: %w", err)
	}
	return nil
}

// rollIsDue reports whether the active Segment has passed the roll threshold.
func rollIsDue(log *wal.SegmentSet) (bool, error) {
	segments, err := log.State()
	if err != nil {
		return false, fmt.Errorf("read redo log state: %w", err)
	}
	if len(segments) == 0 {
		return false, nil
	}
	active := segments[len(segments)-1]
	return active.NextLSN-active.StartLSN >= walSegmentRollBytes, nil
}

// maintainRedoLog runs one maintenance round over the generation's shared redo
// log. The caller must hold the Authority write lock.
func (generation *Generation) maintainRedoLog() error {
	if generation == nil || generation.log == nil {
		return nil
	}
	targets := make([]flushTarget, 0, len(generation.trees))
	for _, tree := range generation.trees {
		targets = append(targets, flushTarget{
			kind: tree.manifest.Kind, runtime: tree.runtime, manager: tree.manager,
		})
	}
	return maintainRedoLog(generation.log, redoBarrier{targets: targets})
}

type generationTree struct {
	manifest treeManifest
	set      *wal.SegmentSet
	manager  *page.Manager
	runtime  *treecommit.Runtime
}

type Generation struct {
	mu        sync.Mutex
	directory string
	manifest  generationManifest
	// log is the one redo log every Tree commits into. It is nil for a pre-v4
	// generation, where each Tree still owns the log in its own generationTree.
	log      *wal.SegmentSet
	trees    []*generationTree
	catalog  *catalogindex.Index
	current  *currentrowindex.Index
	versions *rowversionindex.Index
	fulltext *fulltextindex.Index
	// tables holds the per-Table Trees, keyed by Table ID. The four fixed Trees
	// above are named fields because there is exactly one of each; these follow
	// the Catalog and so cannot be. See docs/storage/per-table-tree-v1.md §2.
	tables map[string]*generationTree
	closed bool
}

func OpenGeneration(directory string) (*Generation, error) {
	return openGeneration(directory, true)
}

// openLiveGeneration opens an activated generation whose Tree/WAL bytes are
// expected to have advanced beyond the immutable F106 migration manifest.
func openLiveGeneration(directory string) (*Generation, error) {
	return openGeneration(directory, false)
}

func openGeneration(directory string, strict bool) (*Generation, error) {
	if directory == "" {
		return nil, fmt.Errorf("%w: generation directory", ErrInvalid)
	}
	info, err := os.Lstat(directory)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: generation directory", ErrTargetCorrupt)
	}
	manifest, err := readManifest(directory)
	if err != nil {
		return nil, err
	}
	if strict {
		digest, err := contentDigest(directory, manifest)
		if err != nil || digest != manifest.ContentDigest {
			return nil, fmt.Errorf("%w: generation content digest", ErrTargetCorrupt)
		}
	} else if err := validateGenerationEntries(directory, manifest); err != nil {
		return nil, err
	}

	generation := &Generation{directory: directory, manifest: manifest}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = generation.Close()
		}
	}()
	trees, shared, err := openGenerationTrees(directory, manifest, openGenerationFrames)
	if err != nil {
		return nil, err
	}
	generation.log = shared
	for _, tree := range trees {
		specification := tree.manifest
		generation.trees = append(generation.trees, tree)
		state := tree.runtime.State()
		baseline := specification.State.runtimeState(specification.SpaceID)
		if (strict && state != baseline) || (!strict && !validLiveState(state, baseline)) {
			return nil, fmt.Errorf("%w: %s Tree state changed", ErrTargetCorrupt, specification.Kind)
		}
		var err error
		switch specification.Kind {
		case "catalog":
			generation.catalog, err = catalogindex.Open(tree.runtime)
		case "current":
			generation.current, err = currentrowindex.Open(tree.runtime)
		case "versions":
			generation.versions, err = rowversionindex.Open(tree.runtime)
		case "fulltext":
			generation.fulltext, err = fulltextindex.Open(tree.runtime)
		default:
			tableID, isTable := tableTreeTableID(specification.Kind)
			if !isTable {
				err = ErrTargetCorrupt
				break
			}
			if generation.tables == nil {
				generation.tables = make(map[string]*generationTree)
			}
			generation.tables[tableID] = tree
		}
		if err != nil {
			return nil, fmt.Errorf("%w: open %s Index: %v", ErrTargetCorrupt, specification.Kind, err)
		}
	}
	if strict {
		afterOpen, err := contentDigest(directory, manifest)
		if err != nil || afterOpen != manifest.ContentDigest {
			return nil, fmt.Errorf("%w: recovery changed generation content", ErrTargetCorrupt)
		}
	}
	closeOnError = false
	return generation, nil
}

func validLiveState(state, baseline treecontrol.State) bool {
	return state.SpaceID == baseline.SpaceID &&
		state.Generation == baseline.Generation &&
		state.Revision >= baseline.Revision &&
		state.NextPageID >= baseline.NextPageID &&
		state.LSN >= baseline.LSN && state.RootPageID != 0
}

// openGenerationTrees opens every Tree of a generation and returns the shared
// redo log when there is one (nil for a pre-v4 generation).
func openGenerationTrees(
	directory string,
	manifest generationManifest,
	capacity uint64,
) ([]*generationTree, *wal.SegmentSet, error) {
	if manifest.sharedLog() {
		return openSharedLogTrees(directory, manifest, capacity)
	}
	var trees []*generationTree
	for _, specification := range manifest.Trees {
		tree, err := openTreeWALTree(directory, specification, capacity)
		if err != nil {
			for index := len(trees) - 1; index >= 0; index-- {
				_ = closeGenerationTree(trees[index], true)
			}
			return nil, nil, fmt.Errorf("%w: open %s Tree: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		trees = append(trees, tree)
	}
	return trees, nil, nil
}

// openSharedLogTrees opens the generation's one redo log, then every Tree over
// it.
//
// Recovery has to run once across all the spaces, before any Runtime exists.
// The shared log interleaves Records from all four spaces and recovery routes
// each one by SpaceID, failing with wal.ErrMissingSpace on a space it was not
// given — so a per-Tree recovery pass would reject the other three Trees'
// Records. That is why the page managers are all opened first and the Runtimes
// are attached afterwards with treecommit.AttachRuntime.
func openSharedLogTrees(
	directory string,
	manifest generationManifest,
	capacity uint64,
) (result []*generationTree, resultLog *wal.SegmentSet, resultErr error) {
	log, err := wal.OpenSegmentSet(filepath.Join(directory, sharedWALDirectory), 0)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open shared redo log: %v", ErrTargetCorrupt, err)
	}
	managers := make([]*page.Manager, 0, len(manifest.Trees))
	defer func() {
		if resultErr == nil {
			return
		}
		for index := len(managers) - 1; index >= 0; index-- {
			_ = managers[index].Close()
		}
		_ = log.Close()
	}()
	spaces := make(map[uint64]wal.PageStore, len(manifest.Trees))
	for _, specification := range manifest.Trees {
		manager, err := page.Open(filepath.Join(directory, specification.PageFile), specification.SpaceID)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: open %s Pages: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		managers = append(managers, manager)
		spaces[specification.SpaceID] = manager
	}
	if _, err := wal.RecoverSegmentSet(log, spaces); err != nil {
		return nil, nil, fmt.Errorf("%w: recover shared redo log: %v", ErrTargetCorrupt, err)
	}
	// One buffer pool for the whole generation, routed by SpaceID. Capacity is
	// a single budget rather than one per Tree, which is what keeps resident
	// memory bounded once the number of Trees follows the number of Tables.
	// See docs/storage/per-table-tree-v1.md §5.5.
	router := buffer.NewRouter()
	for index, specification := range manifest.Trees {
		if err := router.Register(specification.SpaceID, managers[index]); err != nil {
			return nil, nil, fmt.Errorf("%w: register %s Pages: %v", ErrTargetCorrupt, specification.Kind, err)
		}
	}
	pool, err := buffer.New(router, buffer.Config{
		Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
		Writer: router, Durability: log,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("%w: open generation buffer pool: %v", ErrTargetCorrupt, err)
	}
	trees := make([]*generationTree, 0, len(manifest.Trees))
	for index, specification := range manifest.Trees {
		config := runtimeConfig(specification, capacity)
		config.Pool = pool
		runtime, err := treecommit.AttachRuntime(log, managers[index], config)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: attach %s Tree: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		trees = append(trees, &generationTree{
			manifest: specification, set: log, manager: managers[index], runtime: runtime,
		})
	}
	return trees, log, nil
}

// openTreeWALTree opens one Tree of a pre-v4 generation, which owns its log.
func openTreeWALTree(directory string, specification treeManifest, capacity uint64) (*generationTree, error) {
	set, err := wal.OpenSegmentSet(filepath.Join(directory, specification.WALDirectory), 0)
	if err != nil {
		return nil, err
	}
	manager, err := page.Open(filepath.Join(directory, specification.PageFile), specification.SpaceID)
	if err != nil {
		_ = set.Close()
		return nil, err
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, runtimeConfig(specification, capacity))
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return nil, err
	}
	return &generationTree{manifest: specification, set: set, manager: manager, runtime: runtime}, nil
}

func runtimeConfig(specification treeManifest, capacity uint64) treecommit.RuntimeConfig {
	return treecommit.RuntimeConfig{
		SpaceID: specification.SpaceID, Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
	}
}

func (generation *Generation) Catalog() *catalogindex.Index {
	if generation == nil {
		return nil
	}
	return generation.catalog
}

func (generation *Generation) CurrentRows() *currentrowindex.Index {
	if generation == nil {
		return nil
	}
	return generation.current
}

func (generation *Generation) RowVersions() *rowversionindex.Index {
	if generation == nil {
		return nil
	}
	return generation.versions
}

func (generation *Generation) Fulltext() *fulltextindex.Index {
	if generation == nil {
		return nil
	}
	return generation.fulltext
}

func (generation *Generation) PlanDigest() string {
	if generation == nil {
		return ""
	}
	return generation.manifest.PlanDigest
}

func (generation *Generation) SourceFingerprint() string {
	if generation == nil {
		return ""
	}
	return generation.manifest.SourceFingerprint
}

func (generation *Generation) Close() error {
	if generation == nil {
		return nil
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if generation.closed {
		return nil
	}
	generation.closed = true
	var result error
	// A shared log belongs to the generation, not to any one Tree, so it is
	// closed once here rather than four times through the Trees.
	shared := generation.log != nil
	for index := len(generation.trees) - 1; index >= 0; index-- {
		result = errors.Join(result, closeGenerationTree(generation.trees[index], !shared))
	}
	if shared {
		result = errors.Join(result, generation.log.Close())
	}
	return result
}

func closeGenerationTree(tree *generationTree, closeSet bool) error {
	if tree == nil {
		return nil
	}
	var result error
	if closeSet && tree.set != nil {
		result = errors.Join(result, tree.set.Close())
	}
	if tree.manager != nil {
		result = errors.Join(result, tree.manager.Close())
	}
	return result
}
