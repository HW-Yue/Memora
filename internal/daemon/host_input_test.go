package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/hostinput"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestNativeDaemonCapturesPendingHostInputWithoutDatabaseWriteAndReloadsAfterRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan State, 1), make(chan error, 1)
		go func() { done <- Run(ctx, dataDir, ready) }()
		<-ready
		return cancel, done
	}
	stop := func(cancel context.CancelFunc, done chan error) {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	cancel, done := start()
	input := hostinput.Input{Version: hostinput.InputVersion, InputID: "input-daemon", Workspace: "project-memora",
		Actor: "agent:host", AuthorizedDatabases: []string{"work"}, CandidateText: "Router returns locators only.",
		Source: hostinput.Source{Kind: history.SourceConversationAssertion, Title: "Router boundary"}}
	receipt, err := CaptureHostInput(context.Background(), dataDir, input)
	if err != nil || receipt.Status != hostinput.StatusPending || receipt.Replayed ||
		strings.Contains(receipt.String(), input.CandidateText) {
		t.Fatalf("CaptureHostInput() = %#v, %v", receipt, err)
	}
	if databases := executeTraceMSQL(t, dataDir, "SHOW DATABASES", nil).Results[0].Rows; len(databases) != 0 {
		t.Fatalf("capture wrote logical Databases = %#v", databases)
	}
	stop(cancel, done)
	cancel, done = start()
	defer stop(cancel, done)
	pending, err := GetHostInput(context.Background(), dataDir, input.InputID, input.Workspace)
	if err != nil || pending.Input.CandidateText != input.CandidateText ||
		pending.Receipt.InputSHA256 != receipt.InputSHA256 {
		t.Fatalf("GetHostInput() = %#v, %v", pending, err)
	}
	replayed, err := CaptureHostInput(context.Background(), dataDir, input)
	if err != nil || !replayed.Replayed || replayed.InputSHA256 != receipt.InputSHA256 {
		t.Fatalf("replayed CaptureHostInput() = %#v, %v", replayed, err)
	}
}

func TestNativeDaemonFinalizesWorthinessAndReloadsDecisionAfterRestart(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	start := func() (context.CancelFunc, chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan State, 1), make(chan error, 1)
		go func() { done <- Run(ctx, dataDir, ready) }()
		<-ready
		return cancel, done
	}
	stop := func(cancel context.CancelFunc, done chan error) {
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	cancel, done := start()
	input := hostinput.Input{Version: hostinput.InputVersion, InputID: "input-decision", Workspace: "project-memora",
		Actor: "agent:host", AuthorizedDatabases: []string{"work"}, CandidateText: "This input is already represented.",
		Source: hostinput.Source{Kind: history.SourceConversationAssertion, Title: "Duplicate candidate"}}
	capture, err := CaptureHostInput(context.Background(), dataDir, input)
	if err != nil {
		t.Fatal(err)
	}
	decision := hostinput.WorthinessDecision{Version: hostinput.WorthinessDecisionVersion,
		DecisionID: "decision-daemon", InputID: input.InputID, Workspace: input.Workspace, Actor: input.Actor,
		InputSHA256: capture.InputSHA256, ScopeSHA256: capture.ScopeSHA256, Verdict: hostinput.VerdictIgnore,
		Reason: "duplicate after preflight", AuthorizedDatabases: []string{"work"}, MutationReceipt: skillwrite.Receipt{
			Version: skillwrite.ReceiptVersion, PlanID: "plan-ignore", Decision: skillwrite.DecisionIgnore,
			Status: skillwrite.ReceiptIgnored, Changes: []skillwrite.Change{}, Ignored: 1, Verified: true,
			Warnings: []result.Notice{}},
	}
	receipt, err := DecideHostInput(context.Background(), dataDir, decision)
	if err != nil || receipt.Status != hostinput.WorthinessFinalized || receipt.Replayed {
		t.Fatalf("DecideHostInput() = %#v, %v", receipt, err)
	}
	if databases := executeTraceMSQL(t, dataDir, "SHOW DATABASES", nil).Results[0].Rows; len(databases) != 0 {
		t.Fatalf("worthiness decision wrote logical Databases = %#v", databases)
	}
	stop(cancel, done)
	cancel, done = start()
	defer stop(cancel, done)
	outcome, err := GetWorthinessDecision(context.Background(), dataDir, decision.DecisionID, decision.Workspace)
	if err != nil || outcome.Decision.Reason != decision.Reason || outcome.Receipt.DecisionSHA256 != receipt.DecisionSHA256 ||
		strings.Contains(string(mustJSON(t, outcome.Receipt)), input.CandidateText) {
		t.Fatalf("GetWorthinessDecision() = %#v, %v", outcome, err)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func TestHostInputHandlerStrictlyRejectsUnknownRawContentField(t *testing.T) {
	t.Parallel()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "auxiliary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	handler := newDatabaseHandler(context.Background(), nil, nil, store)
	t.Cleanup(func() { _ = handler.Close() })
	payload := json.RawMessage(`{"version":"memora.host-input/v1","content":"unbounded raw content"}`)
	_, err = handler.Handle(context.Background(), ipc.Session{ID: "capture-session"}, ipc.Request{
		Version: ipc.Version, RequestID: "capture-invalid", Method: "host_input.capture", Payload: payload,
	})
	if code := stableCode(err); code != string(result.CodeInvalidRequest) {
		t.Fatalf("strict capture error = %v (%s)", err, code)
	}
	events, auditErr := handler.security.Events(context.Background())
	if auditErr != nil || len(events) != 1 || strings.Contains(events[0].String(), "unbounded raw content") {
		t.Fatalf("capture audit = %#v, %v", events, auditErr)
	}
}

func TestWorthinessHandlerStrictlyRejectsCandidateText(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "auxiliary.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	handler := newDatabaseHandler(context.Background(), nil, nil, database)
	t.Cleanup(func() { _ = handler.Close() })
	payload := json.RawMessage(`{"version":"memora.worthiness-decision/v1","candidate_text":"must not pass"}`)
	_, err = handler.Handle(context.Background(), ipc.Session{ID: "decision-session"}, ipc.Request{
		Version: ipc.Version, RequestID: "decision-invalid", Method: "worthiness.decide", Payload: payload,
	})
	if code := stableCode(err); code != string(result.CodeInvalidRequest) {
		t.Fatalf("strict worthiness error = %v (%s)", err, code)
	}
}
