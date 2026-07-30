package semantichealth_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestDeterministicHealthReportFindsAllMaintenanceCandidatesWithoutRowBodies(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	source := healthFixture()
	service := semantichealth.New(source, database)

	first, err := service.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Report(ctx)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("reports differ: %#v %#v, %v", first, second, err)
	}
	if first.Version != semantichealth.ReportVersion || first.Status != "attention" ||
		first.Hash == "" || first.IssueCount != 7 || first.AutoFixCount != 1 || first.Truncated {
		t.Fatalf("health report = %#v", first)
	}
	wantKinds := []semantichealth.Kind{
		semantichealth.KindDuplicateRow, semantichealth.KindPendingReindex,
		semantichealth.KindPendingReindex, semantichealth.KindRouterChildren,
		semantichealth.KindRouterLeaf, semantichealth.KindStaleDescription,
		semantichealth.KindSynonymousColumns,
	}
	gotKinds := make([]semantichealth.Kind, len(first.Issues))
	for index, issue := range first.Issues {
		gotKinds[index] = issue.Kind
		if issue.Kind != semantichealth.KindPendingReindex && issue.AutoFix {
			t.Fatalf("high-risk issue is auto-fixable: %#v", issue)
		}
	}
	if !reflect.DeepEqual(gotKinds, wantKinds) {
		t.Fatalf("issue kinds = %#v, want %#v", gotKinds, wantKinds)
	}
	encoded, _ := json.Marshal(first)
	for _, forbidden := range []string{"same semantic body", "private source text"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("health report leaks Row body %q: %s", forbidden, encoded)
		}
	}
}

func TestMaintenanceRetriesOnlyExplicitFailedReindexAndReplaysReceipt(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "maintenance.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	source := healthFixture()
	service := semantichealth.New(source, database)
	report, err := service.Report(ctx)
	if err != nil {
		t.Fatal(err)
	}
	failed := issueBy(t, report, semantichealth.KindPendingReindex, true)
	request := semantichealth.Request{
		Version: semantichealth.RequestVersion, RequestID: "maintain-1",
		Trigger: semantichealth.TriggerCheckpoint, Actor: "agent:host",
		ExpectedReportHash: report.Hash, IssueIDs: []string{failed.ID},
	}
	receipt, err := service.Maintain(ctx, request)
	if err != nil || receipt.Version != semantichealth.ReceiptVersion ||
		receipt.Status != "completed" || len(receipt.Actions) != 1 || len(source.retried) != 1 {
		t.Fatalf("maintenance receipt = %#v, retried=%#v, err=%v", receipt, source.retried, err)
	}
	replayed, err := service.Maintain(ctx, request)
	if err != nil || !replayed.Replayed || len(source.retried) != 1 {
		t.Fatalf("replayed receipt = %#v, retried=%#v, err=%v", replayed, source.retried, err)
	}

	highRisk := issueBy(t, report, semantichealth.KindDuplicateRow, false)
	request.RequestID = "maintain-high-risk"
	request.IssueIDs = []string{highRisk.ID}
	_, err = service.Maintain(ctx, request)
	assertCode(t, err, result.CodePermissionDenied)
	if len(source.retried) != 1 {
		t.Fatalf("high-risk request mutated tasks: %#v", source.retried)
	}

	request.RequestID = "maintain-stale"
	request.IssueIDs = []string{failed.ID}
	request.ExpectedReportHash = "sha256:" + strings.Repeat("0", 64)
	_, err = service.Maintain(ctx, request)
	assertCode(t, err, result.CodeRevisionConflict)
}

type fakeSource struct {
	databases []catalog.Database
	rows      map[string][]row.Row
	tasks     []reindex.Task
	children  map[string][]router.Node
	leaves    map[string][]router.Locator
	retried   []string
}

func healthFixture() *fakeSource {
	old := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := old.Add(45 * 24 * time.Hour)
	columns := []catalog.Column{
		{ID: "col_summary", Name: "summary", Type: "TEXT", Purpose: "Canonical summary", SchemaVersion: 1},
		{ID: "col_abstract", Name: "abstract", Type: "TEXT", Purpose: " canonical   summary ", SchemaVersion: 1},
	}
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1, UpdatedAt: old, Columns: columns}
	database := catalog.Database{ID: "db_work", Name: "work", Purpose: "Work", Scope: "Projects", SchemaVersion: 1, UpdatedAt: old, Tables: []catalog.Table{table}}
	children := make([]router.Node, 12)
	for index := range children {
		children[index] = router.Node{Version: router.Version, ID: "leaf-" + string(rune('a'+index)), DatabaseID: database.ID, ParentID: "root", Kind: router.KindLeaf, Path: "/leaf", Revision: 1}
	}
	locators := make([]router.Locator, 100)
	for index := range locators {
		locators[index] = router.Locator{DatabaseID: database.ID, TableID: table.ID, RowID: "row-capacity-" + string(rune(index+1)), Revision: 1}
	}
	return &fakeSource{
		databases: []catalog.Database{database},
		rows: map[string][]row.Row{"work.notes": {
			{ID: "row_b", DatabaseID: database.ID, TableID: table.ID, Revision: 1, State: row.StateLive, Values: map[string]any{"body": "same semantic body", "secret": "private source text"}, UpdatedAt: newer},
			{ID: "row_a", DatabaseID: database.ID, TableID: table.ID, Revision: 1, State: row.StateLive, Values: map[string]any{"secret": "private source text", "body": "same semantic body"}, UpdatedAt: newer},
		}},
		tasks: []reindex.Task{
			{Version: reindex.Version, DatabaseID: database.ID, TableID: table.ID, RowID: "row_failed", Revision: 2, State: reindex.StateFailed},
			{Version: reindex.Version, DatabaseID: database.ID, TableID: table.ID, RowID: "row_pending", Revision: 3, State: reindex.StatePending},
		},
		children: map[string][]router.Node{"root": children}, leaves: map[string][]router.Locator{children[0].ID: locators},
	}
}

func (source *fakeSource) ShowDatabases(context.Context) ([]catalog.Database, error) {
	return append([]catalog.Database{}, source.databases...), nil
}
func (source *fakeSource) ListPage(_ context.Context, database, table string, _ int) ([]row.Row, bool, error) {
	return append([]row.Row{}, source.rows[database+"."+table]...), false, nil
}
func (source *fakeSource) ResolveRouterPath(context.Context, string, string) (router.Node, error) {
	return router.Node{Version: router.Version, ID: "root", DatabaseID: "db_work", Kind: router.KindRoot, Path: "/", Revision: 1}, nil
}
func (source *fakeSource) ListRouterChildren(_ context.Context, parent, _ string, _ int) ([]router.Node, string, error) {
	return append([]router.Node{}, source.children[parent]...), "", nil
}
func (source *fakeSource) ListRouterLeaf(_ context.Context, leaf string, _ int) ([]router.Locator, bool, error) {
	return append([]router.Locator{}, source.leaves[leaf]...), false, nil
}
func (source *fakeSource) ListReindexTasks(context.Context) ([]reindex.Task, error) {
	return append([]reindex.Task{}, source.tasks...), nil
}
func (source *fakeSource) RetryReindex(_ context.Context, databaseID, tableID, rowID string) error {
	source.retried = append(source.retried, databaseID+"/"+tableID+"/"+rowID)
	return nil
}

func issueBy(t *testing.T, report semantichealth.Report, kind semantichealth.Kind, autoFix bool) semantichealth.Issue {
	t.Helper()
	for _, issue := range report.Issues {
		if issue.Kind == kind && issue.AutoFix == autoFix {
			return issue
		}
	}
	t.Fatalf("issue %q auto_fix=%t not found in %#v", kind, autoFix, report.Issues)
	return semantichealth.Issue{}
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	stable, ok := err.(interface{ StableCode() string })
	if !ok || result.Code(stable.StableCode()) != want {
		t.Fatalf("error = %v, want code %q", err, want)
	}
}
