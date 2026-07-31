package btree

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func DeleteLeaf(header page.Header, node Node, key []byte) (Node, []byte, error) {
	if header.Type != page.TypeBTreeLeaf || node.Kind != KindLeaf || len(key) == 0 {
		return Node{}, nil, fmt.Errorf("%w: leaf delete input", ErrInvalid)
	}
	if _, err := Encode(header, node); err != nil {
		return Node{}, nil, err
	}
	index := sort.Search(len(node.LeafEntries), func(index int) bool {
		return bytes.Compare(node.LeafEntries[index].Key, key) >= 0
	})
	if index == len(node.LeafEntries) || !bytes.Equal(node.LeafEntries[index].Key, key) {
		return Node{}, nil, ErrNotFound
	}
	candidate := cloneNode(node)
	removed := bytes.Clone(candidate.LeafEntries[index].Value)
	copy(candidate.LeafEntries[index:], candidate.LeafEntries[index+1:])
	candidate.LeafEntries[len(candidate.LeafEntries)-1] = LeafEntry{}
	candidate.LeafEntries = candidate.LeafEntries[:len(candidate.LeafEntries)-1]
	if _, err := Encode(header, candidate); err != nil {
		return Node{}, nil, err
	}
	return candidate, removed, nil
}
