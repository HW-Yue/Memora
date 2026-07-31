package btree

import (
	"bytes"
	"errors"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
)

const maxSearchDepth = 64

var ErrNotFound = errors.New("B+ Tree key was not found")

type Reader interface {
	Read(uint64) (page.Page, error)
}

type ReaderFunc func(uint64) (page.Page, error)

func (function ReaderFunc) Read(pageID uint64) (page.Page, error) { return function(pageID) }

type Searcher struct {
	spaceID    uint64
	rootPageID uint64
	reader     Reader
}

func NewSearcher(spaceID, rootPageID uint64, reader Reader) (*Searcher, error) {
	if rootPageID == 0 || reader == nil {
		return nil, fmt.Errorf("%w: Searcher configuration", ErrInvalid)
	}
	return &Searcher{spaceID: spaceID, rootPageID: rootPageID, reader: reader}, nil
}

func (searcher *Searcher) Get(key []byte) ([]byte, error) {
	if len(key) == 0 {
		return nil, fmt.Errorf("%w: empty search key", ErrInvalid)
	}
	pageID := searcher.rootPageID
	seen := make(map[uint64]struct{})
	expectedLevel := -1
	for depth := 0; depth < maxSearchDepth; depth++ {
		if _, duplicate := seen[pageID]; duplicate {
			return nil, fmt.Errorf("%w: repeated Page %d", ErrCorrupt, pageID)
		}
		seen[pageID] = struct{}{}
		value, err := searcher.reader.Read(pageID)
		if err != nil {
			return nil, fmt.Errorf("read B+ Tree Page %d: %w", pageID, err)
		}
		if value.Header.SpaceID != searcher.spaceID || value.Header.PageID != pageID {
			return nil, fmt.Errorf("%w: Page identity at %d", ErrCorrupt, pageID)
		}
		node, err := Decode(value)
		if err != nil {
			return nil, err
		}
		if expectedLevel >= 0 && int(node.Level) != expectedLevel {
			return nil, fmt.Errorf("%w: level %d, want %d", ErrCorrupt, node.Level, expectedLevel)
		}
		switch node.Kind {
		case KindLeaf:
			index := sort.Search(len(node.LeafEntries), func(index int) bool {
				return bytes.Compare(node.LeafEntries[index].Key, key) >= 0
			})
			if index == len(node.LeafEntries) || !bytes.Equal(node.LeafEntries[index].Key, key) {
				return nil, ErrNotFound
			}
			return bytes.Clone(node.LeafEntries[index].Value), nil
		case KindInternal:
			index := sort.Search(len(node.InternalEntries), func(index int) bool {
				return bytes.Compare(key, node.InternalEntries[index].Key) < 0
			})
			pageID = node.LeftmostChild
			if index > 0 {
				pageID = node.InternalEntries[index-1].RightChild
			}
			expectedLevel = int(node.Level) - 1
		default:
			return nil, fmt.Errorf("%w: Node kind", ErrCorrupt)
		}
	}
	return nil, fmt.Errorf("%w: depth exceeds %d", ErrCorrupt, maxSearchDepth)
}
