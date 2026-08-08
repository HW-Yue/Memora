package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/adminapi"
	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/hostinput"
	"github.com/HW-Yue/Memora/internal/instancemove"
	"github.com/HW-Yue/Memora/internal/instanceupgrade"
	"github.com/HW-Yue/Memora/internal/launchagent"
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
			wantStderr: "memora: query only accepts SHOW, DESCRIBE, SELECT, OPEN ROUTE, or read-only PLAN statements\n",
		},
		{
			name:       "query rejects recovered mutation after parse failure",
			args:       []string{"query", "SELECT * work.notes; DELETE FROM work.notes WHERE row_id = 'row_1'"},
			wantCode:   2,
			wantStderr: "memora: query only accepts SHOW, DESCRIBE, SELECT, OPEN ROUTE, or read-only PLAN statements\n",
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
			name:       "upgrade requires one operation",
			args:       []string{"upgrade"},
			wantCode:   2,
			wantStderr: "memora: upgrade requires exactly one of --plan or --apply\n",
		},
		{
			name:       "upgrade apply requires approval",
			args:       []string{"upgrade", "--apply"},
			wantCode:   2,
			wantStderr: "memora: upgrade --apply requires --yes after explicit approval\n",
		},
		{
			name:       "doctor repair requires approval",
			args:       []string{"doctor", "repair"},
			wantCode:   2,
			wantStderr: "memora: doctor repair requires --yes after explicit approval\n",
		},
		{
			name:       "assimilate requires one operation",
			args:       []string{"assimilate"},
			wantCode:   2,
			wantStderr: "memora: assimilate requires exactly one of --event, --submission, or --receipt\n",
		},
		{
			name:       "capture requires one operation",
			args:       []string{"capture"},
			wantCode:   2,
			wantStderr: "memora: capture requires exactly one of --candidate or --receipt with --workspace\n",
		},
		{
			name:       "capture rejects unknown candidate field",
			args:       []string{"capture", "--candidate", `{"content":"unsafe"}`},
			wantCode:   2,
			wantStderr: "memora: --candidate must be one strict Host Input JSON object\n",
		},
		{
			name:       "decide requires one operation",
			args:       []string{"decide"},
			wantCode:   2,
			wantStderr: "memora: decide requires exactly one of --decision or --receipt with --workspace\n",
		},
		{
			name:       "decide rejects unknown decision field",
			args:       []string{"decide", "--decision", `{"candidate_text":"unsafe"}`},
			wantCode:   2,
			wantStderr: "memora: --decision must be one strict Worthiness Decision JSON object\n",
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

func TestRunCaptureSubmitsAndReloadsPendingInput(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	input := hostinput.Input{Version: hostinput.InputVersion, InputID: "input-cli", Workspace: "project",
		Actor: "agent:host", AuthorizedDatabases: []string{"work"}, CandidateText: "A bounded candidate",
		Source: hostinput.Source{Kind: history.SourceConversationAssertion, Title: "Candidate"}}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	receipt := hostinput.Receipt{Version: hostinput.ReceiptVersion, InputID: input.InputID, Workspace: input.Workspace,
		Status: hostinput.StatusPending, InputSHA256: "sha256:" + strings.Repeat("a", 64),
		ContentSHA256: "sha256:" + strings.Repeat("b", 64), ScopeSHA256: "sha256:" + strings.Repeat("c", 64),
		ContentBytes: len(input.CandidateText), AuthorizedDatabases: []string{"work"}, CapturedAt: time.Now().UTC()}
	captured, loaded := false, false
	dependencies := Dependencies{HomeDir: func() (string, error) { return home, nil },
		CaptureHostInput: func(_ context.Context, gotDataDir string, got hostinput.Input) (hostinput.Receipt, error) {
			captured = true
			if gotDataDir != dataDir || got.InputID != input.InputID {
				t.Fatalf("capture = %q, %#v", gotDataDir, got)
			}
			return receipt, nil
		},
		GetHostInput: func(_ context.Context, gotDataDir, gotID, gotWorkspace string) (hostinput.Pending, error) {
			loaded = true
			if gotDataDir != dataDir || gotID != input.InputID || gotWorkspace != input.Workspace {
				t.Fatalf("capture lookup = %q, %q, %q", gotDataDir, gotID, gotWorkspace)
			}
			return hostinput.Pending{Input: input, Receipt: receipt}, nil
		},
	}
	for _, args := range [][]string{
		{"capture", "--data-dir", dataDir, "--candidate", string(encoded)},
		{"capture", "--data-dir", dataDir, "--receipt", input.InputID, "--workspace", input.Workspace},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitOK {
			t.Fatalf("capture %v code=%d stderr=%q", args, code, &stderr)
		}
	}
	if !captured || !loaded {
		t.Fatalf("captured=%t loaded=%t", captured, loaded)
	}
}

func TestRunDecideFinalizesAndReloadsWorthinessDecision(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	decision := hostinput.WorthinessDecision{Version: hostinput.WorthinessDecisionVersion,
		DecisionID: "decision-cli", InputID: "input-cli", Workspace: "project", Actor: "agent:host",
		InputSHA256: "sha256:" + strings.Repeat("a", 64), ScopeSHA256: "sha256:" + strings.Repeat("b", 64),
		Verdict: hostinput.VerdictIgnore, Reason: "duplicate", AuthorizedDatabases: []string{"work"},
		MutationReceipt: skillwrite.Receipt{Version: skillwrite.ReceiptVersion, PlanID: "plan-ignore",
			Decision: skillwrite.DecisionIgnore, Status: skillwrite.ReceiptIgnored, Changes: []skillwrite.Change{},
			Ignored: 1, Verified: true, Warnings: []result.Notice{}},
	}
	encoded, err := json.Marshal(decision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := hostinput.WorthinessReceipt{Version: hostinput.WorthinessReceiptVersion,
		DecisionID: decision.DecisionID, InputID: decision.InputID, Workspace: decision.Workspace,
		Status: hostinput.WorthinessFinalized, Verdict: decision.Verdict,
		DecisionSHA256: "sha256:" + strings.Repeat("c", 64), InputSHA256: decision.InputSHA256,
		ScopeSHA256: decision.ScopeSHA256, MutationPlanID: decision.MutationReceipt.PlanID, DecidedAt: time.Now().UTC()}
	decided, loaded := false, false
	dependencies := Dependencies{HomeDir: func() (string, error) { return home, nil },
		DecideHostInput: func(_ context.Context, gotDataDir string, got hostinput.WorthinessDecision) (hostinput.WorthinessReceipt, error) {
			decided = true
			if gotDataDir != dataDir || got.DecisionID != decision.DecisionID {
				t.Fatalf("decision = %q, %#v", gotDataDir, got)
			}
			return receipt, nil
		},
		GetDecision: func(_ context.Context, gotDataDir, gotID, gotWorkspace string) (hostinput.WorthinessResult, error) {
			loaded = true
			if gotDataDir != dataDir || gotID != decision.DecisionID || gotWorkspace != decision.Workspace {
				t.Fatalf("decision lookup = %q, %q, %q", gotDataDir, gotID, gotWorkspace)
			}
			return hostinput.WorthinessResult{Decision: decision, Receipt: receipt}, nil
		},
	}
	for _, args := range [][]string{
		{"decide", "--data-dir", dataDir, "--decision", string(encoded)},
		{"decide", "--data-dir", dataDir, "--receipt", decision.DecisionID, "--workspace", decision.Workspace},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitOK {
			t.Fatalf("decide %v code=%d stderr=%q", args, code, &stderr)
		}
	}
	if !decided || !loaded {
		t.Fatalf("decided=%t loaded=%t", decided, loaded)
	}
}

func TestRunAdminDefaultsToAllDatabases(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(
			context.Context, string, string, []executor.StatementInput,
		) (result.Envelope, error) {
			return result.Envelope{}, errors.New("not called by startup")
		},
		ServeAdmin: func(
			_ context.Context, config adminapi.Config, ready func(adminapi.Descriptor) error,
		) error {
			called = true
			if config.DataDir != dataDir || len(config.Scopes) != 0 || config.Execute == nil {
				t.Fatalf("admin config = %#v", config)
			}
			return ready(adminapi.Descriptor{
				Version:   adminapi.SessionVersion,
				Origin:    "http://127.0.0.1:49152",
				URL:       "http://127.0.0.1:49152/#token=fixture",
				ExpiresAt: time.Date(2026, 8, 1, 0, 15, 0, 0, time.UTC),
			})
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{
		"admin", "--no-open", "--data-dir", dataDir,
	}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || !called || stderr.Len() != 0 {
		t.Fatalf("admin code=%d called=%t stdout=%q stderr=%q", code, called, &stdout, &stderr)
	}
}

func TestRunAdminStartsScopedGatewayAndPrintsDescriptor(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(
			context.Context, string, string, []executor.StatementInput,
		) (result.Envelope, error) {
			return result.Envelope{}, errors.New("not called by startup")
		},
		ServeAdmin: func(
			_ context.Context, config adminapi.Config, ready func(adminapi.Descriptor) error,
		) error {
			called = true
			if config.DataDir != dataDir || len(config.Scopes) != 2 ||
				config.Scopes[0] != "work" || config.Scopes[1] != "personal" || config.Execute == nil {
				t.Fatalf("admin config = %#v", config)
			}
			return ready(adminapi.Descriptor{
				Version:   adminapi.SessionVersion,
				Origin:    "http://127.0.0.1:49152",
				URL:       "http://127.0.0.1:49152/#token=fixture",
				ExpiresAt: time.Date(2026, 8, 1, 0, 15, 0, 0, time.UTC),
			})
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{
		"admin", "--scope", "work", "--scope", "personal", "--no-open", "--data-dir", dataDir,
	}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || !called || stderr.Len() != 0 {
		t.Fatalf("admin code=%d called=%t stdout=%q stderr=%q", code, called, &stdout, &stderr)
	}
	var descriptor adminapi.Descriptor
	if err := json.Unmarshal(stdout.Bytes(), &descriptor); err != nil || descriptor.Version != adminapi.SessionVersion {
		t.Fatalf("descriptor = %#v, %v", descriptor, err)
	}
}

func TestRunAdminOpensBrowserByDefaultAndNoOpenOptsOut(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	opened := []string{}
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
			return result.Envelope{}, nil
		},
		OpenBrowser: func(target string) error {
			opened = append(opened, target)
			return nil
		},
		ServeAdmin: func(_ context.Context, _ adminapi.Config, ready func(adminapi.Descriptor) error) error {
			return ready(adminapi.Descriptor{
				Version:   adminapi.SessionVersion,
				Origin:    "http://127.0.0.1:49152",
				URL:       "http://127.0.0.1:49152/#token=fixture",
				ExpiresAt: time.Now().Add(time.Minute),
			})
		},
	}
	for _, args := range [][]string{
		{"admin", "--scope", "work", "--data-dir", dataDir},
		{"admin", "--scope", "work", "--no-open", "--data-dir", dataDir},
	} {
		var stdout, stderr bytes.Buffer
		if code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitOK {
			t.Fatalf("admin %v code=%d stderr=%q", args, code, &stderr)
		}
	}
	if len(opened) != 1 || opened[0] != "http://127.0.0.1:49152/#token=fixture" {
		t.Fatalf("opened URLs = %#v", opened)
	}
}

func TestServeAdminClosesGatewayWhenBrowserOpenCallbackFails(t *testing.T) {
	t.Parallel()

	address := ""
	err := serveAdmin(context.Background(), adminapi.Config{
		DataDir: "/fixture",
		Scopes:  []string{"work"},
		Execute: func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
			return result.Envelope{}, nil
		},
		Random: bytes.NewReader(bytes.Repeat([]byte{0x66}, 96)),
		Listen: func(network, _ string) (net.Listener, error) {
			return net.Listen(network, "127.0.0.1:0")
		},
		SessionTTL: time.Minute,
	}, func(descriptor adminapi.Descriptor) error {
		address = strings.TrimPrefix(descriptor.Origin, "http://")
		return errors.New("browser unavailable")
	})
	if err == nil || address == "" {
		t.Fatalf("serveAdmin() error=%v address=%q", err, address)
	}
	listener, listenErr := net.Listen("tcp4", address)
	if listenErr != nil {
		t.Fatalf("Gateway port remained open after browser failure: %v", listenErr)
	}
	_ = listener.Close()
}

func TestRunUpgradeAndDoctorRepairUseBoundedOperations(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	backupPath := filepath.Join(home, "backup")
	previewed, applied, repaired := false, false, false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		PreviewUpgrade: func(got string) (instanceupgrade.Plan, error) {
			previewed = true
			if got != dataDir {
				t.Fatalf("preview data dir = %q", got)
			}
			return instanceupgrade.Plan{
				Version: instanceupgrade.PlanVersion, InstanceID: "instance-1",
				FromVersion: 1, ToVersion: 2,
			}, nil
		},
		ApplyUpgrade: func(_ context.Context, got string, options instanceupgrade.Options) (instanceupgrade.Receipt, error) {
			applied = true
			if got != dataDir || !options.Approved {
				t.Fatalf("apply = %q, %#v", got, options)
			}
			return instanceupgrade.Receipt{
				Version: instanceupgrade.ReceiptVersion, Status: "upgraded",
				InstanceID: "instance-1", FromVersion: 1, ToVersion: 2,
			}, nil
		},
		RepairUpgrade: func(_ context.Context, got, backup string, options instanceupgrade.Options) (instanceupgrade.Receipt, error) {
			repaired = true
			if got != dataDir || backup != backupPath || !options.Approved {
				t.Fatalf("repair = %q, %q, %#v", got, backup, options)
			}
			return instanceupgrade.Receipt{
				Version: instanceupgrade.ReceiptVersion, Status: "rolled_back",
				InstanceID: "instance-1", FromVersion: 2, ToVersion: 1,
			}, nil
		},
	}
	for _, args := range [][]string{
		{"upgrade", "--plan", "--data-dir", dataDir},
		{"upgrade", "--apply", "--yes", "--data-dir", dataDir},
		{"doctor", "repair", "--yes", "--backup", backupPath, "--data-dir", dataDir},
	} {
		var stdout, stderr bytes.Buffer
		code := RunWithDependencies(args, &stdout, &stderr, BuildInfo{}, dependencies)
		if code != ExitOK || stderr.Len() != 0 || !json.Valid(stdout.Bytes()) {
			t.Fatalf("%v code=%d stdout=%q stderr=%q", args, code, &stdout, &stderr)
		}
	}
	if !previewed || !applied || !repaired {
		t.Fatalf("previewed=%t applied=%t repaired=%t", previewed, applied, repaired)
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
		Undo: &feedback.Undo{TargetRevision: 1, ExpectedSchemaVersion: 1, RouteLeafIDs: []string{}},
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

func TestRunDatabasePackageCommandsUseMSQLAndSafeFiles(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	packagePath := filepath.Join(home, "work.memora-db")
	var sources []string
	var inputs [][]executor.StatementInput
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(_ context.Context, _ string, source string, statements []executor.StatementInput) (result.Envelope, error) {
			sources = append(sources, source)
			inputs = append(inputs, statements)
			statement := result.NewStatement(0, "PACKAGE", source)
			switch len(sources) {
			case 1:
				statement.Rows = []result.Row{{"package": `{"version":"fixture"}`, "package_sha256": "abc"}}
			case 2:
				statement.Rows = []result.Row{{"name": "work", "read_only": true}}
			default:
				statement.Rows = []result.Row{{"name": "work", "package_sha256": "abc"}}
				statement.AffectedRows = 1
			}
			return result.NewEnvelope("package-cli", statement), nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{
		"pack", `work"quoted`, "--by", "Alice", "--output", packagePath, "--data-dir", dataDir,
	}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || stderr.Len() != 0 {
		t.Fatalf("pack code = %d, stdout = %s, stderr = %s", code, stdout.String(), stderr.String())
	}
	if sources[0] != `PACK DATABASE "work""quoted" BY :author` ||
		inputs[0][0].Parameters.Named["author"] != "Alice" ||
		inputs[0][0].Authorization.Actor != "user:local" ||
		inputs[0][0].Authorization.AuthorizedDatabases[0] != `work"quoted` {
		t.Fatalf("pack MSQL = %q, %#v", sources[0], inputs[0])
	}
	if encoded, err := os.ReadFile(packagePath); err != nil || string(encoded) != `{"version":"fixture"}` {
		t.Fatalf("package file = %q, %v", encoded, err)
	}
	info, err := os.Stat(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("package mode = %v", info.Mode())
	}

	stdout.Reset()
	code = RunWithDependencies([]string{"open", packagePath, "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || sources[1] != "OPEN PACKAGE :package READ ONLY" ||
		inputs[1][0].Parameters.Named["package"] != `{"version":"fixture"}` {
		t.Fatalf("open code = %d, source = %q, input = %#v", code, sources[1], inputs[1])
	}

	stdout.Reset()
	code = RunWithDependencies([]string{"install", packagePath, "--trusted", "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || sources[2] != "INSTALL PACKAGE :package TRUSTED" ||
		inputs[2][0].Authorization.Approval == nil ||
		inputs[2][0].Authorization.Approval.SubjectSHA256 == "" {
		t.Fatalf("install code = %d, source = %q", code, sources[2])
	}
}

func TestRunDatabasePackageRequiresTrustAndDoesNotOverwrite(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	path := filepath.Join(home, "existing.memora-db")
	if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
		t.Fatal(err)
	}
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(_ context.Context, _ string, source string, _ []executor.StatementInput) (result.Envelope, error) {
			statement := result.NewStatement(0, "PACK_DATABASE", source)
			statement.Rows = []result.Row{{"package": "replace", "package_sha256": "hash"}}
			return result.NewEnvelope("package-cli", statement), nil
		},
	}
	var stdout, stderr bytes.Buffer
	if code := RunWithDependencies([]string{"install", path, "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitUsage {
		t.Fatalf("untrusted install code = %d", code)
	}
	stderr.Reset()
	if code := RunWithDependencies([]string{"pack", "work", "--by", "Alice", "-o", path, "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{}, dependencies); code != ExitFailure {
		t.Fatalf("overwrite pack code = %d", code)
	}
	if encoded, err := os.ReadFile(path); err != nil || string(encoded) != "preserve" {
		t.Fatalf("existing package = %q, %v", encoded, err)
	}
}

func TestRunWikiExportUsesMSQLWithBoundProfile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	dataDir := filepath.Join(home, "instance")
	vault := filepath.Join(home, "vault")
	profilePath := filepath.Join(home, "profile.json")
	profile := `{"version":"memora.wiki-profile/v1","tables":{}}`
	if err := os.WriteFile(profilePath, []byte(profile), 0o600); err != nil {
		t.Fatal(err)
	}
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(_ context.Context, gotDataDir, source string, statements []executor.StatementInput) (result.Envelope, error) {
			called = true
			if gotDataDir != dataDir || source != "EXPORT WIKI TO :path PROFILE :profile" ||
				statements[0].Parameters.Named["path"] != vault || statements[0].Parameters.Named["profile"] != profile ||
				len(statements[0].Authorization.AuthorizedDatabases) != 1 ||
				statements[0].Authorization.AuthorizedDatabases[0] != "work" {
				t.Fatalf("export call = %q, %q, %#v", gotDataDir, source, statements)
			}
			statement := result.NewStatement(0, "EXPORT_WIKI", source)
			statement.Rows = []result.Row{{"path": vault, "object_count": int64(2)}}
			return result.NewEnvelope("wiki-cli", statement), nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{
		"export", "--wiki", vault, "--profile", profilePath, "--database", "work", "--data-dir", dataDir,
	}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK || !called || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"object_count":2`) {
		t.Fatalf("export code = %d, called = %v, stdout = %s, stderr = %s", code, called, stdout.String(), stderr.String())
	}
}

func TestExternalWritesRejectSymlinkDirectoriesBeforeMSQL(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	target := t.TempDir()
	link := filepath.Join(home, "linked-output")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	called := false
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return home, nil },
		ExecuteMSQL: func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
			called = true
			return result.Envelope{}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{
		"pack", "work", "--by", "Alice", "--output", filepath.Join(link, "work.memora-db"),
	}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitUsage || called {
		t.Fatalf("pack code = %d, called = %v, stderr = %s", code, called, &stderr)
	}
}

func TestCommandErrorsRedactSecrets(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	code := commandError(&stderr, "test", errors.New(
		"OPENAI_API_KEY=sk-private Authorization: Bearer bearer-private token=token-private",
	))
	if code != ExitFailure {
		t.Fatalf("commandError() = %d", code)
	}
	for _, secret := range []string{"sk-private", "bearer-private", "token-private"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("stderr leaked %q: %s", secret, &stderr)
		}
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

func TestRunInstanceMoveUsesExplicitPathsAndEmitsReceipt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	source := filepath.Join(root, "source")
	backup := filepath.Join(root, "backup")
	target := filepath.Join(root, "target")
	var gotSource, gotBackup, gotTarget, gotExecutable string
	dependencies := Dependencies{
		HomeDir:    func() (string, error) { return root, nil },
		Executable: func() (string, error) { return "/opt/memora", nil },
		MoveInstance: func(_ context.Context, source, backup, target string, options instancemove.Options) (instancemove.Receipt, error) {
			gotSource, gotBackup, gotTarget, gotExecutable = source, backup, target, options.Executable
			return instancemove.Receipt{Version: instancemove.ReceiptVersion, Status: "moved", SourceRetained: true}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{"move", "--data-dir", source, "--backup", backup, "--target", target}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK {
		t.Fatalf("move code = %d, stderr = %s", code, &stderr)
	}
	if gotSource != source || gotBackup != backup || gotTarget != target || gotExecutable != "/opt/memora" {
		t.Fatalf("move args = %q, %q, %q, %q", gotSource, gotBackup, gotTarget, gotExecutable)
	}
	var receipt instancemove.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || !receipt.SourceRetained {
		t.Fatalf("receipt = %#v, error = %v", receipt, err)
	}
}

func TestRunMCPServesOnlyProtocolMessagesOnStdout(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataDir := filepath.Join(root, "instance")
	input := `{"jsonrpc":"2.0","id":"list","method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}}` + "\n"
	dependencies := Dependencies{
		HomeDir: func() (string, error) { return root, nil },
		Stdin:   strings.NewReader(input),
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{"mcp", "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{Version: "0.1-test"}, dependencies)
	if code != ExitOK || stderr.Len() != 0 {
		t.Fatalf("mcp code = %d, stderr = %q", code, &stderr)
	}
	var response map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &response); err != nil {
		t.Fatalf("stdout is not one JSON-RPC response: %v: %q", err, &stdout)
	}
	if response["jsonrpc"] != "2.0" || response["result"] == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestRunServiceInstallUsesCurrentUserAndExplicitInstance(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	dataDir := filepath.Join(root, "instance")
	executable := filepath.Join(root, "memora")
	var gotAction string
	var gotConfig launchagent.Config
	dependencies := Dependencies{
		HomeDir:    func() (string, error) { return root, nil },
		UserID:     func() int { return 502 },
		Executable: func() (string, error) { return executable, nil },
		ManageLaunchAgent: func(_ context.Context, action string, config launchagent.Config) (launchagent.Receipt, error) {
			gotAction, gotConfig = action, config
			return launchagent.Receipt{Version: launchagent.ReceiptVersion, Status: "installed", Installed: true, Loaded: true}, nil
		},
	}
	var stdout, stderr bytes.Buffer
	code := RunWithDependencies([]string{"service", "install", "--data-dir", dataDir}, &stdout, &stderr, BuildInfo{}, dependencies)
	if code != ExitOK {
		t.Fatalf("service code = %d, stderr = %s", code, &stderr)
	}
	if gotAction != "install" || gotConfig.HomeDir != root || gotConfig.DataDir != dataDir || gotConfig.Executable != executable || gotConfig.UID != 502 {
		t.Fatalf("service call = %q, %#v", gotAction, gotConfig)
	}
	var receipt launchagent.Receipt
	if err := json.Unmarshal(stdout.Bytes(), &receipt); err != nil || !receipt.Loaded {
		t.Fatalf("receipt = %#v, error = %v", receipt, err)
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
