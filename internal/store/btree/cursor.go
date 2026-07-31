package btree

import (
	"bytes"
	"fmt"
	"sort"
	"sync"
)

type RangeBatch struct {
	Entries []LeafEntry
	Done    bool
}

type Cursor struct {
	mu          sync.Mutex
	searcher    *Searcher
	start       []byte
	end         []byte
	initialized bool
	done        bool
	poison      error
	pageID      uint64
	node        Node
	index       int
	visited     map[uint64]struct{}
}

func (searcher *Searcher) NewCursor(startInclusive, endExclusive []byte) (*Cursor, error) {
	if len(startInclusive) > 0 && len(endExclusive) > 0 &&
		bytes.Compare(startInclusive, endExclusive) > 0 {
		return nil, fmt.Errorf("%w: range start is after end", ErrInvalid)
	}
	return &Cursor{
		searcher: searcher,
		start:    bytes.Clone(startInclusive),
		end:      bytes.Clone(endExclusive),
		visited:  make(map[uint64]struct{}),
	}, nil
}

func (cursor *Cursor) Next(limit uint64) (RangeBatch, error) {
	cursor.mu.Lock()
	defer cursor.mu.Unlock()
	if limit == 0 {
		return RangeBatch{}, fmt.Errorf("%w: cursor limit is zero", ErrInvalid)
	}
	if cursor.poison != nil {
		return RangeBatch{}, cursor.poison
	}
	if cursor.done {
		return RangeBatch{Done: true}, nil
	}
	if !cursor.initialized {
		if err := cursor.initialize(); err != nil {
			cursor.poison = err
			return RangeBatch{}, err
		}
		cursor.initialized = true
		if cursor.done {
			return RangeBatch{Done: true}, nil
		}
	}

	entries := make([]LeafEntry, 0, min(limit, uint64(64)))
	for {
		for cursor.index < len(cursor.node.LeafEntries) {
			entry := cursor.node.LeafEntries[cursor.index]
			if len(cursor.end) > 0 && bytes.Compare(entry.Key, cursor.end) >= 0 {
				cursor.done = true
				return RangeBatch{Entries: entries, Done: true}, nil
			}
			entries = append(entries, LeafEntry{
				Key: bytes.Clone(entry.Key), Value: bytes.Clone(entry.Value),
			})
			cursor.index++
			if uint64(len(entries)) == limit {
				done := cursor.index == len(cursor.node.LeafEntries) &&
					cursor.node.NextLeafPageID == 0
				if !done && cursor.index < len(cursor.node.LeafEntries) &&
					len(cursor.end) > 0 &&
					bytes.Compare(cursor.node.LeafEntries[cursor.index].Key, cursor.end) >= 0 {
					done = true
				}
				cursor.done = done
				return RangeBatch{Entries: entries, Done: done}, nil
			}
		}
		if cursor.node.NextLeafPageID == 0 {
			cursor.done = true
			return RangeBatch{Entries: entries, Done: true}, nil
		}
		if err := cursor.advanceLeaf(); err != nil {
			cursor.poison = err
			return RangeBatch{}, err
		}
		if len(cursor.end) > 0 &&
			bytes.Compare(cursor.node.LeafEntries[0].Key, cursor.end) >= 0 {
			cursor.done = true
			return RangeBatch{Entries: entries, Done: true}, nil
		}
	}
}

func (cursor *Cursor) initialize() error {
	pageID := cursor.searcher.rootPageID
	expectedLevel := -1
	for depth := 0; depth < maxSearchDepth; depth++ {
		node, err := cursor.readNode(pageID)
		if err != nil {
			return err
		}
		if expectedLevel >= 0 && int(node.Level) != expectedLevel {
			return fmt.Errorf("%w: level %d, want %d", ErrCorrupt, node.Level, expectedLevel)
		}
		switch node.Kind {
		case KindLeaf:
			if len(node.LeafEntries) == 0 {
				if pageID != cursor.searcher.rootPageID || node.NextLeafPageID != 0 {
					return fmt.Errorf("%w: empty non-root leaf", ErrCorrupt)
				}
				cursor.done = true
				return nil
			}
			cursor.pageID = pageID
			cursor.node = node
			cursor.index = 0
			if len(cursor.start) > 0 {
				cursor.index = sort.Search(len(node.LeafEntries), func(index int) bool {
					return bytes.Compare(node.LeafEntries[index].Key, cursor.start) >= 0
				})
			}
			return nil
		case KindInternal:
			index := 0
			if len(cursor.start) > 0 {
				index = sort.Search(len(node.InternalEntries), func(index int) bool {
					return bytes.Compare(cursor.start, node.InternalEntries[index].Key) < 0
				})
			}
			pageID = node.LeftmostChild
			if index > 0 {
				pageID = node.InternalEntries[index-1].RightChild
			}
			expectedLevel = int(node.Level) - 1
		default:
			return fmt.Errorf("%w: Node kind", ErrCorrupt)
		}
	}
	return fmt.Errorf("%w: depth exceeds %d", ErrCorrupt, maxSearchDepth)
}

func (cursor *Cursor) advanceLeaf() error {
	nextPageID := cursor.node.NextLeafPageID
	previousLast := cursor.node.LeafEntries[len(cursor.node.LeafEntries)-1].Key
	node, err := cursor.readNode(nextPageID)
	if err != nil {
		return err
	}
	if node.Kind != KindLeaf || node.Level != 0 {
		return fmt.Errorf("%w: successor Page is not a leaf", ErrCorrupt)
	}
	if len(node.LeafEntries) == 0 {
		return fmt.Errorf("%w: empty successor leaf", ErrCorrupt)
	}
	if bytes.Compare(previousLast, node.LeafEntries[0].Key) >= 0 {
		return fmt.Errorf("%w: leaf chain key order", ErrCorrupt)
	}
	cursor.pageID = nextPageID
	cursor.node = node
	cursor.index = 0
	return nil
}

func (cursor *Cursor) readNode(pageID uint64) (Node, error) {
	if _, duplicate := cursor.visited[pageID]; duplicate {
		return Node{}, fmt.Errorf("%w: repeated Page %d", ErrCorrupt, pageID)
	}
	cursor.visited[pageID] = struct{}{}
	value, err := cursor.searcher.reader.Read(pageID)
	if err != nil {
		return Node{}, fmt.Errorf("read B+ Tree Page %d: %w", pageID, err)
	}
	if value.Header.SpaceID != cursor.searcher.spaceID || value.Header.PageID != pageID {
		return Node{}, fmt.Errorf("%w: Page identity at %d", ErrCorrupt, pageID)
	}
	node, err := Decode(value)
	if err != nil {
		return Node{}, err
	}
	return node, nil
}
