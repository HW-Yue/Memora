//go:build cgo

package sqlstore

import (
	"context"
	"database/sql"

	"github.com/HW-Yue/Memora/internal/sqlstore/vecext"
)

// registerVectorModule compiles sqlite-vec into the binary and makes it visible
// to every connection opened afterwards.
func registerVectorModule() { vecext.Auto() }

// vectorModuleVersion is the assertion the open path runs: a binary without the
// module cannot answer this, and there is no build in which the vector path is
// absent but everything else works.
func vectorModuleVersion(ctx context.Context, handle *sql.DB) (string, error) {
	version := ""
	if err := handle.QueryRowContext(ctx, `SELECT vec_version()`).Scan(&version); err != nil {
		return "", err
	}
	return version, nil
}
