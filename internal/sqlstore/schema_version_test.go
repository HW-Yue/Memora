package sqlstore_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// userVersion reads what the file says about itself, through its own handle so
// the answer does not depend on how the store opened it.
func userVersion(t *testing.T, path string) int {
	t.Helper()
	handle, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	version := 0
	if err := handle.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	return version
}

func setUserVersion(t *testing.T, path string, version int) {
	t.Helper()
	handle, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = handle.Close() }()
	if _, err := handle.Exec(`PRAGMA user_version = ` + itoa(version)); err != nil {
		t.Fatal(err)
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := ""
	for value > 0 {
		digits = string(rune('0'+value%10)) + digits
		value /= 10
	}
	return digits
}

// A newer Instance is not ours to migrate backwards: an old binary opening a new
// file does not fail, it rewrites — which is why the refusal has to happen before
// anything is created or altered.
func TestOpeningAnInstanceWrittenByANewerBinaryIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memora.db")
	db, err := sqlstore.Open(path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	future := userVersion(t, path) + 7
	setUserVersion(t, path, future)

	_, err = sqlstore.Open(path, sqlstore.Options{})
	if err == nil {
		t.Fatal("opening a newer instance must be refused")
	}
	if !strings.Contains(err.Error(), "newer Memora") {
		t.Fatalf("the refusal must say why: %v", err)
	}
	// And it must not have quietly reset the marker to its own version.
	if got := userVersion(t, path); got != future {
		t.Fatalf("a refused open changed the file schema version: %d", got)
	}
}

// The ordinary upgrade: a file from before the marker exists is migrated and
// stamped, so the next binary can tell what it is looking at.
func TestOpeningAnInstanceWithoutAMarkerMigratesAndStampsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memora.db")
	db, err := sqlstore.Open(path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	// An Instance created before the guard carries no marker.
	setUserVersion(t, path, 0)

	upgraded, err := sqlstore.Open(path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = upgraded.Close() }()
	if version := userVersion(t, path); version == 0 {
		t.Fatal("opening an older instance must stamp the file schema version")
	}
	// Opening it again is then the uninteresting case.
	again, err := sqlstore.Open(path, sqlstore.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
}
