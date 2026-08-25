package routelexical_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/routelexical"
	"github.com/HW-Yue/Memora/internal/router"
)

// TestMatchCarriesTheRouteSemanticPath is E1 stage 1's gate.
//
// Retrieval answers "where in the semantic tree is this", so the path is the
// answer. The search already read node.Path into its internal view and indexed
// it as a searchable field, then dropped it when assembling the Match — the
// data was in hand and simply not carried out.
//
// The path must be byte-identical to the Route's own, not recomputed: a second
// way of spelling a path is a second source of truth.
func TestMatchCarriesTheRouteSemanticPath(t *testing.T) {
	t.Parallel()
	source := semanticSource()
	paths := make(map[string]string, len(source.Routes))
	for _, node := range source.Routes {
		paths[node.ID] = node.Path
	}

	result, err := routelexical.Search(source, "recover write ahead log 数据库崩溃恢复")
	if err != nil {
		t.Fatal(err)
	}
	routeMatches := 0
	for _, match := range result.Matches {
		if match.RouteID == "" {
			// Database- and Table-level matches have no Route node, so no
			// Route path. They carry their identity instead.
			if match.Path != "" {
				t.Fatalf("non-Route match carries a Route path: %#v", match)
			}
			continue
		}
		routeMatches++
		want, exists := paths[match.RouteID]
		if !exists {
			t.Fatalf("match names an unknown Route: %#v", match)
		}
		if match.Path != want {
			t.Fatalf("match %s path = %q, want %q", match.RouteID, match.Path, want)
		}
		if match.Path == "" {
			t.Fatalf("match %s carries an empty path", match.RouteID)
		}
	}
	if routeMatches == 0 {
		t.Fatal("no Route matched, so the path assertion proved nothing")
	}
}

// TestDeletedRouteNeverContributesAPath keeps the narrowing from becoming a new
// way to read a deleted Route: a path is reachability, and a deleted node is
// unreachable.
func TestDeletedRouteNeverContributesAPath(t *testing.T) {
	t.Parallel()
	source := semanticSource()
	source.Routes = append(source.Routes, router.Node{
		Version: router.Version, ID: "route_gone", DatabaseID: "db_work", TableID: "tbl_notes",
		ParentID: "route_root", Name: "vanished recovery", Path: "/vanished", Kind: router.KindLeaf,
		Purpose: "vanished material", Revision: 1, Deleted: true,
	})
	result, err := routelexical.Search(source, "vanished")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range result.Matches {
		if match.Path == "/vanished" || match.RouteID == "route_gone" {
			t.Fatalf("deleted Route surfaced a path: %#v", match)
		}
	}
}
