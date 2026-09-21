package row_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
)

// These strings are written to disk as row_state and compared literally by the
// write path and the read filters, so they are a storage contract, not labels.
func TestStateStringsAreStorageValues(t *testing.T) {
	for state, want := range map[row.State]string{
		row.StateLive:       "live",
		row.StateDeleted:    "deleted",
		row.StateSuperseded: "superseded",
	} {
		if string(state) != want {
			t.Fatalf("state %q = %q, want %q", want, string(state), want)
		}
	}
}

// A Row carries its own leaf list: the write path knows it before the Row is
// encoded, so it is stored instead of looked up. ChangeSequence is the opposite
// case — attribution is recorded once per transaction in the Change Log, so the
// Row stores a key that must never cross an export boundary.
func TestRowJSONCarriesLeavesAndDropsChangeSequence(t *testing.T) {
	encoded, err := json.Marshal(row.Row{
		ID: "row_1", State: row.StateLive, ChangeSequence: 7, RouteLeafIDs: []string{"route_leaf"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"route_leaf_ids":["route_leaf"]`) {
		t.Fatalf("encoded row = %s", encoded)
	}
	if strings.Contains(string(encoded), "change_sequence") {
		t.Fatalf("ChangeSequence must not be encoded: %s", encoded)
	}
}

func TestRowJSONOmitsEmptyLeaves(t *testing.T) {
	encoded, err := json.Marshal(row.Row{ID: "row_1", State: row.StateLive})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "route_leaf_ids") {
		t.Fatalf("a Row without leaves must omit the key: %s", encoded)
	}
}
