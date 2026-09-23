package executor

import (
	"reflect"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
)

// A parse failure inside an open transaction has to be classified before the
// batch can decide whether to keep going, and all it has to classify by is the
// statement's kind: the statement itself never parsed. That classification used
// to be a hand-written list of kind strings sitting next to mutationStatement,
// the judgement the rest of the executor uses for "is this a write". The two
// drifted — REPAIR, ACCEPT, REKEY and ALTER were writes to one and not to the
// other — so a broken write let the COMMIT after it publish the writes before
// it, and a retry of the request applied them twice.
//
// There is one judgement now. statementKinds is only the parser's vocabulary:
// which Kind strings the parser emits for which node. Whether a node is a write
// is still mutationStatement's call, asked here of a prototype per field, so a
// statement added to mutationStatement arrives here already classified.

// statementKinds maps each ast.Statement node to the Kind strings the parser
// emits for it. Every pointer field must appear; TestEveryStatementNodeDeclares
// ItsKinds fails when one does not, so a new statement cannot be classified by
// silence.
var statementKinds = map[string][]string{
	"Show":          {"SHOW"},
	"Describe":      {"DESCRIBE", "DESCRIBE_ROUTE"},
	"Create":        {"CREATE"},
	"Alter":         {"ALTER"},
	"Select":        {"SELECT"},
	"Insert":        {"INSERT"},
	"Update":        {"UPDATE"},
	"Delete":        {"DELETE"},
	"Restore":       {"RESTORE"},
	"Reshape":       {"SPLIT", "MERGE"},
	"CreateRoute":   {"CREATE_ROUTE"},
	"RenameRoute":   {"RENAME_ROUTE"},
	"UpdateRoute":   {"UPDATE_ROUTE"},
	"OpenRoute":     {"OPEN_ROUTE"},
	"OpenArchive":   {"OPEN_ARCHIVE"},
	"RepairLinks":   {"REPAIR_LINKS"},
	"RepairVector":  {"REPAIR_VECTOR"},
	"RepairRecall":  {"REPAIR_RECALL"},
	"RekeyVector":   {"REKEY_VECTOR"},
	"AcceptVector":  {"ACCEPT_VECTOR"},
	"Recall":        {"RECALL"},
	"PlanRoute":     {"PLAN_ROUTE_MUTATION"},
	"PlanSchema":    {"PLAN_SCHEMA_CHANGE"},
	"ApplyRoute":    {"APPLY_ROUTE_MUTATION"},
	"ApplySchema":   {"APPLY_SCHEMA_CHANGE"},
	"Configuration": {"ALTER_CONFIGURATION", "RESTORE_CONFIGURATION"},
	"Transaction":   {"BEGIN", "COMMIT", "ROLLBACK"},
}

// mutationKinds is derived, never written: for every node mutationStatement
// calls a write, both the kind the parser reports for a statement it finished
// parsing and the kind it reports for one it did not — the first word, which is
// all recovery has — count as a write.
var mutationKinds = buildMutationKinds()

func buildMutationKinds() map[string]bool {
	kinds := map[string]bool{}
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
			kinds[kind] = true
			kinds[firstWord(kind)] = true
		}
	}
	return kinds
}

// firstWord is how the parser names a statement it could not parse: the leading
// word, upper-cased. APPLY_ROUTE_MUTATION is "APPLY ROUTE MUTATION" in source,
// so the part before the first underscore is that word.
func firstWord(kind string) string {
	if index := strings.IndexByte(kind, '_'); index >= 0 {
		return kind[:index]
	}
	return kind
}

// mutationKind reports whether a statement reported under this kind would have
// written, and so whether a parse failure under it must abort the transaction.
func mutationKind(kind string) bool { return mutationKinds[kind] }
