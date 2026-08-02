package daemon

import (
	"context"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/pagestoremigration"
)

// nativePageServices is the daemon composition adapter. Page Store remains
// independent of MSQL while the executor sees only its narrow read and
// maintenance ports.
type nativePageServices struct {
	*pagestoremigration.Authority
}

func (services *nativePageServices) RebuildLexicalIndex(
	ctx context.Context,
) (executor.LexicalRebuildReceipt, error) {
	receipt, err := services.Authority.ReplaceGeneration(ctx)
	if err != nil {
		return executor.LexicalRebuildReceipt{}, err
	}
	return executor.LexicalRebuildReceipt{
		PreviousGeneration:     receipt.PreviousGeneration,
		Generation:             receipt.Generation,
		Epoch:                  receipt.Epoch,
		PlanDigest:             receipt.PlanDigest,
		SourceFingerprint:      receipt.SourceFingerprint,
		PreviousSnapshotSHA256: receipt.PreviousSnapshotSHA256,
		SnapshotSHA256:         receipt.SnapshotSHA256,
		Parity:                 receipt.Parity,
		Verified:               receipt.Verified,
		Reused:                 receipt.Reused,
	}, nil
}
