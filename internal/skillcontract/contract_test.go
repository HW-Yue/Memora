package skillcontract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
	"github.com/HW-Yue/Memora/internal/skillconflict"
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
	if got, want := bundle.Contract.RouteMutationProposalVersion, routemutationplan.ProposalVersion; got != want {
		t.Fatalf("Route mutation proposal version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.RouteMutationPlanVersion, routemutationplan.PlanVersion; got != want {
		t.Fatalf("Route mutation plan version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.RouteMutationReceiptVersion, routemutationplan.ReceiptVersion; got != want {
		t.Fatalf("Route mutation receipt version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.ConflictViewVersion, skillconflict.ViewVersion; got != want {
		t.Fatalf("conflict view version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.ConflictResolutionVersion, skillconflict.ResolutionVersion; got != want {
		t.Fatalf("conflict resolution version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.AssimilationEventVersion, assimilation.EventVersion; got != want {
		t.Fatalf("assimilation event version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.AssimilationReceiptVersion, assimilation.ReceiptVersion; got != want {
		t.Fatalf("assimilation receipt version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.AssimilationSubmissionVersion, assimilation.SubmissionVersion; got != want {
		t.Fatalf("assimilation submission version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.AssimilationReviewVersion, assimilation.ReviewVersion; got != want {
		t.Fatalf("assimilation review version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.SourceReceiptVersion, assimilation.SourceReceiptVersion; got != want {
		t.Fatalf("Source Receipt version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.FeedbackEventVersion, feedback.EventVersion; got != want {
		t.Fatalf("feedback event version = %q, want %q", got, want)
	}
	if got, want := bundle.Contract.FeedbackConfirmationVersion, feedback.ConfirmationVersion; got != want {
		t.Fatalf("feedback confirmation version = %q, want %q", got, want)
	}
	if bundle.Contract.BootstrapScript != "scripts/install.sh" || bundle.Contract.InstallConsent != "required" {
		t.Fatalf("bootstrap contract = %q, %q", bundle.Contract.BootstrapScript, bundle.Contract.InstallConsent)
	}
	if _, err := os.Stat(filepath.Join(repositoryRoot(t), "skills", "memora", bundle.Contract.BootstrapScript)); err != nil {
		t.Fatalf("bootstrap script is missing: %v", err)
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
	foundCommands := map[string]bool{"schema": false, "reflect": false, "assimilate": false, "feedback": false}
	for _, command := range bundle.Contract.AllowedCommands {
		if _, tracked := foundCommands[command]; tracked {
			foundCommands[command] = true
		}
	}
	for command, found := range foundCommands {
		if !found {
			t.Errorf("canonical Skill does not allow policy-checked %s command", command)
		}
	}
	for _, token := range []string{"sqlite3 ", "prototype.sqlite", "/databases/", ".wal"} {
		if strings.Contains(bundle.Markdown, token) {
			t.Errorf("canonical Skill contains forbidden physical access token %q", token)
		}
	}
	for _, required := range []string{
		"memora.semantic-conflict/v1", "memora.conflict-resolution/v1",
		"memora.assimilation-submission/v1", "memora.assimilation-review/v1", "memora.source-receipt/v1",
		"RETAIN", "REWRITE", "REMOVE", "in_doubt",
		"memora.feedback-event/v1", "memora.feedback-confirmation/v1", "COMPENSATE",
		"memora.route-mutation-proposal/v1", "memora.route-mutation-plan/v1",
		"memora.route-mutation-receipt/v1", "PLAN ROUTE MUTATION", "APPLY ROUTE MUTATION",
	} {
		if !strings.Contains(bundle.Markdown, required) {
			t.Errorf("canonical Skill omits conflict protocol token %q", required)
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

func TestCanonicalSkillSpeculativeDiscoveryIsBoundedAndFallbackSafe(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	bundle, err := skillcontract.Load(filepath.Join(root, "skills", "memora"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"## Speculative discovery",
		"memora.speculative-discovery/v1",
		"navigation_only",
		"same model turn",
		"ordinary Router root fallback",
		"different topic",
		"Answer only from revision-matched SELECT rows",
	} {
		if !strings.Contains(bundle.Markdown, required) {
			t.Errorf("Canonical Skill omits speculative rule %q", required)
		}
	}
	encoded, err := os.ReadFile(filepath.Join(root, "skills", "memora", "contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var machine struct {
		SpeculativeDiscovery struct {
			Version            string `json:"version"`
			MaxDatabases       int    `json:"max_databases"`
			CandidateRows      int    `json:"candidate_rows"`
			CandidateUTF8Bytes int    `json:"candidate_utf8_bytes"`
			PrefetchTables     int    `json:"prefetch_tables"`
			MaxToolCalls       int    `json:"max_tool_calls"`
		} `json:"speculative_discovery"`
	}
	if err := json.Unmarshal(encoded, &machine); err != nil {
		t.Fatal(err)
	}
	profile := machine.SpeculativeDiscovery
	if profile.Version != "memora.speculative-discovery/v1" || profile.MaxDatabases != 4 ||
		profile.CandidateRows != 8 || profile.CandidateUTF8Bytes != 4096 ||
		profile.PrefetchTables != 2 || profile.MaxToolCalls != 10 {
		t.Fatalf("speculative discovery profile = %#v", profile)
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
