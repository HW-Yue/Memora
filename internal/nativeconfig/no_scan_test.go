package nativeconfig

import (
	"path/filepath"
	"testing"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// TestReadingConfigurationHistoryCostsOnePassWhateverItsDepth pins what is left
// to pin after E8 stage 3, and it is a different statement from the one this
// test used to make.
//
// It used to assert zero passes. That was true because the record log carried a
// process-resident map of where every record lived, so a point read was a map
// lookup — and that map is the thing stage 3 deletes, because it grew with how
// many times the Database had ever been written to rather than with how much it
// held. With it gone there is no index over the log at all, and a read of the
// log is a pass over the log. Asserting zero would now only be satisfiable by
// putting the map back.
//
// What still has to hold, and is the property that actually matters, is that
// the cost does not grow: one pass to read a history, whether that history is
// one revision deep or twenty. The chained point reads this replaced were one
// pass per revision.
func TestReadingConfigurationHistoryCostsOnePassWhateverItsDepth(t *testing.T) {
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
	shallow := file.Enumerations() - before
	// One pass per history — two histories are read here — and no more.
	if shallow != 2 {
		t.Fatalf("reading two configuration histories took %d passes, want 2", shallow)
	}
	for index := 0; index < 18; index++ {
		latest, currentErr := service.Current()
		if currentErr != nil {
			t.Fatal(currentErr)
		}
		widened := latest.Budgets
		widened.SelectRows++
		if _, err := service.Update(widened, latest.Revision, "agent:test", "widen"); err != nil {
			t.Fatal(err)
		}
	}
	deepBefore := file.Enumerations()
	if _, err := service.History(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.PolicyHistory(); err != nil {
		t.Fatal(err)
	}
	if deep := file.Enumerations() - deepBefore; deep != shallow {
		t.Fatalf("a 20-revision history took %d passes vs %d for a short one", deep, shallow)
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
