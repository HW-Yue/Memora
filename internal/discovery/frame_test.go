package discovery_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/discovery"
)

func newBuilder(t *testing.T, limit, byteLimit uint64) *discovery.Builder {
	t.Helper()
	builder, err := discovery.NewBuilder("snapshot-1", "catalog-1", limit, byteLimit)
	if err != nil {
		t.Fatal(err)
	}
	return builder
}

func TestBuilderRejectsMixedSnapshotAndCatalogRevision(t *testing.T) {
	t.Parallel()
	builder := newBuilder(t, 8, 4096)
	if err := builder.Add(discovery.Batch{
		Snapshot: "snapshot-2", CatalogRevision: "catalog-1",
	}); !errors.Is(err, discovery.ErrSnapshotMismatch) {
		t.Fatalf("Add(other snapshot) = %v", err)
	}
	if err := builder.Add(discovery.Batch{
		Snapshot: "snapshot-1", CatalogRevision: "catalog-2",
	}); !errors.Is(err, discovery.ErrCatalogRevisionMismatch) {
		t.Fatalf("Add(other catalog revision) = %v", err)
	}
}

// TestFramePublishesPathOrderNotPredictorOrder pins the contract that ordering
// is the frame's, not the predictor's.
//
// Ranking still decides which hits survive the limit, but if the surviving
// order leaked out of the predictor, a caller would read meaning into it — the
// exposed score coming back wearing a different hat.
func TestFramePublishesPathOrderNotPredictorOrder(t *testing.T) {
	t.Parallel()
	builder := newBuilder(t, 8, 4096)
	if err := builder.Add(discovery.Batch{
		Snapshot: "snapshot-1", CatalogRevision: "catalog-1",
		Candidates: []discovery.Candidate{
			{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/zeta"},
			{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/alpha"},
			{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/mid"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	frame := builder.Frame()
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, candidate := range frame.Candidates {
		paths = append(paths, candidate.Path)
	}
	if strings.Join(paths, ",") != "/alpha,/mid,/zeta" {
		t.Fatalf("published order = %v, want lexicographic by path", paths)
	}
}

// TestBuilderDropsARepeatedLocation: two predictors finding the same node found
// one node, not two.
func TestBuilderDropsARepeatedLocation(t *testing.T) {
	t.Parallel()
	builder := newBuilder(t, 8, 4096)
	same := discovery.Candidate{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/shared"}
	for round := 0; round < 2; round++ {
		if err := builder.Add(discovery.Batch{
			Snapshot: "snapshot-1", CatalogRevision: "catalog-1",
			Candidates: []discovery.Candidate{same},
		}); err != nil {
			t.Fatal(err)
		}
	}
	if frame := builder.Frame(); len(frame.Candidates) != 1 {
		t.Fatalf("candidates = %#v, want the repeat collapsed", frame.Candidates)
	}
}

func TestFrameEnforcesBothBounds(t *testing.T) {
	t.Parallel()
	candidates := make([]discovery.Candidate, 0, 6)
	for _, path := range []string{"/a", "/b", "/c", "/d", "/e", "/f"} {
		candidates = append(candidates, discovery.Candidate{
			DatabaseID: "db_work", TableID: "tbl_notes", Path: path,
		})
	}

	byCount := newBuilder(t, 2, 65536)
	if err := byCount.Add(discovery.Batch{
		Snapshot: "snapshot-1", CatalogRevision: "catalog-1", Candidates: candidates,
	}); err != nil {
		t.Fatal(err)
	}
	frame := byCount.Frame()
	if len(frame.Candidates) != 2 || !frame.Truncated {
		t.Fatalf("count bound: %d candidates, truncated=%v", len(frame.Candidates), frame.Truncated)
	}

	// The byte bound is no longer reported in the frame, but the statement lets
	// the caller ask for it, so it still has to bite.
	byBytes := newBuilder(t, 64, 120)
	if err := byBytes.Add(discovery.Batch{
		Snapshot: "snapshot-1", CatalogRevision: "catalog-1", Candidates: candidates,
	}); err != nil {
		t.Fatal(err)
	}
	frame = byBytes.Frame()
	if len(frame.Candidates) == len(candidates) || !frame.Truncated {
		t.Fatalf("byte bound: %d candidates, truncated=%v", len(frame.Candidates), frame.Truncated)
	}
	if err := frame.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFrameRejectsInvalidLocations(t *testing.T) {
	t.Parallel()
	base := discovery.Frame{
		Version: discovery.Version, Usage: discovery.UsageNavigationOnly,
		Snapshot: "snapshot-1", CatalogRevision: "catalog-1", Limit: 8,
		Candidates: []discovery.Candidate{},
	}
	tests := map[string]func(*discovery.Frame){
		"no database": func(frame *discovery.Frame) {
			frame.Candidates = []discovery.Candidate{{TableID: "tbl_notes", Path: "/a"}}
		},
		"path without table": func(frame *discovery.Frame) {
			frame.Candidates = []discovery.Candidate{{DatabaseID: "db_work", Path: "/a"}}
		},
		"unsorted": func(frame *discovery.Frame) {
			frame.Candidates = []discovery.Candidate{
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/b"},
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/a"},
			}
		},
		"repeated": func(frame *discovery.Frame) {
			frame.Candidates = []discovery.Candidate{
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/a"},
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/a"},
			}
		},
		"over limit": func(frame *discovery.Frame) {
			frame.Limit = 1
			frame.Candidates = []discovery.Candidate{
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/a"},
				{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/b"},
			}
		},
		"zero limit":  func(frame *discovery.Frame) { frame.Limit = 0 },
		"old version": func(frame *discovery.Frame) { frame.Version = "memora.discovery-frame/v1" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			frame := base
			mutate(&frame)
			if err := frame.Validate(); !errors.Is(err, discovery.ErrInvalidFrame) {
				t.Fatalf("Validate(%s) = %v", name, err)
			}
		})
	}
}

func TestDiscoveryFrameWireRoundTrip(t *testing.T) {
	t.Parallel()
	builder := newBuilder(t, 8, 4096)
	if err := builder.Add(discovery.Batch{
		Snapshot: "snapshot-1", CatalogRevision: "catalog-1",
		Candidates: []discovery.Candidate{
			{DatabaseID: "db_work", TableID: "tbl_notes", Path: "/architecture/wal"},
			{DatabaseID: "db_work"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	frame := builder.Frame()
	encoded, err := json.Marshal(frame)
	if err != nil {
		t.Fatal(err)
	}
	// The removed fields must be gone from the wire, not merely unset in Go:
	// leaving them present and empty is what a caller would build on.
	for _, gone := range []string{
		"score", "score_kind", "reason", "matched_fields", "predictor", "budget", "route_id", "route_revision",
	} {
		if strings.Contains(string(encoded), `"`+gone+`"`) {
			t.Fatalf("frame still carries %q on the wire: %s", gone, encoded)
		}
	}
	var decoded discovery.Frame
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Version != discovery.Version || len(decoded.Candidates) != 2 ||
		decoded.Candidates[1].Path != "/architecture/wal" {
		t.Fatalf("round trip = %#v", decoded)
	}
}
