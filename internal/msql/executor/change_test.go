package executor_test

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestShowChangesPinsSnapshotAndKeepsEntriesOutOfTimeline(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows := &changeReadRows{Rows: baseRows, changes: testChanges(t, 1, 3)}
	subject := executor.New(dictionary, rows)

	first, err := execute(ctx, subject, "SHOW CHANGES IN DATABASE work LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(first.Rows) != 1 || first.Page == nil || !first.Page.Truncated || first.Page.NextCursor == "" {
		t.Fatalf("first change Page = %#v, %v", first, err)
	}
	if _, leaked := first.Rows[0]["entries"]; leaked {
		t.Fatalf("timeline leaked entries: %#v", first.Rows[0])
	}
	_, err = execute(ctx, subject, "SHOW CHANGES CURSOR :cursor LIMIT 1", executor.Parameters{
		Named: map[string]any{"cursor": first.Page.NextCursor},
	}, executor.MutationOptions{})
	assertExecutorCode(t, err, result.CodeValidation)
	rows.changes = append(rows.changes, testChanges(t, 4, 4)...)
	second, err := execute(ctx, subject,
		"SHOW CHANGES IN DATABASE work CURSOR :cursor LIMIT 3",
		executor.Parameters{Named: map[string]any{"cursor": first.Page.NextCursor}}, executor.MutationOptions{},
	)
	if err != nil || len(second.Rows) != 2 || second.Page == nil || second.Page.Truncated {
		t.Fatalf("continued change Page = %#v, %v", second, err)
	}
	if got := second.Rows[1]["commit_sequence"]; got != uint64(3) {
		t.Fatalf("continued snapshot included wrong sequence: %v", got)
	}
	fresh, err := execute(ctx, subject, "SHOW CHANGES IN DATABASE work LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(fresh.Rows) != 4 {
		t.Fatalf("fresh change Page = %#v, %v", fresh, err)
	}
}

func TestShowChangePaginatesEntriesAndBindsCursor(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	changes := testChanges(t, 1, 2)
	rows := &changeReadRows{Rows: baseRows, changes: changes}
	subject := executor.New(dictionary, rows)

	first, err := execute(ctx, subject,
		"SHOW CHANGE :transaction IN DATABASE work LIMIT 2",
		executor.Parameters{Named: map[string]any{"transaction": changes[0].TransactionID}}, executor.MutationOptions{},
	)
	if err != nil || len(first.Rows) != 2 || first.Page == nil || !first.Page.Truncated {
		t.Fatalf("first entry Page = %#v, %v", first, err)
	}
	for _, value := range first.Rows {
		if _, leaked := value["values"]; leaked {
			t.Fatalf("entry Page leaked Row values: %#v", value)
		}
		if related, ok := value["related_object_ids"].([]string); !ok || related == nil {
			t.Fatalf("entry Page related IDs are not a non-null list: %#v", value)
		}
	}
	second, err := execute(ctx, subject,
		"SHOW CHANGE :transaction IN DATABASE work CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{
			"transaction": changes[0].TransactionID, "cursor": first.Page.NextCursor,
		}}, executor.MutationOptions{},
	)
	if err != nil || len(second.Rows) != 1 || second.Page == nil || second.Page.Truncated {
		t.Fatalf("second entry Page = %#v, %v", second, err)
	}
	tampered := first.Page.NextCursor[:len(first.Page.NextCursor)-1] + "A"
	_, err = execute(ctx, subject,
		"SHOW CHANGE :transaction IN DATABASE work CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{
			"transaction": changes[0].TransactionID, "cursor": tampered,
		}}, executor.MutationOptions{},
	)
	assertExecutorCode(t, err, result.CodeValidation)
	_, err = execute(ctx, subject,
		"SHOW CHANGE :transaction IN DATABASE work CURSOR :cursor LIMIT 2",
		executor.Parameters{Named: map[string]any{
			"transaction": changes[1].TransactionID, "cursor": first.Page.NextCursor,
		}}, executor.MutationOptions{},
	)
	assertExecutorCode(t, err, result.CodeValidation)
}

func TestShowChangesRejectsInvalidBoundsAndMutuallyExclusiveCursor(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows := &changeReadRows{Rows: baseRows, changes: testChanges(t, 1, 2)}
	subject := executor.New(dictionary, rows)
	first, err := execute(ctx, subject, "SHOW CHANGES IN DATABASE work LIMIT 1", executor.Parameters{}, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		source     string
		parameters executor.Parameters
	}{
		{"SHOW CHANGES IN DATABASE work AFTER COMMIT_SEQUENCE -1 LIMIT 1", executor.Parameters{}},
		{"SHOW CHANGES IN DATABASE work LIMIT 257", executor.Parameters{}},
		{
			"SHOW CHANGES IN DATABASE work AFTER COMMIT_SEQUENCE 1 CURSOR :cursor LIMIT 1",
			executor.Parameters{Named: map[string]any{"cursor": first.Page.NextCursor}},
		},
	} {
		_, err := execute(ctx, subject, test.source, test.parameters, executor.MutationOptions{})
		assertExecutorCode(t, err, result.CodeValidation)
	}
}

func TestScopedAuthorizationRequiresExplicitChangeDatabase(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	rows := &changeReadRows{Rows: baseRows, changes: testChanges(t, 1, 1)}
	subject := executor.New(dictionary, rows)
	scoped := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"db_database"},
	})
	_, err := execute(scoped, subject, "SHOW CHANGES LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	assertExecutorCode(t, err, result.CodePermissionDenied)
	if _, err := execute(scoped, subject, "SHOW CHANGES IN DATABASE work LIMIT 10", executor.Parameters{}, executor.MutationOptions{}); err != nil {
		t.Fatalf("scoped SHOW CHANGES error = %v", err)
	}
}

func TestScopedChangeTimelineRedactsOtherDatabaseIDs(t *testing.T) {
	ctx := context.Background()
	dictionary, baseRows, closeStore := changeDependencies(t, ctx)
	defer closeStore()
	value, err := change.NewEnvelope(1, time.Unix(1, 0).UTC(), change.Metadata{
		Actor: "agent:test", Source: "msql", Reason: "cross database transaction",
	}, []change.Entry{
		{
			ObjectKind: change.ObjectRow, DatabaseID: "db_database", TableID: "tbl_table",
			ObjectID: "row_work", Operation: change.OperationInsert, AfterRevision: 1,
			SchemaVersion: 1, HistoryLocator: "row_work@00000000000000000001",
		},
		{
			ObjectKind: change.ObjectRow, DatabaseID: "db_other", TableID: "tbl_other",
			ObjectID: "row_secret", Operation: change.OperationInsert, AfterRevision: 1,
			SchemaVersion: 1, HistoryLocator: "row_secret@00000000000000000001",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := &changeReadRows{Rows: baseRows, changes: []change.Envelope{value}}
	subject := executor.New(dictionary, rows)
	output, err := execute(ctx, subject, "SHOW CHANGES IN DATABASE work LIMIT 10", executor.Parameters{}, executor.MutationOptions{})
	if err != nil || len(output.Rows) != 1 {
		t.Fatalf("scoped timeline = %#v, %v", output, err)
	}
	if got := output.Rows[0]["database_ids"]; !reflect.DeepEqual(got, []string{"db_database"}) {
		t.Fatalf("scoped database_ids = %#v", got)
	}
	if got := output.Rows[0]["entry_count"]; got != uint64(1) {
		t.Fatalf("scoped entry_count = %#v", got)
	}
}

type changeReadRows struct {
	executor.Rows
	changes []change.Envelope
}

func (rows *changeReadRows) ListCommittedChanges(
	_ context.Context, databaseID string, after, snapshot uint64, limit int,
) ([]change.Envelope, uint64, bool, error) {
	if snapshot == 0 {
		snapshot = uint64(len(rows.changes))
	}
	values := make([]change.Envelope, 0, limit+1)
	for _, value := range rows.changes {
		if value.CommitSequence <= after || value.CommitSequence > snapshot ||
			(databaseID != "" && !containsString(value.DatabaseIDs, databaseID)) {
			continue
		}
		values = append(values, value)
		if len(values) > limit {
			return values[:limit], snapshot, true, nil
		}
	}
	return values, snapshot, false, nil
}

func (rows *changeReadRows) GetCommittedChange(
	_ context.Context, transactionID, databaseID string,
) (change.Envelope, error) {
	for _, value := range rows.changes {
		if value.TransactionID == transactionID &&
			(databaseID == "" || containsString(value.DatabaseIDs, databaseID)) {
			return value, nil
		}
	}
	return change.Envelope{}, fmt.Errorf("missing change")
}

func testChanges(t *testing.T, first, last uint64) []change.Envelope {
	t.Helper()
	values := make([]change.Envelope, 0, last-first+1)
	for sequence := first; sequence <= last; sequence++ {
		entries := make([]change.Entry, 0, 3)
		for number := 0; number < 3; number++ {
			entries = append(entries, change.Entry{
				ObjectKind: change.ObjectRow, DatabaseID: "db_database", TableID: "tbl_table",
				ObjectID: fmt.Sprintf("row_%d_%d", sequence, number), Operation: change.OperationInsert,
				AfterRevision: 1, SchemaVersion: 1,
				HistoryLocator: fmt.Sprintf("history_%d_%d", sequence, number),
			})
		}
		value, err := change.NewEnvelope(sequence, time.Unix(int64(sequence), 0).UTC(), change.Metadata{
			Actor: "agent:test", Source: "msql", Reason: "test",
		}, entries)
		if err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	return values
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func changeDependencies(
	t *testing.T, ctx context.Context,
) (executor.Catalog, executor.Rows, func()) {
	t.Helper()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(databaseStore, catalog.Options{
		IDs: &idSource{values: []string{"database", "table", "title"}},
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{
			Name: "title", Type: "TEXT(100)", Purpose: "Title", SemanticRole: "title",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	return dictionary, row.New(databaseStore, dictionary, row.Options{}), func() {
		if err := databaseStore.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
}
