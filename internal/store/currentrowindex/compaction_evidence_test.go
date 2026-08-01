package currentrowindex

import (
	"fmt"
	"math"
	"testing"

	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

// TestCompactionEvidenceGate freezes the F151 synthetic churn workload. The
// current authority uses in-place page updates, so equal-width revisions must
// not grow the page file by more than 25% after the baseline tree is built.
func TestCompactionEvidenceGate(t *testing.T) {
	_, manager, runtime, index := newTestIndex(t)
	const rowCount = 256
	locators := make([]Locator, rowCount)
	updates := make([]Update, rowCount)
	for number := range locators {
		locators[number] = locator(fmt.Sprintf("row_%04d", number), 1, 1, row.StateLive)
		updates[number] = Update{Locator: locators[number]}
	}
	if _, err := index.Apply(1, updates); err != nil {
		t.Fatal(err)
	}
	flushAll(t, runtime)
	baseline, err := manager.PageCount()
	if err != nil || baseline == 0 {
		t.Fatalf("baseline PageCount() = %d, %v", baseline, err)
	}

	for revision := uint64(2); revision <= 17; revision++ {
		for number := range locators {
			locators[number].Revision = revision
			locators[number].CommitSequence = revision
			updates[number] = Update{ExpectedRevision: revision - 1, Locator: locators[number]}
		}
		if _, err := index.Apply(revision, updates); err != nil {
			t.Fatalf("revision %d: %v", revision, err)
		}
	}
	flushAll(t, runtime)
	final, err := manager.PageCount()
	if err != nil {
		t.Fatal(err)
	}
	if final*100 > baseline*125 {
		t.Fatalf("space amplification gate crossed: baseline=%d final=%d threshold=1.25x", baseline, final)
	}
	t.Logf("space amplification %.2fx (%d/%d pages)", float64(final)/float64(baseline), final, baseline)
}

func flushAll(t *testing.T, runtime *treecommit.Runtime) {
	t.Helper()
	report, err := runtime.FlushDirty(math.MaxUint64)
	if err != nil || report.Remaining != 0 {
		t.Fatalf("FlushDirty() = %+v, %v", report, err)
	}
}
