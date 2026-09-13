package hostinput_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/hostinput"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestWorthinessDecisionFinalizesPendingAndReplaysAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host-input.db")
	database, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 16, 0, 0, 0, time.UTC)
	service := hostinput.New(database, hostinput.Options{Clock: fixedInputClock{value: now}, MaximumPending: 1})
	candidate := inputFixture()
	capture, err := service.Capture(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	decision := ignoreDecision(capture)
	receipt, err := service.Decide(context.Background(), decision)
	if err != nil || receipt.Version != hostinput.WorthinessReceiptVersion ||
		receipt.Status != hostinput.WorthinessFinalized || receipt.Replayed ||
		receipt.InputSHA256 != capture.InputSHA256 || receipt.MutationPlanID != "mutation-ignore" {
		t.Fatalf("Decide() = %#v, %v", receipt, err)
	}
	if _, _, err := service.Get(context.Background(), candidate.InputID, candidate.Workspace); stableInputCode(err) != result.CodeNotFound {
		t.Fatalf("pending Get() error = %v", err)
	}
	second := candidate
	second.InputID = "input-after-finalize"
	if _, err := service.Capture(context.Background(), second); err != nil {
		t.Fatalf("released pending capacity: %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service = hostinput.New(reopened, hostinput.Options{Clock: fixedInputClock{value: now.Add(time.Hour)}})
	loaded, loadedReceipt, err := service.GetDecision(context.Background(), decision.DecisionID, decision.Workspace)
	if err != nil || loaded.DecisionID != decision.DecisionID || loadedReceipt.DecidedAt != now {
		t.Fatalf("GetDecision() = %#v, %#v, %v", loaded, loadedReceipt, err)
	}
	replayed, err := service.Decide(context.Background(), decision)
	if err != nil || !replayed.Replayed || replayed.DecidedAt != receipt.DecidedAt {
		t.Fatalf("replayed Decide() = %#v, %v", replayed, err)
	}
}

func TestWorthinessDecisionRejectsMismatchedBindingAndUnverifiedMutation(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := hostinput.New(database, hostinput.Options{})
	capture, err := service.Capture(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	decision := ignoreDecision(capture)
	decision.InputSHA256 = "sha256:" + repeatHex("a")
	if _, err := service.Decide(context.Background(), decision); stableInputCode(err) != result.CodeRevisionConflict {
		t.Fatalf("capture binding error = %v", err)
	}
	decision = writeDecision(capture)
	decision.MutationReceipt.Status = skillwrite.ReceiptCommittedUnverified
	decision.MutationReceipt.Verified = false
	if _, err := service.Decide(context.Background(), decision); stableInputCode(err) != result.CodeValidation {
		t.Fatalf("unverified mutation error = %v", err)
	}
}

func TestWorthinessWriteAndReviseRequireMatchingCommittedChange(t *testing.T) {
	t.Parallel()
	for _, verdict := range []hostinput.WorthinessVerdict{hostinput.VerdictWrite, hostinput.VerdictRevise} {
		verdict := verdict
		t.Run(string(verdict), func(t *testing.T) {
			database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = database.Close() })
			service := hostinput.New(database, hostinput.Options{})
			capture, err := service.Capture(context.Background(), inputFixture())
			if err != nil {
				t.Fatal(err)
			}
			decision := writeDecision(capture)
			if verdict == hostinput.VerdictRevise {
				decision = reviseDecision(capture)
			}
			if _, err := service.Decide(context.Background(), decision); err != nil {
				t.Fatalf("Decide(%s): %v", verdict, err)
			}
		})
	}
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "mismatch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := hostinput.New(database, hostinput.Options{})
	capture, err := service.Capture(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	decision := writeDecision(capture)
	decision.Target.RowID = "other-row"
	if _, err := service.Decide(context.Background(), decision); stableInputCode(err) != result.CodeValidation {
		t.Fatalf("target mismatch error = %v", err)
	}
}

func TestWorthinessDecisionIdentityAndWorkspaceStayIsolated(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := hostinput.New(database, hostinput.Options{})
	firstInput := inputFixture()
	firstCapture, err := service.Capture(context.Background(), firstInput)
	if err != nil {
		t.Fatal(err)
	}
	first := ignoreDecision(firstCapture)
	if _, err := service.Decide(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	changed := first
	changed.DecisionID = "different-decision"
	if _, err := service.Decide(context.Background(), changed); stableInputCode(err) != result.CodeRevisionConflict {
		t.Fatalf("second decision error = %v", err)
	}
	if _, _, err := service.GetDecision(context.Background(), first.DecisionID, "other-workspace"); stableInputCode(err) != result.CodePermissionDenied {
		t.Fatalf("workspace leak error = %v", err)
	}
	secondInput := firstInput
	secondInput.InputID = "input-002"
	secondCapture, err := service.Capture(context.Background(), secondInput)
	if err != nil {
		t.Fatal(err)
	}
	second := ignoreDecision(secondCapture)
	second.DecisionID = first.DecisionID
	if _, err := service.Decide(context.Background(), second); stableInputCode(err) != result.CodeRevisionConflict {
		t.Fatalf("decision ID reuse error = %v", err)
	}
}

func TestConcurrentIdenticalWorthinessDecisionCommitsOneResult(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := hostinput.New(database, hostinput.Options{})
	capture, err := service.Capture(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	decision := ignoreDecision(capture)
	const workers = 8
	receipts := make(chan hostinput.WorthinessReceipt, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, decideErr := service.Decide(context.Background(), decision)
			receipts <- receipt
			errorsFound <- decideErr
		}()
	}
	wait.Wait()
	close(receipts)
	close(errorsFound)
	for decideErr := range errorsFound {
		if decideErr != nil {
			t.Fatal(decideErr)
		}
	}
	initial := 0
	for receipt := range receipts {
		if !receipt.Replayed {
			initial++
		}
	}
	if initial != 1 {
		t.Fatalf("initial decisions = %d, want 1", initial)
	}
}

func TestWorthinessCommitFaultLeavesPendingAndNoDecision(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	base := hostinput.New(database, hostinput.Options{})
	candidate := inputFixture()
	capture, err := base.Capture(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	faulted := hostinput.New(&decisionCommitFaultStore{Store: database}, hostinput.Options{})
	decision := ignoreDecision(capture)
	if _, err := faulted.Decide(context.Background(), decision); stableInputCode(err) != result.CodeInternal {
		t.Fatalf("commit fault error = %v", err)
	}
	if loaded, _, err := base.Get(context.Background(), candidate.InputID, candidate.Workspace); err != nil || loaded.CandidateText != candidate.CandidateText {
		t.Fatalf("pending after fault = %#v, %v", loaded, err)
	}
	if _, _, err := base.GetDecision(context.Background(), decision.DecisionID, decision.Workspace); stableInputCode(err) != result.CodeNotFound {
		t.Fatalf("decision after fault error = %v", err)
	}
}

func TestCorruptWorthinessDecisionFailsClosed(t *testing.T) {
	t.Parallel()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	service := hostinput.New(database, hostinput.Options{})
	capture, err := service.Capture(context.Background(), inputFixture())
	if err != nil {
		t.Fatal(err)
	}
	decision := ignoreDecision(capture)
	if _, err := service.Decide(context.Background(), decision); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin(context.Background(), store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(context.Background(), "host_input.decisions.v1", decision.InputID, []byte(`{"digest":"tampered"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.GetDecision(context.Background(), decision.DecisionID, decision.Workspace); stableInputCode(err) != result.CodeInternal {
		t.Fatalf("corrupt decision error = %v", err)
	}
}

func ignoreDecision(capture hostinput.Receipt) hostinput.WorthinessDecision {
	return hostinput.WorthinessDecision{Version: hostinput.WorthinessDecisionVersion, DecisionID: "decision-ignore",
		InputID: capture.InputID, Workspace: capture.Workspace, Actor: "agent:host", InputSHA256: capture.InputSHA256,
		ScopeSHA256: capture.ScopeSHA256, Verdict: hostinput.VerdictIgnore, Reason: "duplicate after preflight",
		AuthorizedDatabases: append([]string{}, capture.AuthorizedDatabases...), MutationReceipt: skillwrite.Receipt{
			Version: skillwrite.ReceiptVersion, PlanID: "mutation-ignore", Decision: skillwrite.DecisionIgnore,
			Status: skillwrite.ReceiptIgnored, Changes: []skillwrite.Change{}, Ignored: 1, Verified: true,
			Warnings: []result.Notice{}},
	}
}

func writeDecision(capture hostinput.Receipt) hostinput.WorthinessDecision {
	revision := uint64(1)
	return hostinput.WorthinessDecision{Version: hostinput.WorthinessDecisionVersion, DecisionID: "decision-write",
		InputID: capture.InputID, Workspace: capture.Workspace, Actor: "agent:host", InputSHA256: capture.InputSHA256,
		ScopeSHA256: capture.ScopeSHA256, Verdict: hostinput.VerdictWrite, Reason: "new complete semantic module",
		AuthorizedDatabases: append([]string{}, capture.AuthorizedDatabases...),
		Target:              &hostinput.WorthinessTarget{Database: "work", Table: "notes", RowID: "row-1", Revision: revision},
		MutationReceipt: skillwrite.Receipt{Version: skillwrite.ReceiptVersion, PlanID: "mutation-write",
			Decision: skillwrite.DecisionInsert, Status: skillwrite.ReceiptCommitted, Verified: true,
			Changes:  []skillwrite.Change{{Operation: "INSERT", Target: "row-1", ObjectID: "row-1", Revision: &revision}},
			Warnings: []result.Notice{}},
	}
}

func reviseDecision(capture hostinput.Receipt) hostinput.WorthinessDecision {
	decision := writeDecision(capture)
	decision.DecisionID = "decision-revise"
	decision.Verdict = hostinput.VerdictRevise
	decision.Reason = "new evidence changes the existing Row"
	decision.Target.Revision = 2
	decision.MutationReceipt.PlanID = "mutation-revise"
	decision.MutationReceipt.Decision = skillwrite.DecisionRevise
	decision.MutationReceipt.Changes[0].Operation = "UPDATE"
	decision.MutationReceipt.Changes[0].Revision = &decision.Target.Revision
	return decision
}

func repeatHex(character string) string {
	return strings.Repeat(character, 64)
}

type decisionCommitFaultStore struct{ store.Store }

func (database *decisionCommitFaultStore) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	tx, err := database.Store.Begin(ctx, mode)
	if err != nil {
		return nil, err
	}
	return &decisionCommitFaultTx{Tx: tx, fail: mode == store.ReadWrite}, nil
}

type decisionCommitFaultTx struct {
	store.Tx
	fail bool
}

func (tx *decisionCommitFaultTx) Commit() error {
	if tx.fail {
		return errors.New("injected decision commit fault")
	}
	return tx.Tx.Commit()
}
