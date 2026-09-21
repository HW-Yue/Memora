package sqlstore

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/sqlstore/vecext"
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
		// Vectors are required rather than optional: vector recall is one of the
		// four paths, so a build without vec0 must fail at open, not at query.
		{name: "sqlite-vec vec0", ddl: `CREATE VIRTUAL TABLE module_vec USING vec0(embedding float[4])`},
	}
	for _, module := range modules {
		if _, err := db.SQL().ExecContext(ctx, module.ddl); err != nil {
			t.Fatalf("%s is not compiled in: %v (build with the documented tags)", module.name, err)
		}
	}
}

// The vector path is required, so "the module is registered" is not enough: the
// index has to answer a nearest-neighbour query. This runs offline with
// hand-written vectors, so it proves the compiled module works rather than that
// some model returned something.
func TestVectorIndexAnswersAKnnQuery(t *testing.T) {
	ctx := context.Background()
	db, err := Open(filepath.Join(t.TempDir(), "memora.db"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if _, err := db.SQL().ExecContext(ctx,
		`CREATE VIRTUAL TABLE vec_probe USING vec0(embedding float[3])`); err != nil {
		t.Fatal(err)
	}
	for ordinal, vector := range [][]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}} {
		encoded, err := vecext.SerializeFloat32(vector)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.SQL().ExecContext(ctx,
			`INSERT INTO vec_probe(rowid, embedding) VALUES (?, ?)`, ordinal+1, encoded); err != nil {
			t.Fatal(err)
		}
	}

	query, err := vecext.SerializeFloat32([]float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	var rowid int64
	var distance float64
	if err := db.SQL().QueryRowContext(ctx,
		`SELECT rowid, distance FROM vec_probe WHERE embedding MATCH ? AND k = 1`, query).
		Scan(&rowid, &distance); err != nil {
		t.Fatal(err)
	}
	if rowid != 2 {
		t.Fatalf("nearest row = %d, want the row whose vector is the query", rowid)
	}
	if distance > 0.0001 {
		t.Fatalf("distance to an identical vector = %v", distance)
	}
}
