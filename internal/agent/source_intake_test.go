package agent_test

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestSourceIntakeStreamsScopeIssueAnswerAndMonotonicProgress(t *testing.T) {
	t.Parallel()

	session, err := agent.NewSourceIntake("task-book-1")
	if err != nil {
		t.Fatal(err)
	}
	inventory := validSourceInventory()
	events, err := session.Begin(inventory)
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, events, 1,
		agent.SourceIntakeEventInventoryReady,
		agent.SourceIntakeEventQuestion,
		agent.SourceIntakeEventWaitingUser,
	)
	encoded, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "raw chapter text must never appear") {
		t.Fatalf("events leaked source content: %s", encoded)
	}

	accepted, err := session.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionAll, TargetDatabases: []string{"knowledge", "work"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, accepted, 4, agent.SourceIntakeEventAccepted)
	progress, err := session.Progress(agent.SourceProgress{Completed: 1, Total: 20, UnitID: "chapter-1"})
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, progress, 5, agent.SourceIntakeEventProgress)

	issueEvents, err := session.Issue(agent.SourceIssue{
		Code: "FOOTNOTE_TARGET_MISSING", Message: "One footnote target is missing",
		RequiresUser: true, QuestionID: "question-footnote-1",
		Prompt: "Continue without this footnote target?", Options: []string{"continue", "cancel"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, issueEvents, 6,
		agent.SourceIntakeEventIssue,
		agent.SourceIntakeEventQuestion,
		agent.SourceIntakeEventWaitingUser,
	)
	answerEvents, err := session.Answer("question-footnote-1", "continue")
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, answerEvents, 9,
		agent.SourceIntakeEventAnswerReceived,
		agent.SourceIntakeEventResumed,
	)
	if answerEvents[0].Answer == nil || answerEvents[0].Answer.UTF8Bytes != uint64(len("continue")) ||
		!validTestSHA256(answerEvents[0].Answer.SHA256) {
		t.Fatalf("answer event = %#v", answerEvents[0])
	}
	answerJSON, _ := json.Marshal(answerEvents)
	if strings.Contains(string(answerJSON), "continue") {
		t.Fatalf("answer event leaked raw answer: %s", answerJSON)
	}
	if _, err := session.Progress(agent.SourceProgress{Completed: 0, Total: 20, UnitID: "chapter-1"});
		!errors.Is(err, agent.ErrSourceIntakeState) {
		t.Fatalf("regressed Progress() error = %v", err)
	}
	finalProgress, err := session.Progress(agent.SourceProgress{Completed: 20, Total: 20, UnitID: "chapter-2"})
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, finalProgress, 11, agent.SourceIntakeEventProgress)
	if snapshot := session.Snapshot(); snapshot.State != agent.SourceIntakeAccepted ||
		snapshot.Sequence != 11 || snapshot.Selection == nil || snapshot.Question != nil ||
		snapshot.Progress.Completed != 20 {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestSourceIntakeValidatesSelectionQuestionAndTerminalState(t *testing.T) {
	t.Parallel()

	session, err := agent.NewSourceIntake("task-book-2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Confirm(agent.SourceSelection{}); !errors.Is(err, agent.ErrSourceIntakeState) {
		t.Fatalf("Confirm before Begin error = %v", err)
	}
	if _, err := session.Begin(validSourceInventory()); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Begin(validSourceInventory()); !errors.Is(err, agent.ErrSourceIntakeState) {
		t.Fatalf("duplicate Begin error = %v", err)
	}
	invalidSelections := []agent.SourceSelection{
		{Mode: agent.SourceSelectionAll, UnitIDs: []string{"chapter-1"}, TargetDatabases: []string{"work"}},
		{Mode: agent.SourceSelectionSelected, UnitIDs: []string{"missing"}, TargetDatabases: []string{"work"}},
		{Mode: agent.SourceSelectionSelected, UnitIDs: []string{"chapter-1"}, TargetDatabases: []string{"work", " WORK "}},
	}
	for _, selection := range invalidSelections {
		if _, err := session.Confirm(selection); !errors.Is(err, agent.ErrInvalidSourceIntake) {
			t.Fatalf("Confirm(%#v) error = %v", selection, err)
		}
	}
	if _, err := session.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionSelected, UnitIDs: []string{"chapter-1"}, TargetDatabases: []string{"work"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Answer("no-question", "value"); !errors.Is(err, agent.ErrSourceIntakeQuestion) {
		t.Fatalf("Answer without question error = %v", err)
	}
	cancelled, err := session.Cancel("user cancelled import")
	if err != nil {
		t.Fatal(err)
	}
	assertIntakeEvents(t, cancelled, 5, agent.SourceIntakeEventCancelled)
	if _, err := session.Progress(agent.SourceProgress{Completed: 1, Total: 1});
		!errors.Is(err, agent.ErrSourceIntakeState) {
		t.Fatalf("Progress after cancel error = %v", err)
	}
}

func TestSourceIntakeConcurrentConfirmHasOneWinnerAndSnapshotIsIsolated(t *testing.T) {
	t.Parallel()

	session, err := agent.NewSourceIntake("task-book-concurrent")
	if err != nil {
		t.Fatal(err)
	}
	inventory := validSourceInventory()
	if _, err := session.Begin(inventory); err != nil {
		t.Fatal(err)
	}
	inventory.Units[1].Label = "tampered caller"

	const contenders = 16
	start := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, confirmErr := session.Confirm(agent.SourceSelection{
				Mode: agent.SourceSelectionAll, TargetDatabases: []string{"work"},
			})
			results <- confirmErr
		}()
	}
	close(start)
	wait.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for confirmErr := range results {
		if confirmErr == nil {
			succeeded++
		} else if errors.Is(confirmErr, agent.ErrSourceIntakeState) {
			rejected++
		} else {
			t.Fatalf("unexpected Confirm error = %v", confirmErr)
		}
	}
	if succeeded != 1 || rejected != contenders-1 {
		t.Fatalf("succeeded=%d rejected=%d", succeeded, rejected)
	}
	snapshot := session.Snapshot()
	if snapshot.Inventory.Units[1].Label != "Chapter one" {
		t.Fatalf("snapshot aliased caller inventory = %#v", snapshot.Inventory)
	}
	snapshot.Inventory.Units[1].Label = "tampered snapshot"
	snapshot.Selection.TargetDatabases[0] = "archive"
	again := session.Snapshot()
	if again.Inventory.Units[1].Label != "Chapter one" || again.Selection.TargetDatabases[0] != "work" {
		t.Fatalf("Snapshot() exposed mutable state = %#v", again)
	}
}

func validSourceInventory() agent.SourceInventory {
	return agent.SourceInventory{
		Version: agent.SourceInventoryVersion,
		Source: agent.SourceDescriptor{
			ID: "source-book-1", Title: "A test book", Locator: "/books/test.epub",
			ContentSHA256: "sha256:" + strings.Repeat("a", 64), Kind: "document",
		},
		Units: []agent.SourceUnit{
			{ID: "source-root", Kind: "source", Label: "A test book"},
			{ID: "chapter-1", ParentID: "source-root", Kind: "chapter", Label: "Chapter one", Extent: 10},
			{ID: "chapter-2", ParentID: "source-root", Kind: "chapter", Label: "Chapter two", Extent: 10},
		},
	}
}

func assertIntakeEvents(
	t *testing.T,
	events []agent.SourceIntakeEvent,
	firstSequence uint64,
	kinds ...agent.SourceIntakeEventKind,
) {
	t.Helper()
	if len(events) != len(kinds) {
		t.Fatalf("events = %#v, want kinds %v", events, kinds)
	}
	for index, event := range events {
		if event.Kind != kinds[index] || event.Sequence != firstSequence+uint64(index) || event.Validate() != nil {
			t.Fatalf("event[%d] = %#v, Validate=%v", index, event, event.Validate())
		}
	}
}
