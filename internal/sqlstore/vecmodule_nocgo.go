//go:build !cgo

package sqlstore

import (
	"context"
	"database/sql"
	"errors"
)

// Without cgo there is no usable SQLite driver either, so a build in this shape
// only exists for vet and lint. It reports the missing module rather than
// pretending the vector path is there.
func registerVectorModule() {}

func vectorModuleVersion(context.Context, *sql.DB) (string, error) {
	return "", errors.New("sqlite-vec is a C extension and needs cgo")
}
