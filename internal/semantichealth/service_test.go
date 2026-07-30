package semantichealth_test

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
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
			{ID: "row_a", State: row.StateLive, Values: map[string]any{"body": "same"}, UpdatedAt: old.Add(45 * 24 * time.Hour)},
			{ID: "row_b", State: row.StateLive, Values: map[string]any{"body": "same"}, UpdatedAt: old.Add(45 * 24 * time.Hour)},
		},
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
	if first.Status != "attention" || first.IssueCount != 3 || first.AutoFixCount != 0 || first.Hash == "" {
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
}

func (source *fakeSource) ShowDatabases(context.Context) ([]catalog.Database, error) {
	return append([]catalog.Database{}, source.databases...), nil
}

func (source *fakeSource) ListPage(context.Context, string, string, int) ([]row.Row, bool, error) {
	return append([]row.Row{}, source.rows...), false, nil
}
