package logical

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
)

func TestParseDeclarationCanonicalizesSupportedTypes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		kind   Kind
		limit  int
	}{
		{"INTEGER", KindInteger, 0},
		{"boolean", KindBoolean, 0},
		{"TIMESTAMP", KindTimestamp, 0},
		{"TEXT", KindText, DefaultTextCharacters},
		{"text(42)", KindText, 42},
		{"RELATION_ID", KindRelationID, 0},
	}
	for _, test := range tests {
		definition, err := ParseDeclaration(test.source)
		if err != nil {
			t.Fatalf("ParseDeclaration(%q) error = %v", test.source, err)
		}
		if definition.Kind != test.kind || definition.MaxCharacters != test.limit {
			t.Fatalf("ParseDeclaration(%q) = %#v", test.source, definition)
		}
	}

	for _, source := range []string{"", "FLOAT", "INTEGER(2)", "TEXT()", "TEXT(0)", "TEXT(-1)", "TEXT(1,2)"} {
		_, err := ParseDeclaration(source)
		assertLogicalCode(t, err, result.CodeValidation)
	}
}

func TestValidateSupportsNullIntegerBooleanTimeTextAndRelationID(t *testing.T) {
	t.Parallel()

	timestamp := time.Date(2026, 7, 30, 8, 9, 10, 123, time.FixedZone("test", 8*60*60))
	tests := []struct {
		name       string
		constraint Constraint
		value      any
		want       any
	}{
		{"nullable null", Constraint{Name: "optional", Definition: Definition{Kind: KindText, MaxCharacters: 10}, Nullable: true}, nil, nil},
		{"integer", Constraint{Name: "count", Definition: Definition{Kind: KindInteger}}, int(42), int64(42)},
		{"json integer", Constraint{Name: "count", Definition: Definition{Kind: KindInteger}}, json.Number("-7"), int64(-7)},
		{"boolean", Constraint{Name: "done", Definition: Definition{Kind: KindBoolean}}, true, true},
		{"time value", Constraint{Name: "at", Definition: Definition{Kind: KindTimestamp}}, timestamp, timestamp.UTC()},
		{"time string", Constraint{Name: "at", Definition: Definition{Kind: KindTimestamp}}, "2026-07-30T08:09:10+08:00", time.Date(2026, 7, 30, 0, 9, 10, 0, time.UTC)},
		{"unicode text", Constraint{Name: "body", Definition: Definition{Kind: KindText, MaxCharacters: 4}}, "记忆🧠a", "记忆🧠a"},
		{"relation", Constraint{Name: "parent", Definition: Definition{Kind: KindRelationID}}, "row_01J0MEMORA", "row_01J0MEMORA"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := Validate(test.constraint, test.value)
			if err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if !valuesEqual(got, test.want) {
				t.Fatalf("Validate() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestValidateRejectsMismatchesWithoutCoercionOrTruncation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		constraint Constraint
		value      any
		code       result.Code
	}{
		{"required null", Constraint{Name: "title", Definition: Definition{Kind: KindText, MaxCharacters: 10}}, nil, result.CodeConstraint},
		{"integer float", Constraint{Name: "count", Definition: Definition{Kind: KindInteger}}, float64(1), result.CodeConstraint},
		{"integer overflow", Constraint{Name: "count", Definition: Definition{Kind: KindInteger}}, uint64(math.MaxUint64), result.CodeConstraint},
		{"boolean string", Constraint{Name: "done", Definition: Definition{Kind: KindBoolean}}, "true", result.CodeConstraint},
		{"invalid time", Constraint{Name: "at", Definition: Definition{Kind: KindTimestamp}}, "tomorrow", result.CodeConstraint},
		{"invalid utf8", Constraint{Name: "body", Definition: Definition{Kind: KindText, MaxCharacters: 10}}, string([]byte{0xff}), result.CodeConstraint},
		{"invalid relation prefix", Constraint{Name: "parent", Definition: Definition{Kind: KindRelationID}}, "db_123", result.CodeConstraint},
		{"invalid relation characters", Constraint{Name: "parent", Definition: Definition{Kind: KindRelationID}}, "row_bad value", result.CodeConstraint},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Validate(test.constraint, test.value)
			assertLogicalCode(t, err, test.code)
		})
	}

	original := "四个字符"
	_, err := Validate(Constraint{Name: "body", Definition: Definition{Kind: KindText, MaxCharacters: 3}}, original)
	assertLogicalCode(t, err, result.CodeValueTooLong)
	var logicalError *Error
	if !errors.As(err, &logicalError) || logicalError.Actual != 4 || logicalError.Limit != 3 {
		t.Fatalf("length error = %#v", err)
	}
	if original != "四个字符" {
		t.Fatalf("input was mutated to %q", original)
	}
}

func assertLogicalCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var logicalError *Error
	if !errors.As(err, &logicalError) || logicalError.Code != want || logicalError.StableCode() != string(want) {
		t.Fatalf("error = %v, want logical code %q", err, want)
	}
}

func valuesEqual(left, right any) bool {
	leftTime, leftIsTime := left.(time.Time)
	rightTime, rightIsTime := right.(time.Time)
	if leftIsTime || rightIsTime {
		return leftIsTime && rightIsTime && leftTime.Equal(rightTime)
	}
	return left == right
}
