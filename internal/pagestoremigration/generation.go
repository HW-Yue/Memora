package pagestoremigration

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

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
	closed   bool
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
			err = ErrTargetCorrupt
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
	trees := make([]*generationTree, 0, len(manifest.Trees))
	for index, specification := range manifest.Trees {
		runtime, err := treecommit.AttachRuntime(log, managers[index], runtimeConfig(specification, capacity))
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
