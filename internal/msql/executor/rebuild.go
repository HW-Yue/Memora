package executor

import (
	"context"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
)

func (engine *Engine) rebuildLexicalIndex(
	ctx context.Context, statement *ast.RebuildStatement,
) (Output, error) {
	if engine == nil || engine.lexical == nil || statement == nil || statement.Object != "LEXICAL_INDEX" {
		return Output{}, executeError(result.CodeUnsupported, "lexical index rebuild requires an autocommit maintenance session")
	}
	receipt, err := engine.lexical.RebuildLexicalIndex(ctx)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: []result.Column{
			{Name: "previous_generation", Type: "TEXT"},
			{Name: "generation", Type: "TEXT"},
			{Name: "epoch", Type: "INTEGER"},
			{Name: "plan_digest", Type: "TEXT"},
			{Name: "source_fingerprint", Type: "TEXT"},
			{Name: "previous_snapshot_sha256", Type: "TEXT"},
			{Name: "snapshot_sha256", Type: "TEXT"},
			{Name: "parity", Type: "BOOLEAN"},
			{Name: "verified", Type: "BOOLEAN"},
			{Name: "reused", Type: "BOOLEAN"},
		},
		Rows: []result.Row{{
			"previous_generation":      receipt.PreviousGeneration,
			"generation":               receipt.Generation,
			"epoch":                    receipt.Epoch,
			"plan_digest":              receipt.PlanDigest,
			"source_fingerprint":       receipt.SourceFingerprint,
			"previous_snapshot_sha256": receipt.PreviousSnapshotSHA256,
			"snapshot_sha256":          receipt.SnapshotSHA256,
			"parity":                   receipt.Parity,
			"verified":                 receipt.Verified,
			"reused":                   receipt.Reused,
		}},
		AffectedRows: 1,
	}, nil
}
