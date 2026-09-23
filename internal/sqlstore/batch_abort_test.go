package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// One request, several statements, one open transaction. A statement that does
// not parse cannot be executed — but whether the batch may keep going depends
// on what that statement was going to do. A read that does not parse leaves the
// transaction alone; a *write* that does not parse means the caller's intent for
// this transaction is only half known, so the transaction is rolled back and the
// rest of the request is skipped, COMMIT included. Anything else lets a COMMIT
// in the same request publish the writes that came before the broken one, and a
// retry of the whole request applies them twice.
func (h *harness) batch(source string, inputs []executor.StatementInput) result.Envelope {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "b" + strings.Repeat("z", h.seq), Source: source, Statements: inputs,
	})
	requireDeliverable(h.t, source, envelope)
	return envelope
}

func TestAWriteThatDoesNotParseAbortsTheWholeBatch(t *testing.T) {
	// Each of these is a write statement cut short. The parser recovers at the
	// semicolon and reports the statement by its first word, which is all the
	// batch has to classify it by.
	for _, malformed := range []struct {
		kind   string
		source string
	}{
		{"REPAIR", `REPAIR LINKS IN DATABASE`},
		{"ACCEPT", `ACCEPT VECTOR FOR`},
		{"REKEY", `REKEY VECTOR IN`},
		{"ALTER", `ALTER CONFIGURATION SET`},
	} {
		t.Run(malformed.kind, func(t *testing.T) {
			h := newHarness(t)
			h.seedTree()

			insert := write("a write that must not survive its batch")
			insert.RoutePath = pathOf("architecture", "sqlite")
			envelope := h.batch(
				`BEGIN; INSERT INTO work.notes (title) VALUES ('kept'); `+malformed.source+`; COMMIT`,
				[]executor.StatementInput{
					{Authorization: authorization},
					{Mutation: insert, Authorization: authorization},
					{Authorization: authorization},
					{Authorization: authorization},
				})

			if envelope.Error != nil {
				t.Fatalf("the request itself is well formed: %+v", envelope.Error)
			}
			if len(envelope.Results) != 4 {
				t.Fatalf("one result per statement: %+v", envelope.Results)
			}
			if envelope.Results[1].Status != result.StatusRolledBack {
				t.Fatalf("the write before the broken one must be rolled back: %+v", envelope.Results[1])
			}
			if envelope.Results[2].Status != result.StatusFailed {
				t.Fatalf("the broken statement failed to parse: %+v", envelope.Results[2])
			}
			if envelope.Results[3].Status == result.StatusSucceeded {
				t.Fatalf("COMMIT must not publish a half-known transaction: %+v", envelope.Results[3])
			}
			if count := h.rawRowCount(); count != 0 {
				t.Fatalf("nothing from the batch may be on disk: %d rows", count)
			}
		})
	}
}
