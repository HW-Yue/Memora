package agent_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestAssimilationJobReopensHistoryAndCheckpoint(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	intake, begin := begunIntake(t, "task-durable-book")
	start := assimilationCommand("task-durable-book", "command-start", agent.AssimilationJobCommandStart, 0)
	start.SourceEvents = begin
	receipt, err := store.Apply(start)
	if err != nil {
		t.Fatal(err)
	}
	assertJobReceipt(t, receipt, 1, agent.AssimilationJobWaitingUser, false)

	accepted, err := intake.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionAll, TargetDatabases: []string{"knowledge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendAccepted := assimilationCommand(
		"task-durable-book", "command-accept", agent.AssimilationJobCommandAppendSourceEvents, 1,
	)
	appendAccepted.SourceEvents = accepted
	if _, err := store.Apply(appendAccepted); err != nil {
		t.Fatal(err)
	}
	progress, err := intake.Progress(agent.SourceProgress{Completed: 1, Total: 20, UnitID: "chapter-1"})
	if err != nil {
		t.Fatal(err)
	}
	appendProgress := assimilationCommand(
		"task-durable-book", "command-progress", agent.AssimilationJobCommandAppendSourceEvents, 2,
	)
	appendProgress.SourceEvents = progress
	if _, err := store.Apply(appendProgress); err != nil {
		t.Fatal(err)
	}
	checkpoint := agent.AssimilationCheckpoint{
		Version: agent.AssimilationCheckpointVersion, Ordinal: 1, SourceSequence: 5,
		Stage: "inventory-accepted", Cursor: "chapter-1:extent:1",
		StateSHA256: "sha256:" + strings.Repeat("b", 64),
	}
	saveCheckpoint := assimilationCommand(
		"task-durable-book", "command-checkpoint", agent.AssimilationJobCommandSaveCheckpoint, 3,
	)
	saveCheckpoint.Checkpoint = &checkpoint
	receipt, err = store.Apply(saveCheckpoint)
	if err != nil {
		t.Fatal(err)
	}
	assertJobReceipt(t, receipt, 4, agent.AssimilationJobActive, false)

	want := receipt.Snapshot
	reopened, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Read("task-durable-book")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.LastSourceSequence != 5 || got.Checkpoint == nil ||
		got.Checkpoint.Ordinal != 1 || got.Intake.Progress.Completed != 1 ||
		got.Intake.Selection == nil || got.Intake.Question != nil {
		t.Fatalf("reopened snapshot = %#v, want %#v", got, want)
	}
	restoredIntake, err := agent.RestoreSourceIntake(got.Intake)
	if err != nil {
		t.Fatal(err)
	}
	restoredProgress, err := restoredIntake.Progress(agent.SourceProgress{
		Completed: 2, Total: 20, UnitID: "chapter-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	appendRestored := assimilationCommand(
		"task-durable-book", "command-restored-progress", agent.AssimilationJobCommandAppendSourceEvents, 4,
	)
	appendRestored.SourceEvents = restoredProgress
	if _, err := reopened.Apply(appendRestored); err != nil {
		t.Fatalf("append from restored Source Intake: %v", err)
	}
	got.Intake.Inventory.Units[1].Label = "tampered recovered snapshot"
	isolated, err := reopened.Read("task-durable-book")
	if err != nil {
		t.Fatal(err)
	}
	if isolated.Intake.Inventory.Units[1].Label != "Chapter one" ||
		isolated.Intake.Progress.Completed != 2 {
		t.Fatalf("persisted Intake snapshot was mutable or not resumed: %#v", isolated.Intake)
	}
	history, err := reopened.Events("task-durable-book", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 {
		t.Fatalf("history len = %d, want 5", len(history))
	}
	for index, event := range history {
		if event.Revision != uint64(index+1) || event.Validate() != nil {
			t.Fatalf("history[%d] = %#v, Validate=%v", index, event, event.Validate())
		}
	}
	journal := readOnlyJournal(t, root)
	if bytes.Contains(journal, []byte("raw chapter text must never appear")) {
		t.Fatalf("journal leaked source body: %s", journal)
	}
}

func TestAssimilationJobCommandsAreIdempotentRevisionGuardedAndTerminal(t *testing.T) {
	t.Parallel()

	store, err := agent.OpenAssimilationJobStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	intake, begin := begunIntake(t, "task-command-guards")
	start := assimilationCommand("task-command-guards", "command-start", agent.AssimilationJobCommandStart, 0)
	start.SourceEvents = begin
	first, err := store.Apply(start)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := store.Apply(start)
	if err != nil {
		t.Fatal(err)
	}
	assertJobReceipt(t, replayed, 1, agent.AssimilationJobWaitingUser, true)
	if !reflect.DeepEqual(first.Event, replayed.Event) {
		t.Fatalf("replayed event changed: %#v != %#v", replayed.Event, first.Event)
	}
	conflict := start
	conflict.SourceEvents = append([]agent.SourceIntakeEvent(nil), start.SourceEvents...)
	changedInventory := *conflict.SourceEvents[0].Inventory
	changedInventory.Source.Title = "A different valid title"
	conflict.SourceEvents[0].Inventory = &changedInventory
	if _, err := store.Apply(conflict); !errors.Is(err, agent.ErrAssimilationJobCommandConflict) {
		t.Fatalf("same command ID with different content error = %v", err)
	}

	accepted, err := intake.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionAll, TargetDatabases: []string{"knowledge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	const contenders = 16
	startRace := make(chan struct{})
	results := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-startRace
			command := assimilationCommand(
				"task-command-guards", "command-race-"+string(rune('a'+index)),
				agent.AssimilationJobCommandAppendSourceEvents, 1,
			)
			command.SourceEvents = accepted
			_, applyErr := store.Apply(command)
			results <- applyErr
		}()
	}
	close(startRace)
	wait.Wait()
	close(results)
	succeeded, lost := 0, 0
	for applyErr := range results {
		switch {
		case applyErr == nil:
			succeeded++
		case errors.Is(applyErr, agent.ErrAssimilationJobRevision):
			lost++
		default:
			t.Fatalf("unexpected concurrent Apply error = %v", applyErr)
		}
	}
	if succeeded != 1 || lost != contenders-1 {
		t.Fatalf("succeeded=%d lost=%d", succeeded, lost)
	}

	cancel := assimilationCommand("task-command-guards", "command-cancel", agent.AssimilationJobCommandCancel, 2)
	cancel.ReasonCode = "USER_CANCELLED"
	cancelled, err := store.Apply(cancel)
	if err != nil {
		t.Fatal(err)
	}
	assertJobReceipt(t, cancelled, 3, agent.AssimilationJobCancelled, false)
	if replay, err := store.Apply(cancel); err != nil || !replay.Replayed {
		t.Fatalf("cancel replay = %#v, error = %v", replay, err)
	}
	checkpoint := assimilationCommand(
		"task-command-guards", "command-after-cancel", agent.AssimilationJobCommandSaveCheckpoint, 3,
	)
	checkpoint.Checkpoint = &agent.AssimilationCheckpoint{
		Version: agent.AssimilationCheckpointVersion, Ordinal: 1, SourceSequence: 4,
		Stage: "late", Cursor: "none", StateSHA256: "sha256:" + strings.Repeat("c", 64),
	}
	if _, err := store.Apply(checkpoint); !errors.Is(err, agent.ErrAssimilationJobTerminal) {
		t.Fatalf("checkpoint after cancel error = %v", err)
	}
	history, err := store.Events("task-command-guards", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("history len = %d, want 3", len(history))
	}
}

func TestAssimilationJobRecoversTornTailAndRejectsCompleteCorruption(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	intake, begin := begunIntake(t, "task-recovery")
	start := assimilationCommand("task-recovery", "command-start", agent.AssimilationJobCommandStart, 0)
	start.SourceEvents = begin
	if _, err := store.Apply(start); err != nil {
		t.Fatal(err)
	}
	accepted, err := intake.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionAll, TargetDatabases: []string{"knowledge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	accept := assimilationCommand("task-recovery", "command-accept", agent.AssimilationJobCommandAppendSourceEvents, 1)
	accept.SourceEvents = accepted
	if _, err := store.Apply(accept); err != nil {
		t.Fatal(err)
	}
	path := onlyJournalPath(t, root)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"version":"memora.assimilation-journal-record/v1"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reopened.Read("task-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Revision != 2 || snapshot.State != agent.AssimilationJobActive {
		t.Fatalf("recovered snapshot = %#v", snapshot)
	}
	progress, err := intake.Progress(agent.SourceProgress{Completed: 1, Total: 20, UnitID: "chapter-1"})
	if err != nil {
		t.Fatal(err)
	}
	appendProgress := assimilationCommand(
		"task-recovery", "command-progress", agent.AssimilationJobCommandAppendSourceEvents, 2,
	)
	appendProgress.SourceEvents = progress
	if _, err := reopened.Apply(appendProgress); err != nil {
		t.Fatalf("append after recovery: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupted := bytes.Replace(data, []byte(`"revision":1`), []byte(`"revision":9`), 1)
	if bytes.Equal(corrupted, data) {
		t.Fatal("test could not locate revision field")
	}
	if err := os.WriteFile(path, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Read("task-recovery"); !errors.Is(err, agent.ErrAssimilationJobCorrupt) {
		t.Fatalf("Read corrupted journal error = %v", err)
	}
}

func TestAssimilationJobRollsBackTornFirstRecord(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	_, begin := begunIntake(t, "task-first-record")
	start := assimilationCommand("task-first-record", "command-start", agent.AssimilationJobCommandStart, 0)
	start.SourceEvents = begin
	if _, err := store.Apply(start); err != nil {
		t.Fatal(err)
	}
	path := onlyJournalPath(t, root)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Read("task-first-record"); !errors.Is(err, agent.ErrAssimilationJobNotFound) {
		t.Fatalf("Read torn first record error = %v", err)
	}
	if _, err := reopened.Apply(start); err != nil {
		t.Fatalf("restart after torn first record: %v", err)
	}
}

func TestAssimilationJobRejectsInvalidBatchCheckpointAndLeaksNoAnswer(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	intake, begin := begunIntake(t, "task-validation")
	invalidStart := assimilationCommand("task-validation", "command-invalid", agent.AssimilationJobCommandStart, 0)
	invalidStart.SourceEvents = begin[1:]
	if _, err := store.Apply(invalidStart); !errors.Is(err, agent.ErrAssimilationJobInvalid) {
		t.Fatalf("invalid start error = %v", err)
	}
	start := assimilationCommand("task-validation", "command-start", agent.AssimilationJobCommandStart, 0)
	start.SourceEvents = begin
	if _, err := store.Apply(start); err != nil {
		t.Fatal(err)
	}
	badCheckpoint := assimilationCommand(
		"task-validation", "command-bad-checkpoint", agent.AssimilationJobCommandSaveCheckpoint, 1,
	)
	badCheckpoint.Checkpoint = &agent.AssimilationCheckpoint{
		Version: agent.AssimilationCheckpointVersion, Ordinal: 1, SourceSequence: 999,
		Stage: "invalid", Cursor: "nowhere", StateSHA256: "sha256:" + strings.Repeat("d", 64),
	}
	if _, err := store.Apply(badCheckpoint); !errors.Is(err, agent.ErrAssimilationJobInvalid) {
		t.Fatalf("bad checkpoint error = %v", err)
	}
	accepted, err := intake.Confirm(agent.SourceSelection{
		Mode: agent.SourceSelectionAll, TargetDatabases: []string{"knowledge"},
	})
	if err != nil {
		t.Fatal(err)
	}
	accept := assimilationCommand("task-validation", "command-accept", agent.AssimilationJobCommandAppendSourceEvents, 1)
	accept.SourceEvents = accepted
	if _, err := store.Apply(accept); err != nil {
		t.Fatal(err)
	}
	issue, err := intake.Issue(agent.SourceIssue{
		Code: "AMBIGUOUS_NOTE", Message: "A note needs a decision", RequiresUser: true,
		QuestionID: "question-private", Prompt: "Choose handling", Options: []string{"yes", "no"},
	})
	if err != nil {
		t.Fatal(err)
	}
	appendIssue := assimilationCommand("task-validation", "command-issue", agent.AssimilationJobCommandAppendSourceEvents, 2)
	appendIssue.SourceEvents = issue
	if _, err := store.Apply(appendIssue); err != nil {
		t.Fatal(err)
	}
	const privateAnswer = "private-secret-answer-must-not-persist"
	answer, err := intake.Answer("question-private", privateAnswer)
	if err != nil {
		t.Fatal(err)
	}
	appendAnswer := assimilationCommand("task-validation", "command-answer", agent.AssimilationJobCommandAppendSourceEvents, 3)
	appendAnswer.SourceEvents = answer
	if _, err := store.Apply(appendAnswer); err != nil {
		t.Fatal(err)
	}
	if journal := readOnlyJournal(t, root); bytes.Contains(journal, []byte(privateAnswer)) {
		t.Fatalf("journal leaked answer body: %s", journal)
	}
}

func assimilationCommand(
	jobID string,
	commandID string,
	kind agent.AssimilationJobCommandKind,
	expectedRevision uint64,
) agent.AssimilationJobCommand {
	return agent.AssimilationJobCommand{
		Version: agent.AssimilationJobCommandVersion, JobID: jobID, CommandID: commandID,
		Kind: kind, ExpectedRevision: expectedRevision,
	}
}

func begunIntake(t *testing.T, taskID string) (*agent.SourceIntake, []agent.SourceIntakeEvent) {
	t.Helper()
	intake, err := agent.NewSourceIntake(taskID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := intake.Begin(validSourceInventoryForTask(taskID))
	if err != nil {
		t.Fatal(err)
	}
	return intake, events
}

func validSourceInventoryForTask(taskID string) agent.SourceInventory {
	inventory := validSourceInventory()
	inventory.Source.ID = "source-" + taskID
	return inventory
}

func assertJobReceipt(
	t *testing.T,
	receipt agent.AssimilationJobReceipt,
	revision uint64,
	state agent.AssimilationJobState,
	replayed bool,
) {
	t.Helper()
	if receipt.Replayed != replayed || receipt.Event.Revision != revision || receipt.Event.State != state ||
		receipt.Snapshot.Revision != revision || receipt.Snapshot.State != state ||
		receipt.Event.Validate() != nil {
		t.Fatalf("receipt = %#v, Event.Validate=%v", receipt, receipt.Event.Validate())
	}
}

func readOnlyJournal(t *testing.T, root string) []byte {
	t.Helper()
	data, err := os.ReadFile(onlyJournalPath(t, root))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func onlyJournalPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ajournal") {
			paths = append(paths, filepath.Join(root, entry.Name()))
		}
	}
	if len(paths) != 1 {
		t.Fatalf("journal paths = %v", paths)
	}
	return paths[0]
}
