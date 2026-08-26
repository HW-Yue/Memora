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
	// A leaf holds one Row, named on the leaf itself. row_missing is the orphan
	// mount: the leaf still names a Row that is not there.
	mountOnLeaf(source, "route_leaf_a", "row_current")
	mountOnLeaf(source, "route_leaf_c", "row_missing")

	service := semantichealth.New(source, database)
	first, err := service.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[semantichealth.Kind]bool{
		semantichealth.KindRouteCapacity:         false,
		semantichealth.KindAmbiguousSiblings:     false,
		semantichealth.KindInvalidRouteStructure: false,
		semantichealth.KindUnroutedRow:           false,
		semantichealth.KindOrphanMembership:      false,
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
	}
	mountOnLeaf(source, "route_leaf", "row_outside_page")
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

// TestOrphanMountIsFoundOnTheLeafField is E3 stage 6's gate.
//
// The three Membership problem kinds are gone because a leaf field cannot get
// into those states. orphan_membership is not one of them: a leaf can still
// name a Row that no longer exists, and the scan has to keep finding it — now
// by reading the leaf's own RowID rather than a Membership object.
func TestOrphanMountIsFoundOnTheLeafField(t *testing.T) {
	t.Parallel()

	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "orphan-health.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1}
	source := &fakeSource{
		databases: []catalog.Database{{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Private", Tables: []catalog.Table{table}}},
		rows: []row.Row{
			{ID: "row_present", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 4, State: row.StateLive},
		},
		nodes: []router.Node{
			{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Purpose: "Root", Revision: 1},
			{Version: router.Version, ID: "route_held", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "held", Purpose: "Held", Revision: 2, RowID: "row_present"},
			{Version: router.Version, ID: "route_orphan", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "orphan", Purpose: "Orphan", Revision: 2, RowID: "row_gone"},
		},
	}
	report, err := semantichealth.New(source, database).Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	orphans := []semantichealth.Issue{}
	for _, issue := range report.Issues {
		switch issue.Kind {
		case semantichealth.KindOrphanMembership:
			orphans = append(orphans, issue)
		case semantichealth.KindUnroutedRow:
			t.Fatalf("a Row held by a leaf must count as routed: %#v", issue)
		}
	}
	if len(orphans) != 1 || orphans[0].RowID != "row_gone" {
		t.Fatalf("orphan mount findings = %#v", orphans)
	}
}

// mountOnLeaf points one of the fake source's leaves at a Row.
func mountOnLeaf(source *fakeSource, leafID, rowID string) {
	for index := range source.nodes {
		if source.nodes[index].ID == leafID {
			source.nodes[index].RowID = rowID
			return
		}
	}
	panic("unknown leaf " + leafID)
}
