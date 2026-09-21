package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
)

// SQLite's vector index is compiled into this binary from source, and the
// product treats it as required rather than optional: every build that can open
// a database must be able to answer a vector query. So a build that dropped it
// refuses to open the database, instead of failing one path later with
// "no such module: vec0" — which reads like a mistake in the query rather than
// a property of the binary. Registering has to happen before the first
// connection, because it is an SQLite auto-extension; asserting only needs to
// happen once per process.
var vecModule struct {
	once sync.Once
	err  error
}

func requireVectorModule(ctx context.Context, handle *sql.DB) error {
	vecModule.once.Do(func() {
		version, err := vectorModuleVersion(ctx, handle)
		if err != nil {
			vecModule.err = fmt.Errorf("this build has no sqlite-vec, so vector recall cannot work: %w", err)
			return
		}
		if strings.TrimSpace(version) == "" {
			vecModule.err = errors.New("this build has no sqlite-vec: it reports no version")
		}
	})
	return vecModule.err
}
