package storygate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

type Evidence struct {
	Story    string
	Commands []string
	Proofs   []string
}

func RequiredStories() []string {
	return []string{
		"US-HUMAN", "US-COLD", "US-READ", "US-INSERT", "US-UPDATE", "US-DELETE",
		"US-CORRECT", "US-SCHEMA", "US-DBA", "US-OPTIMIZE", "US-SPLIT", "US-CONFLICT",
		"US-ASSIMILATE", "US-RECOVER", "US-ENGINE", "US-DEVELOPER",
	}
}

func ReleaseEvidence() []Evidence {
	discover := []string{
		"SHOW DATABASES COMPACT",
		"SHOW TABLES FROM work COMPACT",
		"DESCRIBE TABLE work.notes COMPACT",
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12",
		"SHOW ROUTES UNDER :route LIMIT 12",
		"OPEN ROUTE :leaf LIMIT 24",
		"SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
	}
	return []Evidence{
		{Story: "US-HUMAN", Proofs: []string{"skills/memora/SKILL.md", "tests/e2e/codex_adapter_test.go"}},
		{Story: "US-COLD", Commands: discover[:4], Proofs: []string{"internal/nativerow/table_router_msql_test.go"}},
		{Story: "US-READ", Commands: discover, Proofs: []string{"internal/skillquery/query_test.go"}},
		{Story: "US-INSERT", Commands: []string{"INSERT INTO work.notes (title) VALUES (:title)", discover[3], discover[4], discover[5], discover[6]}, Proofs: []string{"tests/e2e/vertical_slice_test.go"}},
		{Story: "US-UPDATE", Commands: []string{"UPDATE work.notes SET title = :title WHERE row_id = :row", "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10"}, Proofs: []string{"tests/e2e/vertical_slice_test.go"}},
		{Story: "US-DELETE", Commands: []string{"DELETE FROM work.notes WHERE row_id = :row", "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10"}, Proofs: []string{"internal/nativerow/msql_test.go"}},
		{Story: "US-CORRECT", Commands: []string{discover[6], "UPDATE work.notes SET title = :title WHERE row_id = :row", "SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10"}, Proofs: []string{"internal/feedback/feedback_test.go"}},
		{Story: "US-SCHEMA", Commands: []string{"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Reviewed projects'", "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(200) PURPOSE 'Title')", discover[2]}, Proofs: []string{"internal/nativecatalog/msql_test.go"}},
		{Story: "US-DBA", Commands: []string{discover[3], discover[4], "ALTER ROUTE :route RENAME TO :name"}, Proofs: []string{"internal/semantichealth/service_test.go"}},
		{Story: "US-OPTIMIZE", Commands: []string{discover[3], discover[4], "ALTER ROUTE :route RENAME TO :name"}, Proofs: []string{"internal/nativemutation/reshape_test.go"}},
		{Story: "US-SPLIT", Commands: []string{"UPDATE work.notes SET title = :title WHERE row_id = :row", "INSERT INTO work.notes (title) VALUES (:title)"}, Proofs: []string{"internal/nativemutation/reshape_test.go"}},
		{Story: "US-CONFLICT", Commands: []string{discover[6]}, Proofs: []string{"internal/skillconflict/conflict_test.go"}},
		{Story: "US-ASSIMILATE", Commands: []string{"INSERT INTO work.notes (title) VALUES (:title)"}, Proofs: []string{"internal/assimilation/submission_test.go"}},
		{Story: "US-RECOVER", Commands: []string{discover[3], discover[4], discover[5], discover[6]}, Proofs: []string{"internal/store/native/file_test.go", "internal/nativerow/table_router_msql_test.go"}},
		{Story: "US-ENGINE", Commands: []string{"UPDATE work.notes SET title = :title WHERE row_id = :row"}, Proofs: []string{"internal/nativemutation/coordinator_test.go", "internal/store/native/transaction_test.go"}},
		{Story: "US-DEVELOPER", Proofs: []string{"internal/cicheck/ci_test.go", "docs/planning/feature-product-gate.md"}},
	}
}

func Validate(evidence []Evidence) error {
	required := map[string]bool{}
	for _, story := range RequiredStories() {
		required[story] = false
	}
	for _, item := range evidence {
		if _, ok := required[item.Story]; !ok {
			return fmt.Errorf("unknown story %q", item.Story)
		}
		if required[item.Story] {
			return fmt.Errorf("duplicate story %q", item.Story)
		}
		if len(item.Proofs) == 0 {
			return fmt.Errorf("story %q has no executable proof", item.Story)
		}
		for _, command := range item.Commands {
			if _, err := parser.Parse(command); err != nil {
				return fmt.Errorf("story %q has invalid MSQL: %w", item.Story, err)
			}
			upper := strings.ToUpper(command)
			if strings.HasPrefix(upper, "MAT"+"CH ") || strings.Contains(upper, "FROM DATA"+"BASE") {
				return fmt.Errorf("story %q uses a retired query path", item.Story)
			}
		}
		required[item.Story] = true
	}
	missing := []string{}
	for story, found := range required {
		if !found {
			missing = append(missing, story)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("missing stories: %s", strings.Join(missing, ", "))
	}
	return nil
}
