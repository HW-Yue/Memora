package pagestoremigration

import (
	"fmt"
	"hash/fnv"
	"strings"
)

// A generation holds four fixed Trees plus one Tree per business Table. The
// fixed four are named in expectedTrees; the per-Table ones are derived from
// the Table's own ID, so a manifest can be checked against the Catalog without
// keeping a second registry that could disagree with it.
//
// See docs/storage/per-table-tree-v1.md §2.
const tableTreeKindPrefix = "table:"

// tableSpaceBit marks a space as belonging to a Table. The four fixed spaces
// are small ASCII-derived constants, so setting the top bit puts every derived
// space out of their reach by construction rather than by luck.
const tableSpaceBit = uint64(1) << 63

func tableTreeKind(tableID string) string { return tableTreeKindPrefix + tableID }

// tableTreeTableID returns the Table a Tree kind names, and whether the kind
// names a Table at all.
func tableTreeTableID(kind string) (string, bool) {
	if !strings.HasPrefix(kind, tableTreeKindPrefix) {
		return "", false
	}
	tableID := strings.TrimPrefix(kind, tableTreeKindPrefix)
	if tableID == "" {
		return "", false
	}
	return tableID, true
}

// tableSpaceID derives a Table's space from its ID.
//
// Deriving rather than allocating means the space survives anything that
// rebuilds the generation: the same Table lands in the same space without a
// counter to persist, and a manifest that claims otherwise is detectably wrong.
func tableSpaceID(tableID string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("memora.table-space/v1\x00"))
	_, _ = hash.Write([]byte(tableID))
	return hash.Sum64() | tableSpaceBit
}

// tableTreePageFile names a Table Tree's page file from its space rather than
// its ID, so a Table ID can hold any character a Catalog allows without that
// reaching the filesystem.
func tableTreePageFile(spaceID uint64) string {
	return fmt.Sprintf("table_%016x.pages", spaceID)
}

func tableTreeManifest(tableID string) treeManifest {
	spaceID := tableSpaceID(tableID)
	return treeManifest{
		Kind: tableTreeKind(tableID), SpaceID: spaceID, PageFile: tableTreePageFile(spaceID),
	}
}

// validTableTree reports whether a Tree specification is a well-formed
// per-Table Tree: every field has to follow from the Table it names.
func validTableTree(tree treeManifest) bool {
	tableID, ok := tableTreeTableID(tree.Kind)
	if !ok {
		return false
	}
	expected := tableTreeManifest(tableID)
	return tree.SpaceID == expected.SpaceID && tree.PageFile == expected.PageFile &&
		tree.WALDirectory == ""
}

// A Table has two derived Trees: the clustered Tree holding its current Rows,
// and the history Tree holding every revision of those Rows keyed
// (row_id, sequence). They are separate Trees rather than two key ranges of one
// because history is a Table in its own right — `notes` and `notes_history` —
// and because a scan of current Rows must not walk past revisions it will
// discard. See docs/storage/per-table-tree-v1.md §2 and §4.
const historyTreeKindPrefix = "history:"

func historyTreeKind(tableID string) string { return historyTreeKindPrefix + tableID }

// historyTreeTableID returns the Table a history Tree kind names, and whether
// the kind names one at all.
func historyTreeTableID(kind string) (string, bool) {
	if !strings.HasPrefix(kind, historyTreeKindPrefix) {
		return "", false
	}
	tableID := strings.TrimPrefix(kind, historyTreeKindPrefix)
	if tableID == "" {
		return "", false
	}
	return tableID, true
}

// historySpaceID derives a Table's history space from its ID, the same way
// tableSpaceID does and with a different domain prefix — so one Table's two
// Trees can never be handed the same space, which the shared buffer pool keys
// its frames by.
func historySpaceID(tableID string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte("memora.table-history-space/v1\x00"))
	_, _ = hash.Write([]byte(tableID))
	return hash.Sum64() | tableSpaceBit
}

func historyTreePageFile(spaceID uint64) string {
	return fmt.Sprintf("history_%016x.pages", spaceID)
}

func historyTreeManifest(tableID string) treeManifest {
	spaceID := historySpaceID(tableID)
	return treeManifest{
		Kind: historyTreeKind(tableID), SpaceID: spaceID, PageFile: historyTreePageFile(spaceID),
	}
}

// validHistoryTree reports whether a Tree specification is a well-formed
// per-Table history Tree: every field has to follow from the Table it names.
func validHistoryTree(tree treeManifest) bool {
	tableID, ok := historyTreeTableID(tree.Kind)
	if !ok {
		return false
	}
	expected := historyTreeManifest(tableID)
	return tree.SpaceID == expected.SpaceID && tree.PageFile == expected.PageFile &&
		tree.WALDirectory == ""
}
