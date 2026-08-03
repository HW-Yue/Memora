package nativecatalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestNativeCatalogMSQLSurvivesReopenAndResolvesAliases(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(New(file), ServiceOptions{
		IDs:   &sequenceIDs{values: []string{"database", "table", "title"}},
		Clock: fixedClock{value: time.Date(2026, 7, 30, 13, 0, 0, 0, time.UTC)},
	})
	session := executor.NewBatchSession(ctx, service, nil)
	created := session.Execute(ctx, executor.BatchRequest{
		RequestID: "native-catalog-create",
		Source: "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; " +
			"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' " +
			"(title TEXT NOT NULL PURPOSE 'Title'); " +
			"ALTER TABLE work.notes RENAME TO knowledge",
	})
	assertSucceeded(t, created, 3)
	session.Close()
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service = NewService(New(reopened), ServiceOptions{})
	session = executor.NewBatchSession(ctx, service, nil)
	t.Cleanup(func() { session.Close() })
	discovered := session.Execute(ctx, executor.BatchRequest{
		RequestID: "native-catalog-discover",
		Source:    "SHOW DATABASES LIMIT 1; SHOW TABLES FROM work LIMIT 1; DESCRIBE TABLE work.notes",
	})
	assertSucceeded(t, discovered, 3)
	if got := discovered.Results[0].Rows[0]["name"]; got != "work" {
		t.Fatalf("SHOW DATABASES name = %#v", got)
	}
	if got := discovered.Results[1].Rows[0]["name"]; got != "knowledge" {
		t.Fatalf("SHOW TABLES name = %#v", got)
	}
	for index := 0; index < 2; index++ {
		page := discovered.Results[index].Page
		if page == nil || page.Version != result.ListPageVersion || page.Limit != 1 ||
			page.Snapshot == "" || page.Truncated || page.NextCursor != "" {
			t.Fatalf("Catalog page %d = %#v", index, page)
		}
	}
	if got := discovered.Results[2].Rows[0]["table_id"]; got != "tbl_table" {
		t.Fatalf("DESCRIBE TABLE through old alias table_id = %#v", got)
	}
}

func TestNativeCatalogMSQLRoundTripsFactAndRationaleRoles(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	service := NewService(New(file), ServiceOptions{
		IDs: &sequenceIDs{values: []string{"database", "table", "decision", "rationale"}},
	})
	session := executor.NewBatchSession(ctx, service, nil)
	defer session.Close()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "native-catalog-semantic-roles",
		Source: "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; " +
			"CREATE TABLE work.decisions PURPOSE 'Decisions' ROW SEMANTICS 'One decision' " +
			"(decision TEXT PURPOSE 'Accepted fact' ROLE fact, rationale TEXT PURPOSE 'Reason' ROLE rationale); " +
			"SHOW COLUMNS FROM work.decisions LIMIT 2",
	})
	assertSucceeded(t, envelope, 3)
	if len(envelope.Results[2].Rows) != 2 ||
		envelope.Results[2].Rows[0]["semantic_role"] != "fact" ||
		envelope.Results[2].Rows[1]["semantic_role"] != "rationale" {
		t.Fatalf("MSQL semantic roles = %#v", envelope.Results[2].Rows)
	}
}

func assertSucceeded(t *testing.T, envelope result.Envelope, count int) {
	t.Helper()
	if !envelope.OK || len(envelope.Results) != count {
		t.Fatalf("MSQL envelope = %#v", envelope)
	}
	for _, statement := range envelope.Results {
		if statement.Status != result.StatusSucceeded {
			t.Fatalf("MSQL statement = %#v", statement)
		}
	}
}
