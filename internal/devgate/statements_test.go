package devgate

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/msql/readquery"
	"github.com/HW-Yue/Memora/internal/security"
)

// statementSamples is one parseable statement per statement kind the parser can
// produce. A kind without a sample fails the gate below, which is the point:
// adding a statement forces its author to say how a read-only transport should
// treat it, instead of leaving it silently unreachable from the CLI.
var statementSamples = map[string]string{
	"SHOW":                  `SHOW DATABASES LIMIT 10`,
	"DESCRIBE":              `DESCRIBE TABLE work.notes`,
	"DESCRIBE_ROUTE":        `DESCRIBE ROUTE :route`,
	"SELECT":                `SELECT * FROM work.notes LIMIT 1`,
	"INSERT":                `INSERT INTO work.notes (title) VALUES ('x')`,
	"UPDATE":                `UPDATE work.notes SET title = 'x' WHERE row_id = 'row_1'`,
	"DELETE":                `DELETE FROM work.notes WHERE row_id = 'row_1'`,
	"RESTORE":               `RESTORE work.notes ROW 'row_1' TO REVISION 1`,
	"SPLIT":                 `SPLIT work.notes ROW :row INTO (title) VALUES ('a'), ('b')`,
	"MERGE":                 `MERGE work.notes ROWS (:a, :b) INTO (title) VALUES ('x')`,
	"CREATE":                `CREATE DATABASE work PURPOSE 'p' SCOPE 's'`,
	"ALTER":                 `ALTER TABLE work.notes RENAME TO other`,
	"ALTER_CONFIGURATION":   `ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT 16`,
	"RESTORE_CONFIGURATION": `RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION 3`,
	"CREATE_ROUTE":          `CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'p'`,
	"RENAME_ROUTE":          `ALTER ROUTE :route RENAME TO :name`,
	"UPDATE_ROUTE":          `ALTER ROUTE :route SET SYNOPSIS :synopsis`,
	"OPEN_ROUTE":            `OPEN ROUTE :leaf LIMIT 1`,
	"OPEN_ARCHIVE":          `OPEN ARCHIVE :archive`,
	"RECALL":                `RECALL FROM work MATCH :query LIMIT 5`,
	"PLAN_ROUTE_MUTATION":   `PLAN ROUTE MUTATION FOR TABLE work.notes USING :proposal`,
	"APPLY_ROUTE_MUTATION":  `APPLY ROUTE MUTATION PLAN :plan FOR TABLE work.notes`,
	"PLAN_SCHEMA_CHANGE":    `PLAN SCHEMA CHANGE FOR TABLE work.notes USING :proposal`,
	"APPLY_SCHEMA_CHANGE":   `APPLY SCHEMA CHANGE PLAN :plan FOR TABLE work.notes`,
	"REPAIR_LINKS":          `REPAIR LINKS IN DATABASE work LIMIT 8`,
	"REPAIR_VECTOR":         `REPAIR VECTOR INDEX IN DATABASE work LIMIT 8`,
	"REPAIR_RECALL":         `REPAIR RECALL UNITS IN DATABASE work LIMIT 8`,
	"ACCEPT_VECTOR":         `ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE work MODEL :model HASH :hash`,
	// Transactions take their kind from the action, so the scan below reads the
	// call sites as well as the literals.
	"BEGIN":    `BEGIN`,
	"COMMIT":   `COMMIT`,
	"ROLLBACK": `ROLLBACK`,
}

var (
	parsedKind      = regexp.MustCompile(`Kind: "([A-Z_]+)"`)
	transactionKind = regexp.MustCompile(`transactionStatement\("([A-Z_]+)"\)`)
)

// TestEveryStatementKindHasASample keeps the read-only transports from drifting
// away from the language. The parser is the source of truth for which kinds
// exist; a new one has to appear here before the gate passes.
func TestEveryStatementKindHasASample(t *testing.T) {
	produced := map[string]bool{}
	files, err := filepath.Glob(filepath.Join(repoRoot(t), "internal", "msql", "parser", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range parsedKind.FindAllStringSubmatch(string(body), -1) {
			produced[match[1]] = true
		}
		for _, match := range transactionKind.FindAllStringSubmatch(string(body), -1) {
			produced[match[1]] = true
		}
	}
	if len(produced) == 0 {
		t.Fatal("no statement kinds found in the parser; the scan is broken")
	}
	kinds := make([]string, 0, len(produced))
	for kind := range produced {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		source, ok := statementSamples[kind]
		if !ok {
			t.Errorf("statement kind %s has no sample; add one and decide how a read-only transport treats it", kind)
			continue
		}
		items, err := parser.ParseBatchItems(source)
		if err != nil || len(items) != 1 || items[0].Statement == nil {
			t.Errorf("sample for %s does not parse: %q (%v)", kind, source, err)
			continue
		}
		if items[0].Kind != kind {
			t.Errorf("sample for %s parses as %s: %q", kind, items[0].Kind, source)
		}
	}
	for kind := range statementSamples {
		if !produced[kind] {
			t.Errorf("sample %s no longer matches a statement the parser produces", kind)
		}
	}
}

// TestReadOnlyPolicyFollowsTheClassification proves the transport is not keeping
// a list of its own: a recalled path and an opened archive are reads, and a
// transaction is not.
func TestReadOnlyPolicyFollowsTheClassification(t *testing.T) {
	cases := []struct {
		source string
		read   bool
	}{
		{source: `RECALL FROM work MATCH :query LIMIT 5`, read: true},
		{source: `OPEN ARCHIVE :archive`, read: true},
		{source: `SHOW ARCHIVE FROM work.notes LIMIT 5`, read: true},
		{source: `SELECT * FROM work.notes LIMIT 1`, read: true},
		{source: `REPAIR LINKS IN DATABASE work LIMIT 8`, read: false},
		{source: `BEGIN`, read: false},
		{source: `INSERT INTO work.notes (title) VALUES ('x')`, read: false},
	}
	for _, test := range cases {
		items, err := parser.ParseBatchItems(test.source)
		if err != nil || len(items) != 1 || items[0].Statement == nil {
			t.Fatalf("%q does not parse: %v", test.source, err)
		}
		level := executor.StatementRiskLevel(*items[0].Statement)
		if got := level == security.LevelRead; got != test.read {
			t.Errorf("%q is classified %s", test.source, level)
		}
		if readquery.Allowed(items[0]) != test.read {
			t.Errorf("%q: the read-only policy disagrees with the classification", test.source)
		}
	}
}
