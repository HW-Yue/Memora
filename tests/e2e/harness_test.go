//go:build e2e

package e2e_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestE2EHarnessUsesDeterministicIDs(t *testing.T) {
	t.Parallel()

	ids := testkit.NewFakeIDs("db_test", "table_test", "row_test")
	for _, want := range []string{"db_test", "table_test", "row_test"} {
		got, err := ids.Next()
		if err != nil {
			t.Fatalf("Next() error = %v", err)
		}
		if got != want {
			t.Fatalf("Next() = %q, want %q", got, want)
		}
	}
}
