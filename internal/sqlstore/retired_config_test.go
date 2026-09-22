package sqlstore_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// A revision stored while `route_children` was still a budget keeps the key in
// its body. The engine answers with the four budgets it has — and says what it
// ignored, because a host that raised that number deserves to hear it stopped
// doing anything. Dropping it silently is the one option that leaves someone
// believing a setting is still in force.
func TestAStoredPageBudgetIsReportedAsRetired(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact' (title TEXT NOT NULL PURPOSE 'Title' ROLE title)`, nil, executor.MutationOptions{})

	// The stored shape from before route paging was removed: five budgets.
	body, err := json.Marshal(map[string]any{
		"version": "memora.database-configuration/v1", "key": "query_budgets", "revision": 1,
		"budgets": map[string]any{
			"route_children": 40, "open_locators": 1, "select_scan": 1000,
			"select_rows": 10, "route_frame_nodes": 12,
		},
		"actor": "agent:test", "reason": "from before route paging was removed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.db.SQL().Exec(
		`INSERT INTO mem_config(key, revision, body) VALUES('query_budgets', 1, ?)`, string(body)); err != nil {
		t.Fatal(err)
	}

	statement := h.run(`SHOW CONFIGURATION`, nil, executor.MutationOptions{})
	for _, column := range statement.Columns {
		if column.Name == "route_children" {
			t.Fatal("the retired page budget must not be answered as a column")
		}
	}
	found := false
	for _, warning := range statement.Warnings {
		if warning.Code != "configuration_retired_key" {
			continue
		}
		found = true
		for _, expected := range []string{"route_children", "40", "branch_fanout"} {
			if !strings.Contains(warning.Message, expected) {
				t.Fatalf("the notice must name %q: %s", expected, warning.Message)
			}
		}
		if formatted := fmt.Sprintf("%v", warning.Details["retired_value"]); formatted != "40" {
			t.Fatalf("the notice must carry the value it ignored: %v", warning.Details)
		}
	}
	if !found {
		t.Fatalf("a retired stored budget must be reported, got %+v", statement.Warnings)
	}
}
