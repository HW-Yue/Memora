package btree

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func UpsertLeaf(
	header page.Header,
	node Node,
	key []byte,
	value []byte,
) (Node, bool, error) {
	if len(key) == 0 || len(value) == 0 {
		return Node{}, false, fmt.Errorf("%w: empty leaf key/value", ErrInvalid)
	}
	if _, err := Encode(header, node); err != nil {
		return Node{}, false, err
	}
	candidate := cloneNode(node)
	index := sort.Search(len(candidate.LeafEntries), func(index int) bool {
		return bytes.Compare(candidate.LeafEntries[index].Key, key) >= 0
	})
	replaced := index < len(candidate.LeafEntries) &&
		bytes.Equal(candidate.LeafEntries[index].Key, key)
	entry := LeafEntry{Key: bytes.Clone(key), Value: bytes.Clone(value)}
	if replaced {
		candidate.LeafEntries[index] = entry
	} else {
		candidate.LeafEntries = append(candidate.LeafEntries, LeafEntry{})
		copy(candidate.LeafEntries[index+1:], candidate.LeafEntries[index:])
		candidate.LeafEntries[index] = entry
	}
	if _, err := Encode(header, candidate); err != nil {
		return Node{}, false, err
	}
	return candidate, replaced, nil
}

func UpsertInternal(
	header page.Header,
	node Node,
	key []byte,
	rightChild uint64,
) (Node, bool, error) {
	if len(key) == 0 || rightChild == 0 {
		return Node{}, false, fmt.Errorf("%w: empty separator or zero child", ErrInvalid)
	}
	if _, err := Encode(header, node); err != nil {
		return Node{}, false, err
	}
	candidate := cloneNode(node)
	index := sort.Search(len(candidate.InternalEntries), func(index int) bool {
		return bytes.Compare(candidate.InternalEntries[index].Key, key) >= 0
	})
	replaced := index < len(candidate.InternalEntries) &&
		bytes.Equal(candidate.InternalEntries[index].Key, key)
	entry := InternalEntry{Key: bytes.Clone(key), RightChild: rightChild}
	if replaced {
		candidate.InternalEntries[index] = entry
	} else {
		candidate.InternalEntries = append(candidate.InternalEntries, InternalEntry{})
		copy(candidate.InternalEntries[index+1:], candidate.InternalEntries[index:])
		candidate.InternalEntries[index] = entry
	}
	if _, err := Encode(header, candidate); err != nil {
		return Node{}, false, err
	}
	return candidate, replaced, nil
}

func cloneNode(node Node) Node {
	cloned := node
	cloned.LeafEntries = make([]LeafEntry, len(node.LeafEntries))
	for index, entry := range node.LeafEntries {
		cloned.LeafEntries[index] = LeafEntry{
			Key: bytes.Clone(entry.Key), Value: bytes.Clone(entry.Value),
		}
	}
	cloned.InternalEntries = make([]InternalEntry, len(node.InternalEntries))
	for index, entry := range node.InternalEntries {
		cloned.InternalEntries[index] = InternalEntry{
			Key: bytes.Clone(entry.Key), RightChild: entry.RightChild,
		}
	}
	return cloned
}
