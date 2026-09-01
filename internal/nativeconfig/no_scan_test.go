package nativeconfig

import (
	"path/filepath"
	"testing"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestReadingConfigurationHistoryNeverSweepsTheFile pins the property both
// configuration histories now have: the cost is how many revisions this key has,
// not how many Configuration records the Database holds.
//
// Listing every Configuration record and keeping the ones whose ID started with
// the key grew with every other kind of configuration ever written, and with
// every revision of each — for a history that is usually one entry long.
func TestReadingConfigurationHistoryNeverSweepsTheFile(t *testing.T) {
	file, err := nativestore.Create(
		filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	budgets, err := service.Current()
	if err != nil {
		t.Fatal(err)
	}
	next := budgets.Budgets
	next.SelectRows++
	if _, err := service.Update(next, budgets.Revision, "agent:test", "widen"); err != nil {
		t.Fatal(err)
	}

	before := file.Enumerations()
	history, err := service.History()
	if err != nil {
		t.Fatal(err)
	}
	policies, err := service.PolicyHistory()
	if err != nil {
		t.Fatal(err)
	}
	if swept := file.Enumerations() - before; swept != 0 {
		t.Fatalf("reading configuration history swept the file %d times", swept)
	}
	// The answers are the ones the sweep gave: a dense chain from revision 1.
	if len(history) != int(budgets.Revision)+1 {
		t.Fatalf("query budget history = %d revisions, want %d", len(history), budgets.Revision+1)
	}
	for index, value := range history {
		if value.Revision != uint64(index+1) {
			t.Fatalf("history[%d].Revision = %d", index, value.Revision)
		}
	}
	for index, value := range policies {
		if value.Revision != uint64(index+1) {
			t.Fatalf("policies[%d].Revision = %d", index, value.Revision)
		}
	}
}
