package catalogindex

import (
	"fmt"
	"math"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

// TestFreePageReuseEvidenceGate freezes F152 at a bounded 10% catalog churn:
// remove 32 of 320 databases, add 32 replacements, and compare physical Page
// count with the equal-cardinality baseline. More than 20% excess Pages would
// justify adding a durable free-list/reuse protocol.
func TestFreePageReuseEvidenceGate(t *testing.T) {
	_, manager, runtime, index := newTestIndex(t)
	baselineCatalog := evidenceCatalog(0, 320)
	if _, err := index.Replace(1, baselineCatalog); err != nil {
		t.Fatal(err)
	}
	flushCatalogEvidence(t, runtime)
	baseline, err := manager.PageCount()
	if err != nil || baseline == 0 {
		t.Fatalf("baseline PageCount() = %d, %v", baseline, err)
	}

	churned := append([]catalog.Database(nil), baselineCatalog[:288]...)
	churned = append(churned, evidenceCatalog(320, 32)...)
	if _, err := index.Replace(2, churned); err != nil {
		t.Fatal(err)
	}
	flushCatalogEvidence(t, runtime)
	final, err := manager.PageCount()
	if err != nil {
		t.Fatal(err)
	}
	if final*100 > baseline*120 {
		t.Fatalf("free Page waste gate crossed: baseline=%d final=%d threshold=20%%", baseline, final)
	}
	t.Logf("allocated Page overhead %.2f%% (%d baseline, %d final)",
		(float64(final)/float64(baseline)-1)*100, baseline, final)
}

func evidenceCatalog(start, count int) []catalog.Database {
	result := make([]catalog.Database, 0, count)
	for number := start; number < start+count; number++ {
		result = append(result, catalog.Database{
			ID: fmt.Sprintf("db_%04d", number), Name: fmt.Sprintf("Database %04d", number),
			Aliases: []string{fmt.Sprintf("数据库 %04d", number)}, SchemaVersion: 1,
		})
	}
	return result
}

func flushCatalogEvidence(t *testing.T, runtime *treecommit.Runtime) {
	t.Helper()
	report, err := runtime.FlushDirty(math.MaxUint64)
	if err != nil || report.Remaining != 0 {
		t.Fatalf("FlushDirty() = %+v, %v", report, err)
	}
}
