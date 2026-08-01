package readquery

import (
	"errors"
	"testing"
)

func TestValidateRejectsEveryNonReadItemIncludingAfterParseFailure(t *testing.T) {
	t.Parallel()

	tests := []string{
		"UPDATE work.notes SET title = 'unsafe'",
		"SELECT * work.notes LIMIT 1; DELETE FROM work.notes WHERE row_id = 'row_1'",
		"SHOW DATABASES; BEGIN; SELECT * FROM work.notes LIMIT 1",
		"OPEN PACKAGE :package",
	}
	for _, source := range tests {
		if _, err := Validate(source); !errors.Is(err, ErrNotReadOnly) {
			t.Errorf("Validate(%q) error = %v, want ErrNotReadOnly", source, err)
		}
	}
}

func TestValidateAllowsReadBatchAndPreservesParseErrorsForExecutor(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		source string
		count  int
	}{
		{"SHOW DATABASES; DESCRIBE TABLE work.notes; SELECT * FROM work.notes LIMIT 1", 3},
		{"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 16; OPEN ROUTE :leaf LIMIT 16", 2},
		{"SELECT * work.notes; SHOW DATABASES", 2},
		{"unterminated 'string", 0},
	} {
		count, err := Validate(test.source)
		if err != nil || count != test.count {
			t.Errorf("Validate(%q) = %d, %v, want %d, nil", test.source, count, err, test.count)
		}
	}
}
