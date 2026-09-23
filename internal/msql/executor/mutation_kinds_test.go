package executor

import (
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
)

// The classification of "is this a write" lives in exactly one place. These
// tests hold the two properties that keep it there: every statement node is
// spoken for, and the kind a parse failure is reported under is a kind the
// derivation actually covers.
func TestEveryStatementNodeDeclaresItsKinds(t *testing.T) {
	statement := reflect.TypeOf(ast.Statement{})
	for index := 0; index < statement.NumField(); index++ {
		field := statement.Field(index)
		if field.Type.Kind() != reflect.Pointer {
			continue
		}
		if len(statementKinds[field.Name]) == 0 {
			t.Errorf("ast.Statement.%s declares no kind: a statement no table names cannot be classified", field.Name)
		}
	}
	for name := range statementKinds {
		if _, ok := statement.FieldByName(name); !ok {
			t.Errorf("statementKinds names %q, which ast.Statement does not have", name)
		}
	}
}

// A write cut short is reported by its first word. Every write node's first
// word has to classify as a mutation, or the batch keeps going after it.
func TestEveryWriteStatementIsAMutationKindByItsFirstWord(t *testing.T) {
	statement := reflect.TypeOf(ast.Statement{})
	for index := 0; index < statement.NumField(); index++ {
		field := statement.Field(index)
		if field.Type.Kind() != reflect.Pointer {
			continue
		}
		prototype := ast.Statement{}
		reflect.ValueOf(&prototype).Elem().Field(index).Set(reflect.New(field.Type.Elem()))
		if !mutationStatement(prototype) {
			continue
		}
		for _, kind := range statementKinds[field.Name] {
			if !mutationKind(kind) || !mutationKind(firstWord(kind)) {
				t.Errorf("%s is a write, so %q and %q must abort a batch", field.Name, kind, firstWord(kind))
			}
		}
	}
}

// And the first words are really the ones the parser reports: a truncated write
// comes back under the word the derivation expects.
func TestTruncatedWritesAreReportedUnderAMutationKind(t *testing.T) {
	for _, source := range []string{
		`REPAIR LINKS IN DATABASE`,
		`REPAIR VECTOR INDEX IN`,
		`REPAIR RECALL UNITS IN`,
		`ACCEPT VECTOR FOR`,
		`REKEY VECTOR IN`,
		`ALTER CONFIGURATION SET`,
		`RESTORE CONFIGURATION TO`,
		`APPLY ROUTE MUTATION FOR`,
		`APPLY SCHEMA CHANGE FOR`,
		`INSERT INTO work.notes`,
		`UPDATE work.notes SET`,
		`DELETE FROM`,
		`SPLIT work.notes ROW`,
		`MERGE work.notes ROWS`,
		`CREATE ROUTE UNDER`,
		`RENAME ROUTE`,
		`UPDATE ROUTE`,
	} {
		items, err := parser.ParseBatchItems(source)
		if err != nil || len(items) != 1 || items[0].Issue == nil {
			t.Fatalf("%q must parse as one failed statement: %v", source, err)
		}
		if !mutationKind(items[0].Kind) {
			t.Errorf("%q is reported as %q, which does not abort a batch", source, items[0].Kind)
		}
	}
}
