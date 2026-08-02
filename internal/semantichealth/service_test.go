package semantichealth_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestHealthReportIsDeterministicAndNeverAutoMutatesSemanticIssues(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "health.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1, UpdatedAt: old,
		Columns: []catalog.Column{
			{ID: "col_summary", Name: "summary", Type: "TEXT", Purpose: "Canonical summary", SchemaVersion: 1},
			{ID: "col_abstract", Name: "abstract", Type: "TEXT", Purpose: " canonical summary ", SchemaVersion: 1},
		}}
	source := &fakeSource{
		databases: []catalog.Database{{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Projects", SchemaVersion: 1, Tables: []catalog.Table{table}}},
		rows: []row.Row{
			{ID: "row_a", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 1, State: row.StateLive, Values: map[string]any{"body": "same"}, UpdatedAt: old.Add(45 * 24 * time.Hour)},
			{ID: "row_b", DatabaseID: "db_work", TableID: "tbl_notes", Revision: 1, State: row.StateLive, Values: map[string]any{"body": "same"}, UpdatedAt: old.Add(45 * 24 * time.Hour)},
		},
		nodes: []router.Node{
			{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes", Kind: router.KindRoot, Name: "root", Purpose: "Notes", Revision: 1},
			{Version: router.Version, ID: "route_leaf", DatabaseID: "db_work", TableID: "tbl_notes", ParentID: "route_root", Kind: router.KindLeaf, Name: "notes", Purpose: "Notes", Revision: 1},
		},
		locators: map[string][]router.Locator{"route_leaf": {
			{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_a", Revision: 1},
			{DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_b", Revision: 1},
		}},
	}
	service := semantichealth.New(source, database)
	first, err := service.Report(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Report(context.Background())
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("reports differ: %#v %#v, %v", first, second, err)
	}
	if first.Status != "attention" || first.IssueCount != 4 || first.AutoFixCount != 0 || first.Hash == "" {
		t.Fatalf("report = %#v", first)
	}
	for _, issue := range first.Issues {
		if issue.AutoFix {
			t.Fatalf("semantic issue became an automatic mutation: %#v", issue)
		}
	}
}

type fakeSource struct {
	databases []catalog.Database
	rows      []row.Row
	more      bool
	nodes     []router.Node
	locators  map[string][]router.Locator
}

func (source *fakeSource) ShowDatabases(context.Context) ([]catalog.Database, error) {
	return append([]catalog.Database{}, source.databases...), nil
}

func (source *fakeSource) ListPage(context.Context, string, string, int) ([]row.Row, bool, error) {
	return append([]row.Row{}, source.rows...), source.more, nil
}

func (source *fakeSource) ListRouterNodes(context.Context) ([]router.Node, error) {
	return append([]router.Node{}, source.nodes...), nil
}

func (source *fakeSource) ListRouterLeafPage(
	_ context.Context, leafID, cursor string, _ int,
) ([]router.Locator, router.ReadPage, error) {
	if cursor != "" {
		return []router.Locator{}, router.ReadPage{}, nil
	}
	return append([]router.Locator{}, source.locators[leafID]...), router.ReadPage{}, nil
}
