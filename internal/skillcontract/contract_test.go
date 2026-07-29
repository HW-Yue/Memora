package skillcontract_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillcontract"
)

func TestCanonicalSkillContract(t *testing.T) {
	t.Parallel()

	bundle, err := skillcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	if got, want := bundle.Contract.MSQLASTVersion, ast.Version; got != want {
		t.Fatalf("MSQL AST version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.ResultVersion, result.Version; got != want {
		t.Fatalf("result version = %q, want %q", got, want)
	}

	for _, example := range bundle.Contract.Examples {
		if example.MSQL == "" {
			continue
		}
		if _, err := parser.ParseBatch(example.MSQL); err != nil {
			t.Errorf("example %q has invalid MSQL: %v", example.Name, err)
		}
	}
}

func TestCanonicalSkillCommandExamplesGolden(t *testing.T) {
	t.Parallel()

	bundle, err := skillcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "commands.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.RenderCommandExamples(); got != string(want) {
		t.Fatalf("command examples changed\n--- got ---\n%s--- want ---\n%s", got, want)
	}
}

func TestCanonicalSkillForbidsPhysicalReadsAndEscalatesConflicts(t *testing.T) {
	t.Parallel()

	bundle, err := skillcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	if got := bundle.Contract.PhysicalAccess; got != skillcontract.PhysicalAccessForbidden {
		t.Fatalf("physical access = %q, want %q", got, skillcontract.PhysicalAccessForbidden)
	}
	if got := bundle.Contract.ConflictPolicy; got != skillcontract.ConflictAskUserBeforeMutation {
		t.Fatalf("conflict policy = %q, want %q", got, skillcontract.ConflictAskUserBeforeMutation)
	}
	for _, token := range []string{"sqlite3 ", "prototype.sqlite", "/databases/", ".wal"} {
		if strings.Contains(bundle.Markdown, token) {
			t.Errorf("canonical Skill contains forbidden physical access token %q", token)
		}
	}
}

func TestCanonicalSkillContextBudgetsAreBounded(t *testing.T) {
	t.Parallel()

	bundle, err := skillcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	budgets := bundle.Contract.Budgets
	for name, value := range map[string]int{
		"route_rows":         budgets.RouteRows,
		"candidate_rows":     budgets.CandidateRows,
		"select_rows":        budgets.SelectRows,
		"receipt_characters": budgets.ReceiptCharacters,
		"context_characters": budgets.ContextCharacters,
	} {
		if value <= 0 {
			t.Errorf("%s budget = %d, want positive", name, value)
		}
	}
	if budgets.RouteRows > 12 {
		t.Errorf("route_rows budget = %d, want <= 12", budgets.RouteRows)
	}
	if budgets.CandidateRows > 24 {
		t.Errorf("candidate_rows budget = %d, want <= 24", budgets.CandidateRows)
	}
	if budgets.SelectRows > 10 {
		t.Errorf("select_rows budget = %d, want <= 10", budgets.SelectRows)
	}
	if budgets.ReceiptCharacters > 2_000 {
		t.Errorf("receipt_characters budget = %d, want <= 2000", budgets.ReceiptCharacters)
	}
	if budgets.ContextCharacters > 12_000 {
		t.Errorf("context_characters budget = %d, want <= 12000", budgets.ContextCharacters)
	}
}

func TestSkillLintRejectsUnsafeOrIncompleteContract(t *testing.T) {
	t.Parallel()

	bundle, err := skillcontract.Load(filepath.Join(repositoryRoot(t), "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	bundle.Contract.PhysicalAccess = "allowed"
	bundle.Contract.Workflows = bundle.Contract.Workflows[:len(bundle.Contract.Workflows)-1]
	bundle.Markdown += "\n```sh\nsqlite3 unsafe.db\n```\n"
	err = bundle.Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want lint violations")
	}
	for _, fragment := range []string{"physical_access", "workflow", "sqlite3"} {
		if !strings.Contains(err.Error(), fragment) {
			t.Errorf("lint error %q does not contain %q", err, fragment)
		}
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
