package daemon

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/nativemigration"
	"github.com/HW-Yue/Memora/internal/pagestoremigration"
)

// PageUpgradeReceipt reports what an explicit generation upgrade did.
type PageUpgradeReceipt struct {
	// Generation is the directory the upgraded generation was published under.
	Generation string `json:"generation"`
	// Epoch is the generation's epoch after the upgrade.
	Epoch uint64 `json:"epoch"`
	// PreviousGeneration is left on disk untouched, as the rollback point.
	PreviousGeneration string `json:"previous_generation"`
}

// UpgradePageGeneration rebuilds a Database's page generation from the record
// log, with the daemon stopped.
//
// This is the explicit half of what opening used to do by itself. E8 stage 2
// took the rebuild off the open path: an open that rebuilds is an open that
// reads the record log to decide what the Database holds, which is the thing
// clustered promotion says the Database no longer depends on
// (docs/storage/record-index-and-authority-v1.md §5). The rebuild still works —
// the log is on disk as the archive — it is just asked for now.
//
// The old generation is left where it is. A COW upgrade's whole value is that
// the Database it replaces is still there to go back to.
func UpgradePageGeneration(ctx context.Context, dataDir string) (PageUpgradeReceipt, error) {
	if !filepath.IsAbs(dataDir) {
		return PageUpgradeReceipt{}, fmt.Errorf("data directory must be absolute")
	}
	lease, err := AcquireMaintenance(dataDir)
	if err != nil {
		return PageUpgradeReceipt{}, err
	}
	defer func() { _ = lease.Close() }()

	migration, err := nativemigration.OpenDefault(ctx, dataDir)
	if err != nil {
		return PageUpgradeReceipt{}, err
	}
	defer func() {
		_ = migration.File.Close()
		if migration.Binlog != nil {
			_ = migration.Binlog.Close()
		}
	}()
	directory := filepath.Join(dataDir, "databases")
	receipt, err := pagestoremigration.UpgradeGeneration(ctx, migration.File, directory)
	if err != nil {
		if errors.Is(err, pagestoremigration.ErrConflict) {
			return PageUpgradeReceipt{}, fmt.Errorf("page generation is already current: %w", err)
		}
		return PageUpgradeReceipt{}, err
	}
	return PageUpgradeReceipt{
		Generation:         receipt.Generation,
		Epoch:              receipt.Epoch,
		PreviousGeneration: receipt.PreviousGeneration,
	}, nil
}
