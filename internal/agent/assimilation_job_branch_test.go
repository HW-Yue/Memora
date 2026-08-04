package agent_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestAssimilationJobPausesOnlyAffectedBranchesAndResumesAfterUnrelatedProgress(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, checkpointSnapshot := activeBranchJob(t, root, "task-branch-local")

	raiseZ := branchIssueCommand("task-branch-local", "command-raise-z", "branch-z", "issue-z", 0)
	receipt, err := store.Apply(raiseZ)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Event.Kind != agent.AssimilationJobEventBranchIssueRaised ||
		receipt.Event.State != agent.AssimilationJobActive || receipt.Event.BranchIssue == nil ||
		receipt.Event.BranchIssue.BranchID != "branch-z" || receipt.Snapshot.State != agent.AssimilationJobActive ||
		receipt.Snapshot.BranchRunnable("branch-z") || !receipt.Snapshot.BranchRunnable("branch-free") {
		t.Fatalf("raise branch-z receipt = %#v", receipt)
	}
	assertBranchJobProgressUnchanged(t, checkpointSnapshot, receipt.Snapshot)
	progress := assimilationCommand(
		"task-branch-local", "command-unaffected-progress", agent.AssimilationJobCommandSaveCheckpoint,
		receipt.Snapshot.Revision,
	)
	progress.Checkpoint = &agent.AssimilationCheckpoint{
		Version: agent.AssimilationCheckpointVersion, Ordinal: 2, SourceSequence: 4,
		Stage: "drafting", Cursor: "extent:4", StateSHA256: "sha256:" + strings.Repeat("e", 64),
	}
	progressed, err := store.Apply(progress)
	if err != nil || progressed.Snapshot.Checkpoint == nil ||
		progressed.Snapshot.Checkpoint.Ordinal != 2 || progressed.Snapshot.BranchRunnable("branch-z") {
		t.Fatalf("unaffected progress = %#v, %v", progressed, err)
	}

	// Raising another branch advances the Job journal but must not invalidate a
	// later answer for branch-z at branch revision 1.
	raiseA := branchIssueCommand("task-branch-local", "command-raise-a", "branch-a", "issue-a", 0)
	receiptA, err := store.Apply(raiseA)
	if err != nil {
		t.Fatal(err)
	}
	if len(receiptA.Snapshot.Branches) != 2 || receiptA.Snapshot.Branches[0].BranchID != "branch-a" ||
		receiptA.Snapshot.Branches[1].BranchID != "branch-z" ||
		receiptA.Snapshot.BranchRunnable("branch-a") || receiptA.Snapshot.BranchRunnable("branch-z") {
		t.Fatalf("sorted waiting branches = %#v", receiptA.Snapshot.Branches)
	}

	const privateAnswer = "private explanation that must never enter the journal"
	answerZ := branchAnswerCommand(
		"task-branch-local", "command-answer-z", "branch-z", "issue-z", 1,
		"keep_separate", privateAnswer,
	)
	answered, err := store.Apply(answerZ)
	if err != nil {
		t.Fatal(err)
	}
	branchZ, found := answered.Snapshot.Branch("branch-z")
	if !found || branchZ.State != agent.AssimilationBranchReady || branchZ.Revision != 2 ||
		branchZ.Issue != nil || branchZ.Resolution == nil ||
		branchZ.Resolution.SelectedOption != "keep_separate" ||
		!answered.Snapshot.BranchRunnable("branch-z") || answered.Snapshot.BranchRunnable("branch-a") ||
		answered.Snapshot.State != agent.AssimilationJobActive {
		t.Fatalf("answered branch-z snapshot = %#v", answered.Snapshot)
	}
	assertBranchJobProgressUnchanged(t, progressed.Snapshot, answered.Snapshot)

	replayed, err := store.Apply(answerZ)
	if err != nil || !replayed.Replayed || !reflect.DeepEqual(replayed.Event, answered.Event) {
		t.Fatalf("answer replay = %#v, %v", replayed, err)
	}
	changedAnswer := answerZ
	changedAnswer.BranchAnswer = &agent.AssimilationBranchAnswer{
		Version: agent.AssimilationBranchAnswerVersion, BranchID: "branch-z", IssueID: "issue-z",
		SelectedOption: "merge", AnswerUTF8Bytes: 1,
		AnswerSHA256: "sha256:" + strings.Repeat("f", 64),
	}
	if _, err := store.Apply(changedAnswer); !errors.Is(err, agent.ErrAssimilationJobCommandConflict) {
		t.Fatalf("changed replay error = %v", err)
	}
	reopened, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := reopened.Read("task-branch-local")
	if err != nil || !reflect.DeepEqual(recovered, answered.Snapshot) {
		t.Fatalf("recovered snapshot = %#v, %v; want %#v", recovered, err, answered.Snapshot)
	}
	if journal := readOnlyJournal(t, root); bytes.Contains(journal, []byte(privateAnswer)) {
		t.Fatalf("journal leaked private answer: %s", journal)
	}
}

func TestAssimilationJobBranchAnswersUseIndependentRevisions(t *testing.T) {
	t.Parallel()
	store, _ := activeBranchJob(t, t.TempDir(), "task-independent-branches")
	if _, err := store.Apply(branchIssueCommand(
		"task-independent-branches", "raise-a", "branch-a", "issue-a", 0,
	)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Apply(branchIssueCommand(
		"task-independent-branches", "raise-b", "branch-b", "issue-b", 0,
	)); err != nil {
		t.Fatal(err)
	}

	commands := []agent.AssimilationJobCommand{
		branchAnswerCommand("task-independent-branches", "answer-a", "branch-a", "issue-a", 1, "merge", "a"),
		branchAnswerCommand("task-independent-branches", "answer-b", "branch-b", "issue-b", 1, "keep_separate", "b"),
	}
	var wait sync.WaitGroup
	errorsSeen := make([]error, len(commands))
	for index := range commands {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			_, errorsSeen[index] = store.Apply(commands[index])
		}(index)
	}
	wait.Wait()
	for index, err := range errorsSeen {
		if err != nil {
			t.Fatalf("independent answer[%d] error = %v", index, err)
		}
	}
	snapshot, err := store.Read("task-independent-branches")
	if err != nil || !snapshot.BranchRunnable("branch-a") || !snapshot.BranchRunnable("branch-b") {
		t.Fatalf("independent branch snapshot = %#v, %v", snapshot, err)
	}
}

func TestAssimilationJobBranchAnswerHasOneRevisionWinner(t *testing.T) {
	t.Parallel()
	store, _ := activeBranchJob(t, t.TempDir(), "task-branch-race")
	if _, err := store.Apply(branchIssueCommand(
		"task-branch-race", "raise-race", "branch-race", "issue-race", 0,
	)); err != nil {
		t.Fatal(err)
	}
	commands := []agent.AssimilationJobCommand{
		branchAnswerCommand("task-branch-race", "answer-merge", "branch-race", "issue-race", 1, "merge", "one"),
		branchAnswerCommand("task-branch-race", "answer-separate", "branch-race", "issue-race", 1, "keep_separate", "two"),
	}
	start := make(chan struct{})
	results := make(chan error, len(commands))
	var wait sync.WaitGroup
	for index := range commands {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := store.Apply(commands[index])
			results <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(results)
	winners, stale := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, agent.ErrAssimilationBranchRevision):
			stale++
		default:
			t.Fatalf("unexpected answer error = %v", err)
		}
	}
	if winners != 1 || stale != 1 {
		t.Fatalf("winners=%d stale=%d", winners, stale)
	}
}

func TestAssimilationJobRejectsInvalidBranchTransitions(t *testing.T) {
	t.Parallel()

	t.Run("scope still waiting", func(t *testing.T) {
		store, err := agent.OpenAssimilationJobStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		_, begin := begunIntake(t, "task-branch-waiting")
		start := assimilationCommand("task-branch-waiting", "start", agent.AssimilationJobCommandStart, 0)
		start.SourceEvents = begin
		if _, err := store.Apply(start); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Apply(branchIssueCommand(
			"task-branch-waiting", "raise", "branch-a", "issue-a", 0,
		)); !errors.Is(err, agent.ErrAssimilationJobInvalid) {
			t.Fatalf("raise before scope answer error = %v", err)
		}
	})

	store, _ := activeBranchJob(t, t.TempDir(), "task-branch-invalid")
	if _, err := store.Apply(branchIssueCommand(
		"task-branch-invalid", "raise", "branch-a", "issue-a", 0,
	)); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		command agent.AssimilationJobCommand
		want    error
	}{
		{
			name: "wrong option",
			command: branchAnswerCommand(
				"task-branch-invalid", "answer-wrong-option", "branch-a", "issue-a", 1,
				"invented", "answer",
			),
			want: agent.ErrAssimilationJobInvalid,
		},
		{
			name: "wrong issue",
			command: branchAnswerCommand(
				"task-branch-invalid", "answer-wrong-issue", "branch-a", "other-issue", 1,
				"merge", "answer",
			),
			want: agent.ErrAssimilationJobInvalid,
		},
		{
			name: "stale branch revision",
			command: branchAnswerCommand(
				"task-branch-invalid", "answer-stale", "branch-a", "issue-a", 9,
				"merge", "answer",
			),
			want: agent.ErrAssimilationBranchRevision,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := store.Apply(test.command); !errors.Is(err, test.want) {
				t.Fatalf("Apply() error = %v, want %v", err, test.want)
			}
		})
	}
}

func activeBranchJob(
	t *testing.T,
	root string,
	jobID string,
) (*agent.AssimilationJobStore, agent.AssimilationJobSnapshot) {
	t.Helper()
	store, err := agent.OpenAssimilationJobStore(root)
	if err != nil {
		t.Fatal(err)
	}
	intake, begin := begunIntake(t, jobID)
	start := assimilationCommand(jobID, "command-start", agent.AssimilationJobCommandStart, 0)
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
	accept := assimilationCommand(jobID, "command-accept", agent.AssimilationJobCommandAppendSourceEvents, 1)
	accept.SourceEvents = accepted
	if _, err := store.Apply(accept); err != nil {
		t.Fatal(err)
	}
	checkpoint := assimilationCommand(jobID, "command-checkpoint", agent.AssimilationJobCommandSaveCheckpoint, 2)
	checkpoint.Checkpoint = &agent.AssimilationCheckpoint{
		Version: agent.AssimilationCheckpointVersion, Ordinal: 1, SourceSequence: 4,
		Stage: "drafting", Cursor: "extent:3", StateSHA256: "sha256:" + strings.Repeat("c", 64),
	}
	receipt, err := store.Apply(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	return store, receipt.Snapshot
}

func branchIssueCommand(
	jobID, commandID, branchID, issueID string,
	expectedBranchRevision uint64,
) agent.AssimilationJobCommand {
	command := assimilationCommand(jobID, commandID, agent.AssimilationJobCommandRaiseBranchIssue, 0)
	command.ExpectedBranchRevision = expectedBranchRevision
	command.BranchIssue = &agent.AssimilationBranchIssue{
		Version: agent.AssimilationBranchIssueVersion, BranchID: branchID, IssueID: issueID,
		ExtentSequence: 3, ExtentSHA256: "sha256:" + strings.Repeat("d", 64),
		ClaimIDs: []string{"claim-3"}, Code: "AMBIGUOUS_FACT",
		Message: "Two interpretations are supported", Question: "How should this branch proceed?",
		Options: []string{"merge", "keep_separate"},
	}
	return command
}

func branchAnswerCommand(
	jobID, commandID, branchID, issueID string,
	expectedBranchRevision uint64,
	selectedOption, privateAnswer string,
) agent.AssimilationJobCommand {
	digest := sha256.Sum256([]byte(privateAnswer))
	command := assimilationCommand(jobID, commandID, agent.AssimilationJobCommandAnswerBranchIssue, 0)
	command.ExpectedBranchRevision = expectedBranchRevision
	command.BranchAnswer = &agent.AssimilationBranchAnswer{
		Version: agent.AssimilationBranchAnswerVersion, BranchID: branchID, IssueID: issueID,
		SelectedOption: selectedOption, AnswerUTF8Bytes: uint64(len(privateAnswer)),
		AnswerSHA256: "sha256:" + hex.EncodeToString(digest[:]),
	}
	return command
}

func assertBranchJobProgressUnchanged(
	t *testing.T,
	want agent.AssimilationJobSnapshot,
	got agent.AssimilationJobSnapshot,
) {
	t.Helper()
	if !reflect.DeepEqual(got.Intake, want.Intake) || !reflect.DeepEqual(got.Checkpoint, want.Checkpoint) ||
		got.LastSourceSequence != want.LastSourceSequence {
		t.Fatalf("branch transition changed progress: got %#v, want %#v", got, want)
	}
}
