package sqlstore_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// A mutation block says what a write was and what it was for. Statements that
// record none used to accept it and drop it: a host that copied the shape from
// `ALTER ROUTE ... SET PURPOSE` onto `CREATE DATABASE` / `ALTER DATABASE ... SET`
// believed an actor and a reason had been recorded, and nothing said otherwise.
func TestStatementsThatRecordNoMutationRefuseAMutationBlock(t *testing.T) {
	h := newHarness(t)

	// Catalog DDL is a write that records no actor and no reason.
	if code := h.fails(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, write("nobody will ever see this")); code != result.CodeValidation {
		t.Fatalf("mutation block on CREATE DATABASE: code = %s, want %s", code, result.CodeValidation)
	}
	// The supported shape is the same statement without the block.
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})

	// Reads are the other half of the same trap.
	if code := h.fails(`SHOW TABLES FROM work LIMIT 10`, nil, write("a reason nobody reads")); code != result.CodeValidation {
		t.Fatalf("mutation block on a read: code = %s, want %s", code, result.CodeValidation)
	}
	// Transaction control takes no block either: there is no work to attach it to.
	if code := h.fails(`BEGIN`, nil, write("open a transaction")); code != result.CodeValidation {
		t.Fatalf("mutation block on BEGIN: code = %s, want %s", code, result.CodeValidation)
	}

	// A statement that does record one still takes it.
	h.run(`CREATE TABLE work.notes PURPOSE 'p' ROW SEMANTICS 'r' `+
		`(title TEXT NOT NULL PURPOSE 't' ROLE title)`, nil, executor.MutationOptions{})
	options := write("rebuild the recall layer")
	options.MaxAffectedRows = 8
	h.run(`REPAIR RECALL UNITS IN DATABASE work LIMIT :limit`, map[string]any{"limit": 8}, options)
}
