package storetest

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/store"
)

type OpenFunc func(path string) (store.Store, error)

func Run(t *testing.T, open OpenFunc) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "store.db")

	t.Run("commit and reopen unicode", func(t *testing.T) {
		database := mustOpen(t, open, path)
		put(t, database, "知识", "项目/记忆", []byte("Memora 支持中文 🧠"))
		mustClose(t, database)

		database = mustOpen(t, open, path)
		defer mustClose(t, database)
		if got, want := get(t, database, "知识", "项目/记忆"), "Memora 支持中文 🧠"; got != want {
			t.Fatalf("Get() = %q, want %q", got, want)
		}
	})

	t.Run("rollback hides writes", func(t *testing.T) {
		database := mustOpen(t, open, filepath.Join(t.TempDir(), "rollback.db"))
		defer mustClose(t, database)
		tx := mustBegin(t, database, store.ReadWrite)
		if err := tx.Put(context.Background(), "rows", "row_1", []byte("draft")); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("Rollback() error = %v", err)
		}
		read := mustBegin(t, database, store.ReadOnly)
		defer rollback(t, read)
		if _, err := read.Get(context.Background(), "rows", "row_1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get() after rollback error = %v, want %v", err, store.ErrNotFound)
		}
	})

	t.Run("read only rejects mutation", func(t *testing.T) {
		database := mustOpen(t, open, filepath.Join(t.TempDir(), "readonly.db"))
		defer mustClose(t, database)
		tx := mustBegin(t, database, store.ReadOnly)
		defer rollback(t, tx)
		if err := tx.Put(context.Background(), "rows", "row_1", []byte("value")); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("Put() error = %v, want %v", err, store.ErrReadOnly)
		}
		if err := tx.Delete(context.Background(), "rows", "row_1"); !errors.Is(err, store.ErrReadOnly) {
			t.Fatalf("Delete() error = %v, want %v", err, store.ErrReadOnly)
		}
	})

	t.Run("delete commits", func(t *testing.T) {
		database := mustOpen(t, open, filepath.Join(t.TempDir(), "delete.db"))
		defer mustClose(t, database)
		put(t, database, "rows", "row_1", []byte("value"))
		tx := mustBegin(t, database, store.ReadWrite)
		if err := tx.Delete(context.Background(), "rows", "row_1"); err != nil {
			t.Fatalf("Delete() error = %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit() error = %v", err)
		}
		read := mustBegin(t, database, store.ReadOnly)
		defer rollback(t, read)
		if _, err := read.Get(context.Background(), "rows", "row_1"); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("Get() after delete error = %v, want %v", err, store.ErrNotFound)
		}
	})

	t.Run("concurrent readers", func(t *testing.T) {
		database := mustOpen(t, open, filepath.Join(t.TempDir(), "concurrent.db"))
		defer mustClose(t, database)
		put(t, database, "rows", "shared", []byte("visible"))

		const readers = 8
		var wait sync.WaitGroup
		errorsFound := make(chan error, readers)
		for index := 0; index < readers; index++ {
			wait.Add(1)
			go func() {
				defer wait.Done()
				tx, err := database.Begin(context.Background(), store.ReadOnly)
				if err != nil {
					errorsFound <- err
					return
				}
				defer func() { _ = tx.Rollback() }()
				value, err := tx.Get(context.Background(), "rows", "shared")
				if err != nil {
					errorsFound <- err
					return
				}
				if string(value) != "visible" {
					errorsFound <- fmt.Errorf("value = %q", value)
				}
			}()
		}
		wait.Wait()
		close(errorsFound)
		for err := range errorsFound {
			t.Errorf("concurrent reader: %v", err)
		}
	})
}

func mustOpen(t *testing.T, open OpenFunc, path string) store.Store {
	t.Helper()
	database, err := open(path)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return database
}

func mustBegin(t *testing.T, database store.Store, mode store.Mode) store.Tx {
	t.Helper()
	tx, err := database.Begin(context.Background(), mode)
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	return tx
}

func put(t *testing.T, database store.Store, bucket, key string, value []byte) {
	t.Helper()
	tx := mustBegin(t, database, store.ReadWrite)
	if err := tx.Put(context.Background(), bucket, key, value); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
}

func get(t *testing.T, database store.Store, bucket, key string) string {
	t.Helper()
	tx := mustBegin(t, database, store.ReadOnly)
	defer rollback(t, tx)
	value, err := tx.Get(context.Background(), bucket, key)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	return string(value)
}

func rollback(t *testing.T, tx store.Tx) {
	t.Helper()
	if err := tx.Rollback(); err != nil && !errors.Is(err, store.ErrTxClosed) {
		t.Errorf("Rollback() error = %v", err)
	}
}

func mustClose(t *testing.T, database store.Store) {
	t.Helper()
	if err := database.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}
