package daemon

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

// historyProvenanceFixture writes one Row twice, the second write carrying the
// document-anchor provenance that used to be recorded only on the per-Row
// History record.
func historyProvenanceFixture(t *testing.T) (string, string) {
	t.Helper()
	dataDir := archiveInstance(t)
	existing := executeTraceMSQL(t, dataDir, "SELECT row_id FROM work.notes LIMIT 10", nil)
	rowID, _ := existing.Results[0].Rows[0]["row_id"].(string)
	if rowID == "" {
		t.Fatal("fixture Row is missing")
	}
	executeTraceMSQL(t, dataDir,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "anchored", "row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				Actor: "agent:anchor", Source: "conversation", Reason: "anchored rewrite",
				SourceKind:        "document_anchor",
				SourceLocator:     "docs/design.md#L20",
				SourceContentHash: "0000000000000000000000000000000000000000000000000000000000000000",
			},
		}},
	)
	return dataDir, rowID
}

// TestHistoryKeepsEveryProvenanceFieldOfAWrite pins the whole point of folding
// History into the Change Log: no attribution field may be lost. The three
// anchor fields are the ones at risk — they lived only on the History record.
func TestHistoryKeepsEveryProvenanceFieldOfAWrite(t *testing.T) {
	t.Parallel()

	dataDir, rowID := historyProvenanceFixture(t)

	got := executeTraceMSQL(t, dataDir,
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
		}},
	)
	rows := got.Results[0].Rows
	if len(rows) < 2 {
		t.Fatalf("SHOW HISTORY returned %d records, want at least 2", len(rows))
	}
	newest := rows[0]
	for column, want := range map[string]any{
		"actor":               "agent:anchor",
		"source":              "conversation",
		"reason":              "anchored rewrite",
		"source_kind":         "document_anchor",
		"source_locator":      "docs/design.md#L20",
		"source_content_hash": "0000000000000000000000000000000000000000000000000000000000000000",
	} {
		if newest[column] != want {
			t.Fatalf("newest history %s = %#v, want %#v", column, newest[column], want)
		}
	}
	if newest["revision"] == rows[1]["revision"] {
		t.Fatalf("history revisions must differ: %#v", rows)
	}
}

// TestOneTransactionGivesEveryRowItTouchedTheSameAttribution pins that
// attribution is per-transaction. Recording it once on the envelope rather than
// once per Row is the reason the History record can go away.
func TestOneTransactionGivesEveryRowItTouchedTheSameAttribution(t *testing.T) {
	t.Parallel()

	dataDir := archiveInstance(t)
	listed := executeTraceMSQL(t, dataDir, "SELECT row_id FROM work.notes LIMIT 10", nil)
	if len(listed.Results[0].Rows) < 1 {
		t.Fatal("fixture needs at least one Row")
	}
	rowID, _ := listed.Results[0].Rows[0]["row_id"].(string)
	executeTraceMSQL(t, dataDir,
		"UPDATE work.notes SET title = :title WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"title": "batched", "row": rowID}},
			Mutation: executor.MutationOptions{
				ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
				Actor: "agent:batch", Source: "conversation", Reason: "one transaction",
			},
		}},
	)

	history := executeTraceMSQL(t, dataDir,
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
		}},
	)
	newest := history.Results[0].Rows[0]
	if newest["actor"] != "agent:batch" || newest["reason"] != "one transaction" {
		t.Fatalf("attribution did not follow the transaction: %#v", newest)
	}
	if newest["commit_sequence"] == nil {
		t.Fatal("a history record must carry the commit sequence that joins it to its transaction")
	}
}
