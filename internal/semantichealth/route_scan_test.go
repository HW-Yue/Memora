package semantichealth_test

import (
	"context"
	"encoding/json"
	"math/rand"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRouteHealthFindsOnlyDeterministicStructuralDebtAndIsOrderIndependent(t *testing.T) {
	t.Parallel()

	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "route-health.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1}
	source := &fakeSource{
		databases: []catalog.Database{{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Private", Tables: []catalog.Table{table}}},
		rows: []row.Row{
			{ID: "row_current", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 3, State: row.StateLive, Values: map[string]any{"body": "do-not-leak-secret"}},
			{ID: "row_unrouted", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 1, State: row.StateLive, Values: map[string]any{"body": "private"}},
		},
		locators: map[string][]router.Locator{},
	}
	source.nodes = append(source.nodes, router.Node{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Purpose: "Root", Revision: 1})
	source.nodes = append(source.nodes, router.Node{Version: router.Version, ID: "route_branch", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindBranch, Name: "branch", Purpose: "Branch", Revision: 1})
	for index := 0; index < 12; index++ {
		name := "leaf-" + string(rune('a'+index))
		aliases := []string{name}
		if index < 2 {
			aliases = append(aliases, "shared-topic")
		}
		id := "route_leaf_" + string(rune('a'+index))
		source.nodes = append(source.nodes, router.Node{Version: router.Version, ID: id,
			DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_branch",
			Kind: router.KindLeaf, Name: name, Aliases: aliases, Purpose: "Leaf", Revision: 1})
	}
	source.nodes = append(source.nodes, router.Node{Version: router.Version, ID: "route_broken",
		DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_missing",
		Kind: router.KindLeaf, Name: "broken", Purpose: "Broken", Revision: 1})
	source.locators["route_leaf_a"] = []router.Locator{
		{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_current", Revision: 2},
		{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_missing", Revision: 1},
	}
	source.locators["route_leaf_b"] = []router.Locator{{DatabaseID: "db_other", TableID: "tbl_other", RowID: "row_current", Revision: 3}}
	source.locators["route_leaf_c"] = []router.Locator{{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_missing", Revision: 1}}

	service := semantichealth.New(source, database)
	first, err := service.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[semantichealth.Kind]bool{
		semantichealth.KindRouteCapacity: false, semantichealth.KindMultiRowLeaf: false,
		semantichealth.KindAmbiguousSiblings:     false,
		semantichealth.KindInvalidRouteStructure: false, semantichealth.KindUnroutedRow: false,
		semantichealth.KindOrphanMembership: false, semantichealth.KindStaleMembership: false,
		semantichealth.KindInvalidMembershipScope: false,
	}
	for _, issue := range first.Issues {
		if _, ok := wantKinds[issue.Kind]; ok {
			wantKinds[issue.Kind] = true
		}
		if issue.AutoFix || issue.Severity != semantichealth.SeverityReviewRequired {
			t.Errorf("semantic issue became auto-fix: %#v", issue)
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("health report omitted %s: %#v", kind, first.Issues)
		}
	}
	encoded, _ := json.Marshal(first)
	if strings.Contains(string(encoded), "do-not-leak-secret") {
		t.Fatal("health report leaked Row values")
	}
	rand.New(rand.NewSource(7)).Shuffle(len(source.nodes), func(left, right int) {
		source.nodes[left], source.nodes[right] = source.nodes[right], source.nodes[left]
	})
	second, err := service.Report(context.Background())
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("shuffled report drift = %v\n%#v\n%#v", err, first, second)
	}
}

func TestTruncatedRowsSuppressAbsenceBasedMembershipFindings(t *testing.T) {
	t.Parallel()

	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "truncated-health.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	source := &fakeSource{
		databases: []catalog.Database{{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Private", Tables: []catalog.Table{{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note"}}}},
		rows:      []row.Row{{ID: "row_visible", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 1, State: row.StateLive}}, more: true,
		nodes: []router.Node{
			{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Purpose: "Root", Revision: 1},
			{Version: router.Version, ID: "route_leaf", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "leaf", Purpose: "Leaf", Revision: 1},
		},
		locators: map[string][]router.Locator{"route_leaf": {{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_outside_page", Revision: 1}}},
	}
	report, err := semantichealth.New(source, database).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.Truncated {
		t.Fatal("truncated Row page was not reported")
	}
	for _, issue := range report.Issues {
		if issue.Kind == semantichealth.KindOrphanMembership || issue.Kind == semantichealth.KindUnroutedRow {
			t.Fatalf("truncated scan emitted absence-based issue: %#v", issue)
		}
	}
}
