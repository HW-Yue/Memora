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

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/feedback"
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
			name:       "maintain requires one operation",
			args:       []string{"maintain"},
			wantCode:   2,
			wantStderr: "memora: maintain requires exactly one of --report or --request\n",
		},
		{
			name:       "maintain rejects unknown request field",
			args:       []string{"maintain", "--request", `{"content":"unsafe"}`},
			wantCode:   2,
			wantStderr: "memora: --request must be one strict Maintenance Request JSON object\n",
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
		{
			name:       "assimilate requires one operation",
			args:       []string{"assimilate"},
			wantCode:   2,
			wantStderr: "memora: assimilate requires exactly one of --event, --submission, or --receipt\n",
		},
		{
			name:       "assimilate rejects raw content field",
			args:       []string{"assimilate", "--event", `{"content":"raw source text"}`},
			wantCode:   2,
			wantStderr: "memora: --event must be one strict Assimilation Event JSON object\n",
		},
		{
			name:       "assimilate rejects raw submission field",
			args:       []string{"assimilate", "--submission", `{"content":"raw source text"}`},
			wantCode:   2,
			wantStderr: "memora: --submission must be one strict Assimilation Submission JSON object\n",
		},
		{
			name:       "assimilate operations are mutually exclusive",
			args:       []string{"assimilate", "--event", `{}`, "--receipt", "submission-1"},
			wantCode:   2,
			wantStderr: "memora: assimilate requires exactly one of --event, --submission, or --receipt\n",
		},
		{
			name:       "feedback requires one operation",
			args:       []string{"feedback"},
			wantCode:   2,
			wantStderr: "memora: feedback requires exactly one of --event or --confirmation\n",
		},
		{
			name:       "feedback rejects unknown event field",
			args:       []string{"feedback", "--event", `{"content":"unsafe"}`},
			wantCode:   2,
			wantStderr: "memora: --event must be one strict Feedback Event JSON object\n",
		},
		{
			name:       "reflect requires event",
			args:       []string{"reflect"},
			wantCode:   2,
			wantStderr: "memora: reflect requires --event JSON\n",
		},
		{
			name:       "reflect rejects unknown event field",
			args:       []string{"reflect", "--event", `{"surprise":true}`},
			wantCode:   2,
			wantStderr: "memora: --event must be one strict Conversation Event JSON object\n",
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

func TestRunFeedbackPassesEventAndConfirmationToDaemon(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	event := feedback.Event{
		Version: feedback.EventVersion, EventID: "feedback-cli", Kind: feedback.KindWrong,
		Actor: "agent:test", Reason: "wrong", Target: feedback.Target{Database: "work", Table: "notes", RowID: "row-1", Revision: 2},
	}
	confirmation := feedback.Confirmation{
		Version: feedback.ConfirmationVersion, ConfirmationID: "confirm-cli", FeedbackEventID: event.EventID,
		SourceEventID: "user-confirm-cli", Actor: "agent:test", Instruction: "restore", Action: feedback.ActionUndo,
		ExpectedRevision: 2, AuthorizedDatabases: []string{"work"},
		Undo: &feedback.Undo{TargetRevision: 1, ExpectedSchemaVersion: 1, IndexTerms: []string{}, RouteLeafIDs: []string{}},
	}
	eventJSON, _ := json.Marshal(event)
	confirmationJSON, _ := json.Marshal(confirmation)
	recorded, confirmed := false, false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		RecordFeedback: func(_ context.Context, gotDataDir string, got feedback.Event) (feedback.Receipt, error) {
			recorded = true
			if gotDataDir != dataDir || got.EventID != event.EventID {
				t.Fatalf("feedback event = %q, %#v", gotDataDir, got)
			}
			return feedback.Receipt{Version: feedback.ReceiptVersion, EventID: got.EventID, Kind: got.Kind, Status: "recorded", Target: got.Target}, nil
		},
		ConfirmFeedback: func(_ context.Context, gotDataDir string, got feedback.Confirmation) (feedback.ConfirmationReceipt, error) {
			confirmed = true
			if gotDataDir != dataDir || got.ConfirmationID != confirmation.ConfirmationID {
				t.Fatalf("feedback confirmation = %q, %#v", gotDataDir, got)
			}
			return feedback.ConfirmationReceipt{
				Version: feedback.ConfirmationReceiptVersion, ConfirmationID: got.ConfirmationID,
				FeedbackEventID: got.FeedbackEventID, Action: got.Action, Status: "confirmed", Verified: true,
				Target: event.Target, Warnings: []result.Notice{},
			}, nil
		},
	}
	for _, args := range [][]string{
		{"feedback", "--data-dir", dataDir, "--event", string(eventJSON)},
		{"feedback", "--data-dir", dataDir, "--confirmation", string(confirmationJSON)},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitOK {
			t.Fatalf("feedback %v code=%d stderr=%q", args, code, &stderr)
		}
	}
	if !recorded || !confirmed {
		t.Fatalf("recorded=%t confirmed=%t", recorded, confirmed)
	}
}

func TestRunAssimilateSubmitsReviewAndReloadsSourceReceipt(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	submission := assimilation.Submission{
		Version: assimilation.SubmissionVersion, SubmissionID: "submit-cli", TaskID: "book-task",
		Workspace: "project", CoverageRevision: 3, Author: "agent:host",
		DraftContextID: "draft", DraftDigest: "sha256:" + strings.Repeat("a", 64),
	}
	encoded, err := json.Marshal(submission)
	if err != nil {
		t.Fatal(err)
	}
	receipt := assimilation.SourceReceipt{
		Version: assimilation.SourceReceiptVersion, SubmissionID: submission.SubmissionID,
		TaskID: submission.TaskID, Status: assimilation.SubmissionCommitted,
		Impacts: []assimilation.SourceImpact{}, KeyFacts: []assimilation.KeyFactReceipt{}, Warnings: []result.Notice{},
	}
	submitted, loaded := false, false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		SubmitAssimilation: func(_ context.Context, gotDataDir string, got assimilation.Submission) (assimilation.SourceReceipt, error) {
			submitted = true
			if gotDataDir != dataDir || got.SubmissionID != submission.SubmissionID {
				t.Fatalf("submission = %q, %#v", gotDataDir, got)
			}
			return receipt, nil
		},
		GetSourceReceipt: func(_ context.Context, gotDataDir, gotID string) (assimilation.SourceReceipt, error) {
			loaded = true
			if gotDataDir != dataDir || gotID != submission.SubmissionID {
				t.Fatalf("receipt lookup = %q, %q", gotDataDir, gotID)
			}
			return receipt, nil
		},
	}
	for _, args := range [][]string{
		{"assimilate", "--data-dir", dataDir, "--submission", string(encoded)},
		{"assimilate", "--data-dir", dataDir, "--receipt", submission.SubmissionID},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitOK {
			t.Fatalf("assimilate %v code=%d stderr=%q", args, code, &stderr)
		}
		var got assimilation.SourceReceipt
		if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.SubmissionID != submission.SubmissionID {
			t.Fatalf("Source Receipt = %#v, %v", got, err)
		}
	}
	if !submitted || !loaded {
		t.Fatalf("submitted=%t loaded=%t", submitted, loaded)
	}
}

func TestRunAssimilateFailsWhenRequiredRangesRemainUnread(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	event := assimilation.Event{
		Version: assimilation.EventVersion, EventID: "finish-cli", TaskID: "book-task",
		Workspace: "project", Kind: assimilation.KindFinish, ExpectedRevision: 3,
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		Assimilate: func(_ context.Context, gotDataDir string, got assimilation.Event) (assimilation.Receipt, error) {
			called = true
			if gotDataDir != dataDir || got.EventID != event.EventID {
				t.Fatalf("assimilate = %q, %#v", gotDataDir, got)
			}
			return assimilation.Receipt{
				Version: assimilation.ReceiptVersion, EventID: got.EventID, TaskID: got.TaskID,
				Status: assimilation.StatusIncomplete, Revision: 3,
				UnreadCount: 1, Unread: []assimilation.UnreadRange{{UnitID: "chapter", Start: 2, End: 4}},
			}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"assimilate", "--data-dir", dataDir, "--event", string(encoded)},
		&stdout, &stderr, BuildInfo{}, dependencies,
	)
	if code != ExitFailure || !called || stderr.Len() != 0 {
		t.Fatalf("assimilate code=%d called=%t stderr=%q", code, called, &stderr)
	}
	var receipt assimilation.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Status != assimilation.StatusIncomplete {
		t.Fatalf("receipt = %#v, %v", receipt, err)
	}
}

func TestRunReflectPassesExplicitEventToDaemon(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	event := conversation.Event{
		Version: conversation.EventVersion, EventID: "checkpoint-cli", SessionID: "session-cli",
		Kind: conversation.KindCheckpoint, Workspace: "repo", AuthorizedDatabases: []string{"work"},
		Checkpoint: &conversation.Checkpoint{ActiveDatabase: "work", LastEventID: "event-before"},
	}
	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		Reflect: func(_ context.Context, gotDataDir string, got conversation.Event) (conversation.Receipt, error) {
			called = true
			if gotDataDir != dataDir || got.EventID != event.EventID {
				t.Fatalf("reflect = %q, %#v", gotDataDir, got)
			}
			return conversation.Receipt{
				Version: conversation.ReceiptVersion, EventID: got.EventID, SessionID: got.SessionID,
				Status: conversation.StatusCheckpointed, Mutations: []skillwrite.Receipt{}, Missing: []string{},
			}, nil
		},
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := RunWithDependencies(
		[]string{"reflect", "--data-dir", dataDir, "--event", string(encoded)},
		&stdout, &stderr, BuildInfo{}, dependencies,
	)
	if code != ExitOK || !called || stderr.Len() != 0 {
		t.Fatalf("reflect code=%d called=%t stderr=%q", code, called, &stderr)
	}
	var receipt conversation.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || receipt.Status != conversation.StatusCheckpointed {
		t.Fatalf("receipt = %#v, %v", receipt, err)
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
