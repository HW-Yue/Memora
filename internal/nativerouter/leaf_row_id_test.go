package nativerouter

import (
	"encoding/binary"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
)

// TestLeafCarriesItsRowID is E3 stage 1's gate.
//
// A semantic leaf and the Row hanging under it are related by a separate
// Membership object today, which is a whole object kind, a validation surface
// and three classes of health problem, all to express one field. The leaf
// carries the RowID instead.
//
// See docs/storage/leaf-rowid-v1.md §4.
func TestLeafCarriesItsRowID(t *testing.T) {
	leaf := router.Node{
		Version: router.Version, ID: "route_leaf", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Name: "architecture", Path: "/architecture",
		Kind: router.KindLeaf, Purpose: "Architecture decisions", Synopsis: "Decisions",
		RowID: "row_decision", Revision: 2,
	}
	encoded, err := encodeNode(leaf)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeNode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.RowID != leaf.RowID {
		t.Fatalf("decoded RowID = %q, want %q", decoded.RowID, leaf.RowID)
	}
	if decoded.Synopsis != leaf.Synopsis || decoded.Path != leaf.Path {
		t.Fatalf("round trip lost an existing field: %#v", decoded)
	}
}

// TestNodeWithoutARowIDStillDecodes keeps the field additive.
//
// Synopsis was added the same way — appended after the fixed text array, read
// back behind an offset guard — and records written before it still decode. A
// RowID that widened the fixed array instead would make every record written
// before today unreadable, which is not a migration, it is data loss.
func TestNodeWithoutARowIDStillDecodes(t *testing.T) {
	before := encodeNodeWithoutRowID(t, router.Node{
		Version: router.Version, ID: "route_old", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Name: "legacy", Path: "/legacy", Kind: router.KindLeaf,
		Purpose: "Written before leaves carried a RowID", Synopsis: "Legacy",
		Aliases: []string{"old"}, Revision: 3,
	})
	decoded, err := decodeNode(before)
	if err != nil {
		t.Fatalf("a Route written before the RowID field no longer decodes: %v", err)
	}
	if decoded.RowID != "" {
		t.Fatalf("decoded RowID = %q, want empty for a record that predates it", decoded.RowID)
	}
	if decoded.ID != "route_old" || decoded.Synopsis != "Legacy" ||
		len(decoded.Aliases) != 1 || decoded.Aliases[0] != "old" {
		t.Fatalf("old record decoded to %#v", decoded)
	}
}

// encodeNodeWithoutRowID reproduces the encoding exactly as it was before the
// RowID field existed, so the compatibility test has a real old record to read
// rather than a new one with the field left blank.
func encodeNodeWithoutRowID(t *testing.T, value router.Node) []byte {
	t.Helper()
	texts := []string{
		value.ID, value.DatabaseID, value.TableID, value.ParentID,
		value.Name, value.Path, string(value.Kind), value.Purpose,
	}
	encoded, err := encodeTexts(texts)
	if err != nil {
		t.Fatal(err)
	}
	encoded = binary.LittleEndian.AppendUint64(encoded, value.Revision)
	if value.Deleted {
		encoded = append(encoded, 1)
	} else {
		encoded = append(encoded, 0)
	}
	encoded = binary.LittleEndian.AppendUint32(encoded, uint32(len(value.Aliases)))
	for _, alias := range value.Aliases {
		encoded, err = appendText(encoded, alias)
		if err != nil {
			t.Fatal(err)
		}
	}
	encoded, err = appendText(encoded, value.Synopsis)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
