package reindex_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestQueueSurvivesRestartAndExpiredLeaseCanBeReclaimed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	databaseStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := reindex.New()
	now := time.Date(2026, 7, 30, 1, 2, 3, 0, time.UTC)
	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.EnqueueIn(ctx, tx, reindex.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: "row_note", Revision: 7,
	}, true, true, now); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}

	databaseStore, err = sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	tx, err = databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.ClaimIn(ctx, tx, "worker-a", now, time.Minute)
	if err != nil || first.Revision != 7 || first.Attempts != 1 {
		t.Fatalf("first claim = %#v, %v", first, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	tx, err = databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ValidateClaimIn(ctx, tx, first, now.Add(2*time.Minute)); err == nil {
		t.Fatal("expired lease remained valid")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	tx, err = databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ClaimIn(ctx, tx, "worker-b", now.Add(2*time.Minute), time.Minute)
	if err != nil || second.LeaseOwner != "worker-b" || second.Attempts != 2 ||
		second.LeaseToken == first.LeaseToken {
		t.Fatalf("reclaimed lease = %#v, %v", second, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
