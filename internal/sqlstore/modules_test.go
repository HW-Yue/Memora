package sqlstore

import (
	"context"
	"path/filepath"
	"testing"
)

// SQLite's optional modules are compiled in, not assumed: a build that drops a
// build tag produces a binary where recall silently has no index. This is the
// assertion the plan asks for, so a missing module fails here instead of at the
// first recall.
func TestSQLiteModulesAreCompiledIn(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "memora.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	modules := []struct {
		name string
		ddl  string
	}{
		{name: "fts5", ddl: `CREATE VIRTUAL TABLE module_fts USING fts5(body)`},
		{name: "fts5 trigram tokenizer", ddl: `CREATE VIRTUAL TABLE module_trigram USING fts5(body, tokenize='trigram')`},
	}
	for _, module := range modules {
		if _, err := db.SQL().ExecContext(ctx, module.ddl); err != nil {
			t.Fatalf("%s is not compiled in: %v (build with the documented tags)", module.name, err)
		}
	}
}
