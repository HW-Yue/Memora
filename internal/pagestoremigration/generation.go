package pagestoremigration

import (
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/buffer"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
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

// walRingBytes is the hard cap on the redo log's in-use span.
//
// Maintenance already bounds the log while the checkpoint can advance. It
// cannot always — a checkpoint needs the dirty Pages flushed, and a flush that
// fails stops the tail — and before the ring nothing noticed: the log just kept
// growing. A bound a policy can quietly fail to enforce is not a bound.
//
// 16 MiB is four roll thresholds, so an ordinary write never approaches it and
// only a genuinely stuck checkpoint does. Reaching it is a loud error rather
// than a silent overwrite; see wal.ErrRingFull.
//
// A var for the same reason as the threshold above: tests lower it.
var walRingBytes = uint64(16 << 20)

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
	return runRedoMaintenance(log, barrier)
}

// relieveRedoRing runs a maintenance round when the ring has no room left,
// before the write rather than after it.
//
// The ordinary round is driven by the roll threshold and runs after a
// publication; that keeps the log small but is not what stops it overflowing.
// This is: a full ring means the next commit is refused, and the one thing that
// can free space is a checkpoint moving the tail. So the last chance to avoid
// back-pressure is taken here, and if it does not work the commit fails loudly.
//
// See docs/storage/shared-circular-redo-v1.md §3.
func relieveRedoRing(log *wal.SegmentSet, barrier redoBarrier) error {
	if log == nil {
		return nil
	}
	capacity := log.RingBytes()
	if capacity == 0 {
		return nil
	}
	inUse, err := log.InUseBytes()
	if err != nil {
		return fmt.Errorf("read redo ring usage: %w", err)
	}
	if inUse < capacity {
		return nil
	}
	return runRedoMaintenance(log, barrier)
}

// runRedoMaintenance is one round: roll, checkpoint, reclaim. The three benign
// sentinels mean "nothing to do" and are swallowed; anything else is reported.
func runRedoMaintenance(log *wal.SegmentSet, barrier redoBarrier) error {
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
	return maintainRedoLog(generation.log, generation.redoBarrier())
}

// relieveRedoRing frees ring space before a publication when the ring is
// already full. The caller must hold the Authority write lock.
func (generation *Generation) relieveRedoRing() error {
	if generation == nil || generation.log == nil {
		return nil
	}
	return relieveRedoRing(generation.log, generation.redoBarrier())
}

// redoBarrier is every Tree in this generation, which is what a checkpoint has
// to flush before the log's tail may move past their changes.
func (generation *Generation) redoBarrier() redoBarrier {
	targets := make([]flushTarget, 0, len(generation.trees))
	for _, tree := range generation.trees {
		targets = append(targets, flushTarget{
			kind: tree.manifest.Kind, runtime: tree.runtime, manager: tree.manager,
		})
	}
	return redoBarrier{targets: targets}
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
	versions *rowversionindex.Index
	fulltext *fulltextindex.Index
	// objects is the clustered store for everything that keeps one record per
	// identity — Route, Relation, Configuration and the rest. It replaces the
	// record file's process-resident map as their index.
	// See docs/storage/physical-index-v1.md.
	objects *objectindex.Index
	// tables holds the per-Table Trees, keyed by Table ID. The four fixed Trees
	// above are named fields because there is exactly one of each; these follow
	// the Catalog and so cannot be. See docs/storage/per-table-tree-v1.md §2.
	tables map[string]*generationTree
	// tableRows is the current Row Index of each Table's Tree, opened once when
	// the Tree is.
	tableRows map[string]*currentrowindex.Index
	// histories holds each Table's history Tree and the Index over it. A Table
	// has two Trees: its current Rows and every revision of them.
	// See docs/storage/per-table-tree-v1.md §4.
	histories     map[string]*generationTree
	tableVersions map[string]*rowversionindex.Index
	// router and pool are the generation's one buffer pool and the SpaceID
	// routing behind it. A Tree added after open registers with them rather
	// than building a pool of its own.
	router *buffer.Router
	pool   *buffer.Pool
	closed bool
}

// EnsureTableTrees opens a Tree for every Table it is given, creating the ones
// that do not exist yet, and records them in the manifest.
//
// A Table's Tree is created when the Table is, so the two never disagree about
// which Tables exist. Creating it is idempotent: an existing Tree is left
// exactly as it is, which is what makes calling this after every Catalog
// publication safe.
func (generation *Generation) EnsureTableTrees(tableIDs []string) error {
	if generation == nil {
		return fmt.Errorf("%w: generation", ErrInvalid)
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	if generation.closed {
		return fmt.Errorf("%w: generation is closed", ErrInvalid)
	}
	// Only a shared-log generation can grow: a per-Tree-log layout would need a
	// new log directory per Table, and those layouts are upgraded to v4 on open
	// rather than extended.
	if generation.log == nil || generation.pool == nil || generation.router == nil {
		return fmt.Errorf("%w: generation cannot grow Trees", ErrInvalid)
	}
	// Completeness is decided per Tree, not per Table. A generation written
	// before history became a Table has the clustered Tree and no history Tree;
	// deciding by the Table would skip it forever and every publication into it
	// would fail for want of a Tree that never grows.
	missing := make([]missingTree, 0, 2*len(tableIDs))
	seen := make(map[string]bool, len(tableIDs))
	for _, tableID := range tableIDs {
		if tableID == "" || seen[tableID] {
			continue
		}
		seen[tableID] = true
		if generation.tables[tableID] == nil {
			missing = append(missing, missingTree{tableID: tableID})
		}
		if generation.histories[tableID] == nil {
			missing = append(missing, missingTree{tableID: tableID, history: true})
		}
	}
	if len(missing) == 0 {
		return nil
	}
	// Sorted so two runs over the same Catalog produce the same Tree order, and
	// so a Table's two Trees stay adjacent.
	sort.Slice(missing, func(left, right int) bool {
		if missing[left].tableID != missing[right].tableID {
			return missing[left].tableID < missing[right].tableID
		}
		return !missing[left].history
	})
	opened := make([]*generationTree, 0, len(missing))
	defer func() {
		// Anything opened before a failure is closed here; the caller sees the
		// generation exactly as it was.
		if len(opened) == 0 {
			return
		}
		for index := len(opened) - 1; index >= 0; index-- {
			_ = closeGenerationTree(opened[index], false)
		}
	}()
	for _, want := range missing {
		tree, err := generation.openDerivedTreeLocked(want.manifest(), want.bootstrap)
		if err != nil {
			return fmt.Errorf("%w: Table %q %s Tree: %v", ErrTargetCorrupt, want.tableID, want.name(), err)
		}
		opened = append(opened, tree)
	}
	manifest := generation.manifest
	manifest.Trees = append(append([]treeManifest(nil), manifest.Trees...), func() []treeManifest {
		added := make([]treeManifest, 0, len(opened))
		for _, tree := range opened {
			added = append(added, tree.manifest)
		}
		return added
	}()...)
	if err := rewriteManifest(generation.directory, manifest); err != nil {
		return err
	}
	// The manifest is the record of which Trees exist, so it lands before the
	// Trees are published in memory: a crash between the two reopens a
	// generation whose manifest already lists them, and opening them again is
	// what EnsureTableTrees does anyway.
	generation.manifest = manifest
	if generation.tables == nil {
		generation.tables = make(map[string]*generationTree, len(missing))
		generation.tableRows = make(map[string]*currentrowindex.Index, len(missing))
	}
	if generation.histories == nil {
		generation.histories = make(map[string]*generationTree, len(missing))
		generation.tableVersions = make(map[string]*rowversionindex.Index, len(missing))
	}
	for position, want := range missing {
		tree := opened[position]
		if want.history {
			index, err := rowversionindex.Open(tree.runtime)
			if err != nil {
				return fmt.Errorf("%w: open Table %q history Index: %v", ErrTargetCorrupt, want.tableID, err)
			}
			generation.histories[want.tableID], generation.tableVersions[want.tableID] = tree, index
		} else {
			index, err := currentrowindex.Open(tree.runtime)
			if err != nil {
				return fmt.Errorf("%w: open Table %q Index: %v", ErrTargetCorrupt, want.tableID, err)
			}
			generation.tables[want.tableID], generation.tableRows[want.tableID] = tree, index
		}
		generation.trees = append(generation.trees, tree)
	}
	opened = nil
	return nil
}

// missingTree names one derived Tree a Table is expected to have and does not.
// The two differ only in their key space, so everything about opening them is
// shared and only the manifest and the bootstrap are per kind.
type missingTree struct {
	tableID string
	history bool
}

func (want missingTree) name() string {
	if want.history {
		return "history"
	}
	return "clustered"
}

func (want missingTree) manifest() treeManifest {
	if want.history {
		return historyTreeManifest(want.tableID)
	}
	return tableTreeManifest(want.tableID)
}

func (want missingTree) bootstrap(runtime *treecommit.Runtime, transactionID uint64) error {
	if want.history {
		index, err := rowversionindex.Open(runtime)
		if err != nil {
			return err
		}
		_, err = index.Bootstrap(transactionID, nil)
		return err
	}
	index, err := currentrowindex.Open(runtime)
	if err != nil {
		return err
	}
	_, err = index.Bootstrap(transactionID, nil, 0)
	return err
}

// openDerivedTreeLocked opens one derived Tree's page file, registers it with
// the generation's router and attaches it to the shared redo log.
//
// A Tree is born with an empty B+ root rather than no root at all: "created but
// rootless" would be a second empty state for every reader to know about, and
// the manifest's own invariant is that a Tree has a root. bootstrap plants that
// root, and differs per Tree because the key space does.
func (generation *Generation) openDerivedTreeLocked(
	specification treeManifest, bootstrap func(*treecommit.Runtime, uint64) error,
) (*generationTree, error) {
	path := filepath.Join(generation.directory, specification.PageFile)
	manager, err := page.Open(path, specification.SpaceID)
	if errors.Is(err, os.ErrNotExist) {
		// First time this Tree is seen: its page file does not exist yet.
		manager, err = page.Create(path, specification.SpaceID)
	}
	if err != nil {
		return nil, fmt.Errorf("open Pages: %w", err)
	}
	if err := generation.router.Register(specification.SpaceID, manager); err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("register Pages: %w", err)
	}
	runtime, err := treecommit.AttachRuntime(generation.log, manager, treecommit.RuntimeConfig{
		SpaceID: specification.SpaceID, Pool: generation.pool,
	})
	if err != nil {
		_ = manager.Close()
		return nil, fmt.Errorf("attach Tree: %w", err)
	}
	if runtime.State().RootPageID == 0 {
		transactionID, frontierErr := generation.nextTransactionIDLocked()
		if frontierErr != nil {
			_ = manager.Close()
			return nil, frontierErr
		}
		if err := bootstrap(runtime, transactionID); err != nil {
			_ = manager.Close()
			return nil, fmt.Errorf("bootstrap Tree: %w", err)
		}
	}
	specification.State = treeStateFromRuntime(runtime.State())
	return &generationTree{
		manifest: specification, set: generation.log, manager: manager, runtime: runtime,
	}, nil
}

// nextTransactionIDLocked picks the next WAL transaction ID on the shared log.
// Every Tree commits into that one log, so the ID space is the log's, not any
// one Tree's.
func (generation *Generation) nextTransactionIDLocked() (uint64, error) {
	frontier, err := generation.log.DurableFrontier()
	if err != nil {
		return 0, fmt.Errorf("%w: read redo frontier: %v", ErrTargetCorrupt, err)
	}
	if frontier.LastTransactionID == math.MaxUint64 {
		return 0, fmt.Errorf("%w: redo transaction IDs are exhausted", ErrTargetCorrupt)
	}
	return frontier.LastTransactionID + 1, nil
}

// CurrentRowsFor returns a Table's current Row Index, or nil when the Table has
// no Tree in this generation.
func (generation *Generation) CurrentRowsFor(tableID string) *currentrowindex.Index {
	if generation == nil {
		return nil
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.tableRows[tableID]
}

// TableTree reports whether a Table has a Tree in this generation.
func (generation *Generation) TableTree(tableID string) bool {
	if generation == nil {
		return false
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.tables[tableID] != nil
}

// Objects returns the clustered Index for objects that keep one record per
// identity, or nil for a generation written before it existed.
// CatalogObjects satisfies nativecatalog.ObjectSource. The Catalog reader is
// bound to one generation and rebuilt when the generation is replaced, so this
// hands out that generation's Tree rather than whatever the Authority currently
// holds.
func (generation *Generation) CatalogObjects() *objectindex.Index {
	return generation.Objects()
}

func (generation *Generation) Objects() *objectindex.Index {
	if generation == nil {
		return nil
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.objects
}

// HistoryTree reports whether a Table has a history Tree in this generation.
func (generation *Generation) HistoryTree(tableID string) bool {
	if generation == nil {
		return false
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.histories[tableID] != nil
}

// HistoryFor returns a Table's history Index — every revision of that Table's
// Rows, keyed (Row, revision) — or nil when the Table has no history Tree.
func (generation *Generation) HistoryFor(tableID string) *rowversionindex.Index {
	if generation == nil {
		return nil
	}
	generation.mu.Lock()
	defer generation.mu.Unlock()
	return generation.tableVersions[tableID]
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
	trees, shared, router, pool, err := openGenerationTrees(directory, manifest, openGenerationFrames)
	if err != nil {
		return nil, err
	}
	generation.log, generation.router, generation.pool = shared, router, pool
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
		case "versions":
			generation.versions, err = rowversionindex.Open(tree.runtime)
		case "fulltext":
			generation.fulltext, err = fulltextindex.Open(tree.runtime)
		case "objects":
			generation.objects, err = objectindex.Open(tree.runtime)
		case "current":
			// A pre-v5 generation's shared current Row Tree. It is opened as a
			// Tree — its pages have to be recovered like any other — but no
			// Index is built over it: the Authority replaces the generation on
			// open, and reads are served from the new one.
		default:
			if tableID, isTable := tableTreeTableID(specification.Kind); isTable {
				if generation.tables == nil {
					generation.tables = make(map[string]*generationTree)
					generation.tableRows = make(map[string]*currentrowindex.Index)
				}
				index, openErr := currentrowindex.Open(tree.runtime)
				if openErr != nil {
					err = openErr
					break
				}
				generation.tables[tableID], generation.tableRows[tableID] = tree, index
				break
			}
			tableID, isHistory := historyTreeTableID(specification.Kind)
			if !isHistory {
				err = ErrTargetCorrupt
				break
			}
			if generation.histories == nil {
				generation.histories = make(map[string]*generationTree)
				generation.tableVersions = make(map[string]*rowversionindex.Index)
			}
			index, openErr := rowversionindex.Open(tree.runtime)
			if openErr != nil {
				err = openErr
				break
			}
			generation.histories[tableID], generation.tableVersions[tableID] = tree, index
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
) ([]*generationTree, *wal.SegmentSet, *buffer.Router, *buffer.Pool, error) {
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
			return nil, nil, nil, nil, fmt.Errorf("%w: open %s Tree: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		trees = append(trees, tree)
	}
	return trees, nil, nil, nil, nil
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
) (result []*generationTree, resultLog *wal.SegmentSet, resultRouter *buffer.Router, resultPool *buffer.Pool, resultErr error) {
	log, err := wal.OpenSegmentSetWithCapacity(filepath.Join(directory, sharedWALDirectory), 0, walRingBytes)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w: open shared redo log: %v", ErrTargetCorrupt, err)
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
			return nil, nil, nil, nil, fmt.Errorf("%w: open %s Pages: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		managers = append(managers, manager)
		spaces[specification.SpaceID] = manager
	}
	if _, err := wal.RecoverSegmentSet(log, spaces); err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w: recover shared redo log: %v", ErrTargetCorrupt, err)
	}
	// One buffer pool for the whole generation, routed by SpaceID. Capacity is
	// a single budget rather than one per Tree, which is what keeps resident
	// memory bounded once the number of Trees follows the number of Tables.
	// See docs/storage/per-table-tree-v1.md §5.5.
	router := buffer.NewRouter()
	for index, specification := range manifest.Trees {
		if err := router.Register(specification.SpaceID, managers[index]); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: register %s Pages: %v", ErrTargetCorrupt, specification.Kind, err)
		}
	}
	pool, err := buffer.New(router, buffer.Config{
		Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
		Writer: router, Durability: log,
	})
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("%w: open generation buffer pool: %v", ErrTargetCorrupt, err)
	}
	trees := make([]*generationTree, 0, len(manifest.Trees))
	for index, specification := range manifest.Trees {
		config := runtimeConfig(specification, capacity)
		config.Pool = pool
		runtime, err := treecommit.AttachRuntime(log, managers[index], config)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("%w: attach %s Tree: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		trees = append(trees, &generationTree{
			manifest: specification, set: log, manager: managers[index], runtime: runtime,
		})
	}
	return trees, log, router, pool, nil
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
