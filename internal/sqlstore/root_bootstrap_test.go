package sqlstore_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The Skill used to teach "an empty rows array means this Table has no root",
// which is wrong: `SHOW ROUTES ... AT ROOT` lists the root's CHILDREN, so a Table
// without a root and a Table whose root holds nothing answer exactly the same
// way. It now teaches reading the refusal from the create step instead — and an
// instruction that leans on a refusal is only as good as the refusal, so both
// halves of that dependency are pinned here.
func TestRootBootstrapIsLearnedFromTheRefusal(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'Work memory' SCOPE 'Projects and decisions'`, nil, executor.MutationOptions{})
	h.run(`CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One fact per row' `+
		`(title TEXT NOT NULL PURPOSE 'Title' ROLE title)`, nil, executor.MutationOptions{})

	emptyPage := func(when string) {
		h.t.Helper()
		listing := h.run(`SHOW ROUTES FROM TABLE work.notes AT ROOT`, nil, executor.MutationOptions{})
		if len(listing.Rows) != 0 {
			t.Fatalf("%s: the listing must be an empty page, got %v", when, listing.Rows)
		}
	}
	// A Table that never had a root is a legal empty state, not an error.
	emptyPage("before a root exists")

	h.run(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything about work'`, nil, write("root"))

	// And it still answers the same way once the root exists: this is the whole
	// reason the listing cannot be the check.
	emptyPage("with a root and nothing mounted under it")

	// The refusal is the only signal, so it has to name the Table the way the
	// Skill quotes it.
	code, message := h.failureWithMessage(`CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Everything about work'`)
	if code != result.CodeAlreadyExists {
		t.Fatalf("creating a second root: code = %s, want %s", code, result.CodeAlreadyExists)
	}
	if !strings.Contains(message, `table "notes" already has a route root`) {
		t.Fatalf("the refusal the Skill quotes must be the refusal the engine gives: %q", message)
	}
}

// failureWithMessage is `fails` plus the sentence: the refusal is what the Skill
// tells the agent to read, so the wording is part of the contract this test pins.
func (h *harness) failureWithMessage(source string) (result.Code, string) {
	h.t.Helper()
	h.seq++
	envelope := h.session.ExecuteBatch(context.Background(), executor.BatchRequest{
		RequestID: "m" + strings.Repeat("z", h.seq), Source: source,
		Statements: []executor.StatementInput{{
			Parameters:    executor.Parameters{Named: nil},
			Mutation:      write("root"),
			Authorization: authorization,
		}},
	})
	requireDeliverable(h.t, source, envelope)
	if envelope.Error != nil {
		return envelope.Error.Code, envelope.Error.Message
	}
	if envelope.Results[0].Error == nil {
		h.t.Fatalf("%s unexpectedly succeeded", source)
	}
	return envelope.Results[0].Error.Code, envelope.Results[0].Error.Message
}
