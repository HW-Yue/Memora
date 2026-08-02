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
	trees     []*generationTree
	catalog   *catalogindex.Index
	current   *currentrowindex.Index
	versions  *rowversionindex.Index
	fulltext  *fulltextindex.Index
	closed    bool
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
		digest, err := contentDigest(directory, manifest.Trees)
		if err != nil || digest != manifest.ContentDigest {
			return nil, fmt.Errorf("%w: generation content digest", ErrTargetCorrupt)
		}
	} else if err := validateGenerationEntries(directory, manifest.Trees); err != nil {
		return nil, err
	}

	generation := &Generation{directory: directory, manifest: manifest}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = generation.Close()
		}
	}()
	for _, specification := range manifest.Trees {
		tree, err := openGenerationTree(directory, specification, openGenerationFrames)
		if err != nil {
			return nil, fmt.Errorf("%w: open %s Tree: %v", ErrTargetCorrupt, specification.Kind, err)
		}
		state := tree.runtime.State()
		baseline := specification.State.runtimeState(specification.SpaceID)
		if (strict && state != baseline) || (!strict && !validLiveState(state, baseline)) {
			_ = closeGenerationTree(tree)
			return nil, fmt.Errorf("%w: %s Tree state changed", ErrTargetCorrupt, specification.Kind)
		}
		generation.trees = append(generation.trees, tree)
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
		afterOpen, err := contentDigest(directory, manifest.Trees)
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

func openGenerationTree(directory string, specification treeManifest, capacity uint64) (*generationTree, error) {
	set, err := wal.OpenSegmentSet(filepath.Join(directory, specification.WALDirectory), 0)
	if err != nil {
		return nil, err
	}
	manager, err := page.Open(filepath.Join(directory, specification.PageFile), specification.SpaceID)
	if err != nil {
		_ = set.Close()
		return nil, err
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: specification.SpaceID, Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		return nil, err
	}
	return &generationTree{manifest: specification, set: set, manager: manager, runtime: runtime}, nil
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
	for index := len(generation.trees) - 1; index >= 0; index-- {
		result = errors.Join(result, closeGenerationTree(generation.trees[index]))
	}
	return result
}

func closeGenerationTree(tree *generationTree) error {
	if tree == nil {
		return nil
	}
	var result error
	if tree.set != nil {
		result = errors.Join(result, tree.set.Close())
	}
	if tree.manager != nil {
		result = errors.Join(result, tree.manager.Close())
	}
	return result
}
