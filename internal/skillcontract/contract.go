package skillcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillconflict"
)

const (
	Version                       = "memora.skill/v1"
	PhysicalAccessForbidden       = "forbidden"
	ConflictAskUserBeforeMutation = "ask_user_before_mutation"
)

var requiredWorkflows = []string{
	"discover",
	"query",
	"summarize",
	"write",
	"assimilate",
	"conflict",
	"request_user",
	"receipt",
	"reflect",
}

var forbiddenMarkdownTokens = []string{
	"sqlite3 ",
	"prototype.sqlite",
	"/databases/",
	".wal",
}

type Budgets struct {
	RouteRows         int `json:"route_rows"`
	CandidateRows     int `json:"candidate_rows"`
	SelectRows        int `json:"select_rows"`
	ReceiptCharacters int `json:"receipt_characters"`
	ContextCharacters int `json:"context_characters"`
}

type Example struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	MSQL    string `json:"msql,omitempty"`
}

type Contract struct {
	Version                    string    `json:"version"`
	Source                     string    `json:"source"`
	MSQLASTVersion             string    `json:"msql_ast_version"`
	ResultVersion              string    `json:"result_version"`
	ConflictViewVersion        string    `json:"conflict_view_version"`
	ConflictResolutionVersion  string    `json:"conflict_resolution_version"`
	AssimilationEventVersion   string    `json:"assimilation_event_version"`
	AssimilationReceiptVersion string    `json:"assimilation_receipt_version"`
	AllowedCommands            []string  `json:"allowed_commands"`
	PhysicalAccess             string    `json:"physical_access"`
	ConflictPolicy             string    `json:"conflict_policy"`
	Workflows                  []string  `json:"workflows"`
	Budgets                    Budgets   `json:"budgets"`
	Examples                   []Example `json:"examples"`
}

type Bundle struct {
	Markdown string
	Contract Contract
}

type Error struct {
	Code       result.Code
	Violations []string
}

func (err *Error) Error() string {
	return fmt.Sprintf("canonical Skill lint failed: %s", strings.Join(err.Violations, "; "))
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func Load(directory string) (Bundle, error) {
	contractPath := filepath.Join(directory, "contract.json")
	contractBytes, err := os.ReadFile(contractPath)
	if err != nil {
		return Bundle{}, loadError("read contract.json", err)
	}
	var contract Contract
	decoder := json.NewDecoder(bytes.NewReader(contractBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&contract); err != nil {
		return Bundle{}, loadError("decode contract.json", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return Bundle{}, loadError("decode contract.json", err)
	}
	markdown, err := os.ReadFile(filepath.Join(directory, contract.Source))
	if err != nil {
		return Bundle{}, loadError("read canonical Skill source", err)
	}
	return Bundle{Markdown: string(markdown), Contract: contract}, nil
}

func (bundle Bundle) Validate() error {
	var violations []string
	contract := bundle.Contract
	requireEqual := func(name, got, want string) {
		if got != want {
			violations = append(violations, fmt.Sprintf("%s must be %q, got %q", name, want, got))
		}
	}
	requireEqual("version", contract.Version, Version)
	requireEqual("source", contract.Source, "SKILL.md")
	requireEqual("msql_ast_version", contract.MSQLASTVersion, ast.Version)
	requireEqual("result_version", contract.ResultVersion, result.Version)
	requireEqual("conflict_view_version", contract.ConflictViewVersion, skillconflict.ViewVersion)
	requireEqual("conflict_resolution_version", contract.ConflictResolutionVersion, skillconflict.ResolutionVersion)
	requireEqual("assimilation_event_version", contract.AssimilationEventVersion, assimilation.EventVersion)
	requireEqual("assimilation_receipt_version", contract.AssimilationReceiptVersion, assimilation.ReceiptVersion)
	requireEqual("physical_access", contract.PhysicalAccess, PhysicalAccessForbidden)
	requireEqual("conflict_policy", contract.ConflictPolicy, ConflictAskUserBeforeMutation)

	violations = append(violations, validateCommands(contract.AllowedCommands)...)
	violations = append(violations, validateWorkflows(contract.Workflows)...)
	violations = append(violations, validateBudgets(contract.Budgets)...)
	violations = append(violations, validateMarkdown(bundle.Markdown)...)
	violations = append(violations, validateExamples(bundle.Markdown, contract)...)
	if len(violations) == 0 {
		return nil
	}
	sort.Strings(violations)
	return &Error{Code: result.CodeValidation, Violations: violations}
}

func (bundle Bundle) RenderCommandExamples() string {
	var rendered strings.Builder
	for _, example := range bundle.Contract.Examples {
		fmt.Fprintf(&rendered, "%s\t%s\n", example.Name, example.Command)
	}
	return rendered.String()
}

func validateCommands(commands []string) []string {
	want := map[string]bool{
		"assimilate": false, "doctor": false, "query": false, "exec": false,
		"mutate": false, "schema": false, "reflect": false,
	}
	var violations []string
	for _, command := range commands {
		if _, ok := want[command]; !ok {
			violations = append(violations, fmt.Sprintf("allowed_commands contains unsupported command %q", command))
			continue
		}
		want[command] = true
	}
	for command, found := range want {
		if !found {
			violations = append(violations, fmt.Sprintf("allowed_commands is missing %q", command))
		}
	}
	return violations
}

func validateWorkflows(workflows []string) []string {
	found := make(map[string]bool, len(workflows))
	for _, workflow := range workflows {
		found[workflow] = true
	}
	var violations []string
	for _, workflow := range requiredWorkflows {
		if !found[workflow] {
			violations = append(violations, fmt.Sprintf("workflow %q is missing", workflow))
		}
	}
	return violations
}

func validateBudgets(budgets Budgets) []string {
	limits := []struct {
		name  string
		value int
		max   int
	}{
		{"route_rows", budgets.RouteRows, 12},
		{"candidate_rows", budgets.CandidateRows, 24},
		{"select_rows", budgets.SelectRows, 10},
		{"receipt_characters", budgets.ReceiptCharacters, 2_000},
		{"context_characters", budgets.ContextCharacters, 12_000},
	}
	var violations []string
	for _, limit := range limits {
		if limit.value <= 0 || limit.value > limit.max {
			violations = append(violations, fmt.Sprintf(
				"budget %s must be between 1 and %d, got %d",
				limit.name,
				limit.max,
				limit.value,
			))
		}
	}
	return violations
}

func validateMarkdown(markdown string) []string {
	var violations []string
	for _, heading := range []string{
		"# Memora Canonical Skill",
		"## Discover",
		"## Query and summarize",
		"## Write",
		"## Assimilate sources",
		"## Request the user",
		"## Return a receipt",
	} {
		if !strings.Contains(markdown, heading) {
			violations = append(violations, fmt.Sprintf("Skill source is missing heading %q", heading))
		}
	}
	for _, token := range forbiddenMarkdownTokens {
		if strings.Contains(markdown, token) {
			violations = append(violations, fmt.Sprintf("Skill source contains forbidden physical access token %q", token))
		}
	}
	return violations
}

func validateExamples(markdown string, contract Contract) []string {
	allowed := make(map[string]bool, len(contract.AllowedCommands))
	for _, command := range contract.AllowedCommands {
		allowed["memora "+command] = true
	}
	var violations []string
	names := make(map[string]bool, len(contract.Examples))
	for _, example := range contract.Examples {
		if example.Name == "" {
			violations = append(violations, "example name is empty")
		} else if names[example.Name] {
			violations = append(violations, fmt.Sprintf("example name %q is duplicated", example.Name))
		}
		names[example.Name] = true
		if !allowedCommand(example.Command, allowed) {
			violations = append(violations, fmt.Sprintf("example %q uses a command outside allowed_commands", example.Name))
		}
		if !strings.Contains(markdown, example.Command) {
			violations = append(violations, fmt.Sprintf("example %q is absent from SKILL.md", example.Name))
		}
		if example.MSQL != "" {
			if _, err := parser.ParseBatch(example.MSQL); err != nil {
				violations = append(violations, fmt.Sprintf("example %q has invalid MSQL: %v", example.Name, err))
			}
			if !strings.Contains(example.Command, example.MSQL) {
				violations = append(violations, fmt.Sprintf("example %q command does not contain its MSQL", example.Name))
			}
		}
	}
	return violations
}

func allowedCommand(command string, allowed map[string]bool) bool {
	fields := strings.Fields(command)
	if len(fields) < 2 {
		return false
	}
	return allowed[fields[0]+" "+fields[1]]
}

func loadError(operation string, err error) error {
	return &Error{
		Code:       result.CodeInternal,
		Violations: []string{fmt.Sprintf("%s: %v", operation, err)},
	}
}
