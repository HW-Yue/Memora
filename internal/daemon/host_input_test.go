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
