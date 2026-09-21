package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The read surface is narrow on purpose: one equality on row_id, `AND` to join
// more, and every returned Row carrying its own `route_paths` and `links`. A
// fresh agent walks into the edges of that surface with the shapes it knows from
// SQL — `IN (…)` for several Rows, `route_paths` in the projection — so each edge
// has to say what the surface is and what to do instead. These tests pin the
// message, because the message is the whole fix: the failure is not a bug, the
// silence around it was.

func (h *harness) readFailure(source string, named map[string]any) (result.Code, string) {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "m" + strings.Repeat("z", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Parameters: executor.Parameters{Named: named}, Mutation: executor.MutationOptions{}, Authorization: authorization,
		}},
	})
	requireDeliverable(h.t, source, envelope)
	if envelope.Error != nil {
		return envelope.Error.Code, envelope.Error.Message
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	failure := envelope.Results[0].Error
	return failure.Code, failure.Message
}

func TestAnUnsupportedReadOperatorSaysWhatToDoInstead(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one", "two")
	first := h.insertTitle("第一条事实", []string{leaves[0]})
	second := h.insertTitle("第二条事实", []string{leaves[1]})

	code, message := h.readFailure(
		`SELECT title FROM work.notes WHERE row_id IN (:a, :b) LIMIT 2`,
		map[string]any{"a": first, "b": second})
	if code != result.CodeParseError {
		t.Fatalf("code = %s, want %s", code, result.CodeParseError)
	}
	for _, expected := range []string{"row_id", "not part of the read surface", "--input array"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("message must explain the read surface (%q missing): %s", expected, message)
		}
	}
}

func TestAReadLimitAboveTheBudgetNamesTheBudgetAccessor(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("唯一一条事实", []string{leaves[0]})

	code, message := h.readFailure(`SELECT title FROM work.notes LIMIT 50`, nil)
	if code != result.CodeValidation {
		t.Fatalf("code = %s, want %s", code, result.CodeValidation)
	}
	for _, expected := range []string{"select_rows", "SHOW CONFIGURATION", "SELECT_ROWS"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("a refusal must name the budget and how to read it (%q missing): %s", expected, message)
		}
	}
}

func TestProjectingAnAttachedFieldExplainsWhereItComesFrom(t *testing.T) {
	h := newHarness(t)
	leaves := h.seedLeaves("one")
	h.insertTitle("唯一一条事实", []string{leaves[0]})

	code, message := h.readFailure(`SELECT route_paths FROM work.notes LIMIT 1`, nil)
	if code != result.CodeValidation {
		t.Fatalf("code = %s, want %s", code, result.CodeValidation)
	}
	if !strings.Contains(message, "attached to every returned Row") {
		t.Fatalf("message must say route_paths rides along on the Row: %s", message)
	}
	// The projection an agent should have written keeps working, and the Row
	// still carries the membership it was looking for.
	row := h.run(`SELECT title FROM work.notes LIMIT 1`, nil, executor.MutationOptions{}).Rows[0]
	if _, ok := row["route_paths"]; !ok {
		t.Fatalf("every returned Row must carry route_paths: %v", row)
	}
}
