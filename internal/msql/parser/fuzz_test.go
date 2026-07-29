package parser

import (
	"encoding/json"
	"testing"
)

func FuzzParseNeverPanics(f *testing.F) {
	seeds := []string{
		"SHOW DATABASES",
		"SELECT * FROM notes WHERE revision >= :revision LIMIT ?",
		"INSERT INTO notes (title) VALUES ('hello')",
		"UPDATE notes SET title = 'new' WHERE row_id = ?",
		"DELETE FROM notes WHERE row_id = :id",
		"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'",
		"ALTER TABLE work.notes ADD COLUMN status TEXT PURPOSE 'State'",
		"SELECT (",
		"\x00\xff",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		document, err := Parse(source)
		if err == nil {
			if _, err := json.Marshal(document); err != nil {
				t.Fatalf("Marshal(document) error = %v", err)
			}
		}
		batch, err := ParseBatch(source)
		if err == nil {
			if _, err := json.Marshal(batch); err != nil {
				t.Fatalf("Marshal(batch) error = %v", err)
			}
		}
		items, err := ParseBatchItems(source)
		if err == nil {
			for _, item := range items {
				if item.Statement != nil {
					if _, err := json.Marshal(item.Statement); err != nil {
						t.Fatalf("Marshal(batch item) error = %v", err)
					}
				}
			}
		}
	})
}
