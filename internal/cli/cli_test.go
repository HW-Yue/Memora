package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillschema"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestRun(t *testing.T) {
	t.Parallel()

	build := BuildInfo{
		Version: "0.1.0-test",
		Commit:  "abc1234",
		BuiltAt: "2026-07-29T00:00:00Z",
	}

	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{
			name:       "help",
			args:       []string{"help"},
			wantCode:   0,
			wantStdout: golden(t, "help.golden"),
		},
		{
			name:       "no arguments shows help",
			wantCode:   0,
			wantStdout: golden(t, "help.golden"),
		},
		{
			name:       "JSON version",
			args:       []string{"version", "--json"},
			wantCode:   0,
			wantStdout: golden(t, "version-json.golden"),
		},
		{
			name:       "text version",
			args:       []string{"version"},
			wantCode:   0,
			wantStdout: "memora 0.1.0-test (abc1234)\n",
		},
		{
			name:       "unknown command",
			args:       []string{"wat"},
			wantCode:   2,
			wantStderr: golden(t, "unknown-command.golden"),
		},
		{
			name:       "invalid version option",
			args:       []string{"version", "--yaml"},
			wantCode:   2,
			wantStderr: "memora: unknown option for version: \"--yaml\"\n",
		},
		{
			name:       "exec requires source",
			args:       []string{"exec"},
			wantCode:   2,
			wantStderr: "memora: exec requires an MSQL source argument\n",
		},
		{
			name:       "query rejects malformed input",
			args:       []string{"query", "--input", `{"unknown":true}`, "SELECT * FROM work.notes LIMIT 1"},
			wantCode:   2,
			wantStderr: "memora: --input must be one strict StatementInput JSON object\n",
		},
		{
			name:       "query rejects mutation",
			args:       []string{"query", "UPDATE work.notes SET title = 'unsafe'"},
			wantCode:   2,
			wantStderr: "memora: query only accepts SHOW, DESCRIBE, SELECT, MATCH, or OPEN ROUTE\n",
		},
		{
			name:       "mutate requires plan",
			args:       []string{"mutate"},
			wantCode:   2,
			wantStderr: "memora: mutate requires --plan JSON\n",
		},
		{
			name:       "mutate rejects unknown plan field",
			args:       []string{"mutate", "--plan", `{"surprise":true}`},
			wantCode:   2,
			wantStderr: "memora: --plan must be one strict Mutation Plan JSON object\n",
		},
		{
			name:       "schema requires plan",
			args:       []string{"schema"},
			wantCode:   2,
			wantStderr: "memora: schema requires --plan JSON\n",
		},
		{
			name:       "schema rejects unknown plan field",
			args:       []string{"schema", "--plan", `{"surprise":true}`},
			wantCode:   2,
			wantStderr: "memora: --plan must be one strict Schema Plan JSON object\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer

			gotCode := Run(tt.args, &stdout, &stderr, build)
			if gotCode != tt.wantCode {
				t.Fatalf("Run() code = %d, want %d", gotCode, tt.wantCode)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Errorf("stdout mismatch\n--- got ---\n%s--- want ---\n%s", got, tt.wantStdout)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Errorf("stderr mismatch\n--- got ---\n%s--- want ---\n%s", got, tt.wantStderr)
			}
		})
	}
}

func TestRunSchemaExecutesValidatedPlanAndPrintsReceipt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	plan := skillschema.Plan{
		Version: skillschema.PlanVersion, ID: "schema-cli", Actor: "agent:test",
		SourceEventID: "conversation:event-schema", Reason: "reuse existing schema",
		AuthorizedDatabases: []string{"work"},
		Ensure: &skillschema.EnsurePlan{
			Database: catalog.DatabaseDefinition{Name: "work", Purpose: "Work", Scope: "Projects"},
			Table: catalog.TableDefinition{
				Name: "notes", Purpose: "Notes", RowSemantics: "One note",
				Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT", Purpose: "Title"}},
			},
		},
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(_ context.Context, gotDataDir, source string, _ []executor.StatementInput) (result.Envelope, error) {
			invocations++
			if gotDataDir != dataDir {
				t.Fatalf("data dir = %q", gotDataDir)
			}
			statement := result.NewStatement(0, "SHOW", source)
			switch source {
			case "SHOW DATABASES COMPACT":
				statement.Rows = []result.Row{{"database_id": "db_work", "name": "work", "aliases": []any{}}}
			case "SHOW TABLES FROM `work` COMPACT":
				statement.Rows = []result.Row{{"table_id": "tbl_notes", "name": "notes", "aliases": []any{}}}
			default:
				t.Fatalf("unexpected schema MSQL %q", source)
			}
			return result.NewEnvelope("schema-cli", statement), nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"schema", "--data-dir", dataDir, "--plan", string(encodedPlan)},
		&stdout, &stderr, BuildInfo{}, dependencies,
	)
	if code != ExitOK || stderr.Len() != 0 || invocations != 2 {
		t.Fatalf("schema code = %d, stderr = %q, invocations = %d", code, &stderr, invocations)
	}
	var receipt skillschema.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != skillschema.ReceiptApplied ||
		receipt.Database.Action != skillschema.ObjectReused || receipt.Table.Action != skillschema.ObjectReused {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRunMutateExecutesValidatedPlanAndPrintsReceipt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	rows := 1
	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: "plan-cli", Decision: skillwrite.DecisionIgnore,
		Database: "work", Table: "notes", Actor: "agent:test",
		SourceEventID: "conversation:event-1", Reason: "duplicate",
		AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "existing", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: &rows,
		}},
		Steps: []skillwrite.Step{}, Verify: []skillwrite.Check{},
	}
	encodedPlan, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	invocations := 0
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(
			_ context.Context,
			gotDataDir, source string,
			_ []executor.StatementInput,
		) (result.Envelope, error) {
			invocations++
			if gotDataDir != dataDir || source != plan.Preflight[0].MSQL {
				t.Fatalf("execute = %q, %q", gotDataDir, source)
			}
			statement := result.NewStatement(0, "SELECT", source)
			statement.Rows = []result.Row{{"row_id": "row_one"}}
			return result.NewEnvelope("plan-cli-existing", statement), nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"mutate", "--data-dir", dataDir, "--plan", string(encodedPlan)},
		&stdout,
		&stderr,
		BuildInfo{},
		dependencies,
	)
	if code != ExitOK || stderr.Len() != 0 || invocations != 1 {
		t.Fatalf("mutate code = %d, stderr = %q, invocations = %d", code, &stderr, invocations)
	}
	var receipt skillwrite.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != skillwrite.ReceiptIgnored || !receipt.Verified || receipt.Ignored != 1 {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestRunInit(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "custom-instance")
	createdAt := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		LookupEnv: func(string) (string, bool) {
			return "", false
		},
		Clock: testkit.NewFakeClock(createdAt),
		IDs:   testkit.NewFakeIDs("018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"),
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
		BuildInfo{},
		dependencies,
	)
	if code != ExitOK {
		t.Fatalf("first init code = %d, stderr = %s", code, &stderr)
	}
	want := fmt.Sprintf("Initialized Memora instance 018f2f7e-7b5d-7c31-8a29-53f27d8f93c1 at %s\n", dataDir)
	if got := stdout.String(); got != want {
		t.Fatalf("first init stdout = %q, want %q", got, want)
	}

	stdout.Reset()
	stderr.Reset()
	code = RunWithDependencies(
		[]string{"init", "--data-dir", dataDir},
		&stdout,
		&stderr,
		BuildInfo{},
		dependencies,
	)
	if code != ExitOK {
		t.Fatalf("second init code = %d, stderr = %s", code, &stderr)
	}
	want = fmt.Sprintf("Memora instance 018f2f7e-7b5d-7c31-8a29-53f27d8f93c1 already initialized at %s\n", dataDir)
	if got := stdout.String(); got != want {
		t.Fatalf("second init stdout = %q, want %q", got, want)
	}
}

func TestRunInitUsesMemoraEnvironmentWithoutLeakingHostSecrets(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "environment-instance")
	values := map[string]string{
		"MEMORA_DATA_DIR":   dataDir,
		"MEMORA_INSTANCE":   "environment",
		"MEMORA_LOG_LEVEL":  "debug",
		"OPENAI_API_KEY":    "must-not-leak-openai",
		"ANTHROPIC_API_KEY": "must-not-leak-anthropic",
	}
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		LookupEnv: func(name string) (string, bool) {
			value, ok := values[name]
			return value, ok
		},
		Clock: testkit.NewFakeClock(time.Now()),
		IDs:   testkit.NewFakeIDs("018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"),
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies([]string{"init"}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK {
		t.Fatalf("init code = %d, stderr = %s", code, &stderr)
	}
	if !strings.Contains(stdout.String(), dataDir) {
		t.Fatalf("stdout = %q, want data dir %q", &stdout, dataDir)
	}
	err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, secret := range []string{"must-not-leak-openai", "must-not-leak-anthropic"} {
			if bytes.Contains(content, []byte(secret)) {
				t.Fatalf("file %q leaked host secret", path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
}

func TestRunInitRejectsRelativeDataDir(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"init", "--data-dir", "relative/path"},
		&stdout,
		&stderr,
		BuildInfo{},
		Dependencies{HomeDir: os.UserHomeDir},
	)
	if code != ExitUsage {
		t.Fatalf("init code = %d, want %d", code, ExitUsage)
	}
	if got, want := stderr.String(), "memora: --data-dir must be an absolute path\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func golden(t *testing.T, name string) string {
	t.Helper()

	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read golden file %q: %v", name, err)
	}
	return string(content)
}
