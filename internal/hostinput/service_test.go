package hostinput_test

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/hostinput"
	"github.com/HW-Yue/Memora/internal/result"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestCaptureReturnsStableNonEchoingReceiptAndReplaysAcrossReopen(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "host-input.db")
	store, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC)
	service := hostinput.New(store, hostinput.Options{Clock: fixedInputClock{value: now}})
	candidate := inputFixture()
	first, err := service.Capture(context.Background(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if first.Version != hostinput.ReceiptVersion || first.Status != hostinput.StatusPending || first.Replayed ||
		first.InputSHA256 == "" || first.ContentSHA256 == "" || first.ScopeSHA256 == "" ||
		first.ContentBytes != len(candidate.CandidateText) || !first.CapturedAt.Equal(now) ||
		strings.Contains(first.String(), candidate.CandidateText) {
		t.Fatalf("receipt = %#v", first)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativekvstore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service = hostinput.New(reopened, hostinput.Options{Clock: fixedInputClock{value: now.Add(time.Hour)}})
	replayed, err := service.Capture(context.Background(), candidate)
	if err != nil || !replayed.Replayed || replayed.InputSHA256 != first.InputSHA256 ||
		!replayed.CapturedAt.Equal(first.CapturedAt) {
		t.Fatalf("replay = %#v, %v", replayed, err)
	}
	loaded, receipt, err := service.Get(context.Background(), candidate.InputID, candidate.Workspace)
	if err != nil || loaded.CandidateText != candidate.CandidateText || receipt.InputSHA256 != first.InputSHA256 {
		t.Fatalf("Get() = %#v, %#v, %v", loaded, receipt, err)
	}
}

func TestConcurrentIdenticalCaptureCommitsOneStableRecord(t *testing.T) {
	t.Parallel()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := hostinput.New(store, hostinput.Options{})
	candidate := inputFixture()
	const workers = 8
	receipts := make(chan hostinput.Receipt, workers)
	errorsFound := make(chan error, workers)
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, captureErr := service.Capture(context.Background(), candidate)
			receipts <- receipt
			errorsFound <- captureErr
		}()
	}
	wait.Wait()
	close(receipts)
	close(errorsFound)
	for captureErr := range errorsFound {
		if captureErr != nil {
			t.Fatal(captureErr)
		}
	}
	digest, initial := "", 0
	for receipt := range receipts {
		if digest == "" {
			digest = receipt.InputSHA256
		}
		if receipt.InputSHA256 != digest {
			t.Fatalf("capture digest drift = %#v", receipt)
		}
		if !receipt.Replayed {
			initial++
		}
	}
	if initial != 1 {
		t.Fatalf("initial captures = %d, want 1", initial)
	}
}

func TestCaptureRejectsIdentityReuseInvalidSourcesAndWorkspaceLeak(t *testing.T) {
	t.Parallel()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := hostinput.New(store, hostinput.Options{})
	candidate := inputFixture()
	if _, err := service.Capture(context.Background(), candidate); err != nil {
		t.Fatal(err)
	}
	changed := candidate
	changed.CandidateText = "different candidate"
	if _, err := service.Capture(context.Background(), changed); stableInputCode(err) != result.CodeRevisionConflict {
		t.Fatalf("identity reuse error = %v", err)
	}
	if _, _, err := service.Get(context.Background(), candidate.InputID, "other-workspace"); stableInputCode(err) != result.CodePermissionDenied {
		t.Fatalf("workspace leak error = %v", err)
	}
	invalid := candidate
	invalid.InputID = "invalid-anchor"
	invalid.Source = hostinput.Source{Kind: history.SourceDocumentAnchor, Title: "Document"}
	if _, err := service.Capture(context.Background(), invalid); stableInputCode(err) != result.CodeValidation {
		t.Fatalf("invalid anchor error = %v", err)
	}
	invalid = candidate
	invalid.InputID = "oversized"
	invalid.CandidateText = strings.Repeat("x", hostinput.MaximumCandidateBytes+1)
	if _, err := service.Capture(context.Background(), invalid); stableInputCode(err) != result.CodeOutputTruncated {
		t.Fatalf("oversized candidate error = %v", err)
	}
}

func TestCaptureCanonicalizesScopeAndEnforcesPendingCapacity(t *testing.T) {
	t.Parallel()
	store, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-input.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := hostinput.New(store, hostinput.Options{MaximumPending: 2})
	first := inputFixture()
	first.AuthorizedDatabases = []string{"work", "archive"}
	receipt, err := service.Capture(context.Background(), first)
	if err != nil || strings.Join(receipt.AuthorizedDatabases, ",") != "archive,work" {
		t.Fatalf("canonical capture = %#v, %v", receipt, err)
	}
	second := first
	second.InputID = "input-002"
	if _, err := service.Capture(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	third := first
	third.InputID = "input-003"
	if _, err := service.Capture(context.Background(), third); stableInputCode(err) != result.CodeConstraint {
		t.Fatalf("capacity error = %v", err)
	}
}

func inputFixture() hostinput.Input {
	return hostinput.Input{Version: hostinput.InputVersion, InputID: "input-001", Workspace: "project-memora",
		Actor: "agent:host", AuthorizedDatabases: []string{"work"}, CandidateText: "Remember that Router returns locators only.",
		Source: hostinput.Source{Kind: history.SourceConversationAssertion, Title: "Router boundary"}}
}

type fixedInputClock struct{ value time.Time }

func (clock fixedInputClock) Now() time.Time { return clock.value }

func stableInputCode(err error) result.Code {
	if stable, ok := err.(interface{ StableCode() string }); ok {
		return result.Code(stable.StableCode())
	}
	return ""
}
