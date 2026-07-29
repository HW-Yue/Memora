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
		"SELECT (",
		"\x00\xff",
	}
	for _, seed := range seeds {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		document, err := Parse(source)
		if err != nil {
			return
		}
		if _, err := json.Marshal(document); err != nil {
			t.Fatalf("Marshal() error = %v", err)
		}
	})
}
