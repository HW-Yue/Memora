package agent

import (
	"errors"
	"reflect"
)

const (
	AssimilationJobCommandVersion  = "memora.assimilation-job-command/v1"
	AssimilationCheckpointVersion  = "memora.assimilation-checkpoint/v1"
	AssimilationJobEventVersion    = "memora.assimilation-job-event/v1"
	AssimilationJobSnapshotVersion = "memora.assimilation-job-snapshot/v1"
)

var (
	ErrAssimilationJobInvalid         = errors.New("Assimilation Job input is invalid")
	ErrAssimilationJobNotFound        = errors.New("Assimilation Job was not found")
	ErrAssimilationJobRevision        = errors.New("Assimilation Job revision conflict")
	ErrAssimilationJobCommandConflict = errors.New("Assimilation Job command ID has different content")
	ErrAssimilationJobTerminal        = errors.New("Assimilation Job is terminal")
	ErrAssimilationJobCorrupt         = errors.New("Assimilation Job journal is corrupt")
)

type AssimilationJobState string

const (
	AssimilationJobWaitingUser AssimilationJobState = "waiting_user"
	AssimilationJobActive      AssimilationJobState = "active"
	AssimilationJobCancelled   AssimilationJobState = "cancelled"
)

type AssimilationJobCommandKind string

const (
	AssimilationJobCommandStart              AssimilationJobCommandKind = "start"
	AssimilationJobCommandAppendSourceEvents AssimilationJobCommandKind = "append_source_events"
	AssimilationJobCommandSaveCheckpoint     AssimilationJobCommandKind = "save_checkpoint"
	AssimilationJobCommandCancel             AssimilationJobCommandKind = "cancel"
)

type AssimilationCheckpoint struct {
	Version        string `json:"version"`
	Ordinal        uint64 `json:"ordinal"`
	SourceSequence uint64 `json:"source_sequence"`
	Stage          string `json:"stage"`
	Cursor         string `json:"cursor"`
	StateSHA256    string `json:"state_sha256"`
}

type AssimilationJobCommand struct {
	Version          string                     `json:"version"`
	JobID            string                     `json:"job_id"`
	CommandID        string                     `json:"command_id"`
	Kind             AssimilationJobCommandKind `json:"kind"`
	ExpectedRevision uint64                     `json:"expected_revision"`
	SourceEvents     []SourceIntakeEvent        `json:"source_events,omitempty"`
	Checkpoint       *AssimilationCheckpoint    `json:"checkpoint,omitempty"`
	ReasonCode       string                     `json:"reason_code,omitempty"`
}

type AssimilationJobEventKind string

const (
	AssimilationJobEventStarted              AssimilationJobEventKind = "started"
	AssimilationJobEventSourceEventsAppended AssimilationJobEventKind = "source_events_appended"
	AssimilationJobEventCheckpointSaved      AssimilationJobEventKind = "checkpoint_saved"
	AssimilationJobEventCancelled            AssimilationJobEventKind = "cancelled"
)

type AssimilationJobEvent struct {
	Version       string                   `json:"version"`
	JobID         string                   `json:"job_id"`
	Revision      uint64                   `json:"revision"`
	CommandID     string                   `json:"command_id"`
	CommandSHA256 string                   `json:"command_sha256"`
	Kind          AssimilationJobEventKind `json:"kind"`
	State         AssimilationJobState     `json:"state"`
	SourceEvents  []SourceIntakeEvent      `json:"source_events,omitempty"`
	Checkpoint    *AssimilationCheckpoint  `json:"checkpoint,omitempty"`
	ReasonCode    string                   `json:"reason_code,omitempty"`
}

func (event AssimilationJobEvent) Validate() error {
	if event.Version != AssimilationJobEventVersion || !validSourceIntakeID(event.JobID, 128) ||
		!validSourceIntakeID(event.CommandID, 160) || !validSourceSHA256(event.CommandSHA256) ||
		event.Revision == 0 || !validAssimilationJobState(event.State) {
		return ErrAssimilationJobInvalid
	}
	switch event.Kind {
	case AssimilationJobEventStarted:
		if event.Revision != 1 || event.State != AssimilationJobWaitingUser ||
			!validInitialSourceBatch(event.JobID, event.SourceEvents) || event.Checkpoint != nil ||
			event.ReasonCode != "" {
			return ErrAssimilationJobInvalid
		}
	case AssimilationJobEventSourceEventsAppended:
		if !validStandaloneSourceBatch(event.JobID, event.SourceEvents) || event.Checkpoint != nil ||
			event.ReasonCode != "" || event.State != jobStateFromSource(event.SourceEvents[len(event.SourceEvents)-1].State) {
			return ErrAssimilationJobInvalid
		}
	case AssimilationJobEventCheckpointSaved:
		if len(event.SourceEvents) != 0 || event.Checkpoint == nil ||
			!validAssimilationCheckpoint(*event.Checkpoint) || event.ReasonCode != "" ||
			event.State == AssimilationJobCancelled {
			return ErrAssimilationJobInvalid
		}
	case AssimilationJobEventCancelled:
		if len(event.SourceEvents) != 0 || event.Checkpoint != nil ||
			!validSourceIntakeID(event.ReasonCode, 80) || event.State != AssimilationJobCancelled {
			return ErrAssimilationJobInvalid
		}
	default:
		return ErrAssimilationJobInvalid
	}
	return nil
}

type AssimilationJobSnapshot struct {
	Version            string                  `json:"version"`
	JobID              string                  `json:"job_id"`
	Revision           uint64                  `json:"revision"`
	State              AssimilationJobState    `json:"state"`
	LastSourceSequence uint64                  `json:"last_source_sequence"`
	Intake             SourceIntakeSnapshot    `json:"intake"`
	Checkpoint         *AssimilationCheckpoint `json:"checkpoint,omitempty"`
}

type AssimilationJobReceipt struct {
	Event    AssimilationJobEvent    `json:"event"`
	Snapshot AssimilationJobSnapshot `json:"snapshot"`
	Replayed bool                    `json:"replayed"`
}

func validAssimilationJobCommand(command AssimilationJobCommand) bool {
	if command.Version != AssimilationJobCommandVersion ||
		!validSourceIntakeID(command.JobID, 128) || !validSourceIntakeID(command.CommandID, 160) {
		return false
	}
	switch command.Kind {
	case AssimilationJobCommandStart:
		return command.ExpectedRevision == 0 && validInitialSourceBatch(command.JobID, command.SourceEvents) &&
			command.Checkpoint == nil && command.ReasonCode == ""
	case AssimilationJobCommandAppendSourceEvents:
		return validStandaloneSourceBatch(command.JobID, command.SourceEvents) &&
			command.Checkpoint == nil && command.ReasonCode == ""
	case AssimilationJobCommandSaveCheckpoint:
		return len(command.SourceEvents) == 0 && command.Checkpoint != nil &&
			validAssimilationCheckpoint(*command.Checkpoint) && command.ReasonCode == ""
	case AssimilationJobCommandCancel:
		return len(command.SourceEvents) == 0 && command.Checkpoint == nil &&
			validSourceIntakeID(command.ReasonCode, 80)
	default:
		return false
	}
}

func validAssimilationCheckpoint(checkpoint AssimilationCheckpoint) bool {
	return checkpoint.Version == AssimilationCheckpointVersion && checkpoint.Ordinal > 0 &&
		checkpoint.SourceSequence > 0 && validSourceIntakeID(checkpoint.Stage, 120) &&
		validSourceIntakeID(checkpoint.Cursor, 256) && validSourceSHA256(checkpoint.StateSHA256)
}

func validInitialSourceBatch(jobID string, events []SourceIntakeEvent) bool {
	return len(events) == 3 && events[0].Kind == SourceIntakeEventInventoryReady &&
		events[1].Kind == SourceIntakeEventQuestion && events[2].Kind == SourceIntakeEventWaitingUser &&
		events[0].Sequence == 1 && validStandaloneSourceBatch(jobID, events)
}

func validStandaloneSourceBatch(jobID string, events []SourceIntakeEvent) bool {
	if len(events) == 0 || len(events) > 64 {
		return false
	}
	for index, event := range events {
		if event.TaskID != jobID || event.Validate() != nil ||
			(index > 0 && event.Sequence != events[index-1].Sequence+1) {
			return false
		}
	}
	switch len(events) {
	case 1:
		switch events[0].Kind {
		case SourceIntakeEventAccepted, SourceIntakeEventProgress, SourceIntakeEventIssue,
			SourceIntakeEventCancelled:
			return true
		}
	case 2:
		return events[0].Kind == SourceIntakeEventAnswerReceived &&
			events[1].Kind == SourceIntakeEventResumed
	case 3:
		return (events[0].Kind == SourceIntakeEventInventoryReady &&
			events[1].Kind == SourceIntakeEventQuestion &&
			events[2].Kind == SourceIntakeEventWaitingUser) ||
			(events[0].Kind == SourceIntakeEventIssue &&
				events[1].Kind == SourceIntakeEventQuestion &&
				events[2].Kind == SourceIntakeEventWaitingUser)
	}
	return false
}

func validSourceBatchTransition(snapshot AssimilationJobSnapshot, events []SourceIntakeEvent) bool {
	if !validStandaloneSourceBatch(snapshot.JobID, events) ||
		events[0].Sequence != snapshot.LastSourceSequence+1 || snapshot.Intake.Validate() != nil {
		return false
	}
	firstKind := events[0].Kind
	switch snapshot.State {
	case AssimilationJobWaitingUser:
		switch snapshot.Intake.Question.Kind {
		case SourceQuestionScope:
			return firstKind == SourceIntakeEventAccepted || firstKind == SourceIntakeEventCancelled
		case SourceQuestionIssue:
			return firstKind == SourceIntakeEventAnswerReceived || firstKind == SourceIntakeEventCancelled
		default:
			return false
		}
	case AssimilationJobActive:
		return firstKind == SourceIntakeEventProgress || firstKind == SourceIntakeEventIssue ||
			firstKind == SourceIntakeEventCancelled
	default:
		return false
	}
}

func validAssimilationJobState(state AssimilationJobState) bool {
	switch state {
	case AssimilationJobWaitingUser, AssimilationJobActive, AssimilationJobCancelled:
		return true
	default:
		return false
	}
}

func jobStateFromSource(state SourceIntakeState) AssimilationJobState {
	switch state {
	case SourceIntakeWaitingUser:
		return AssimilationJobWaitingUser
	case SourceIntakeAccepted:
		return AssimilationJobActive
	case SourceIntakeCancelled:
		return AssimilationJobCancelled
	default:
		return ""
	}
}

func deriveAssimilationJobEvent(
	snapshot AssimilationJobSnapshot,
	command AssimilationJobCommand,
	commandSHA256 string,
) (AssimilationJobEvent, AssimilationJobSnapshot, error) {
	if snapshot.Revision != command.ExpectedRevision {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobRevision
	}
	if snapshot.State == AssimilationJobCancelled {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobTerminal
	}
	event := AssimilationJobEvent{
		Version: AssimilationJobEventVersion, JobID: command.JobID,
		Revision: snapshot.Revision + 1, CommandID: command.CommandID,
		CommandSHA256: commandSHA256,
	}
	next := cloneAssimilationJobSnapshot(snapshot)
	if snapshot.Revision == 0 {
		if command.Kind != AssimilationJobCommandStart {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobNotFound
		}
		event.Kind = AssimilationJobEventStarted
		event.State = AssimilationJobWaitingUser
		event.SourceEvents = cloneSourceIntakeEvents(command.SourceEvents)
		intake, ok := sourceIntakeSnapshotFromInitial(command.SourceEvents)
		if !ok {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
		}
		next = AssimilationJobSnapshot{
			Version: AssimilationJobSnapshotVersion, JobID: command.JobID, Revision: 1,
			State:              AssimilationJobWaitingUser,
			LastSourceSequence: command.SourceEvents[len(command.SourceEvents)-1].Sequence,
			Intake:             intake,
		}
		return event, next, nil
	}
	if command.Kind == AssimilationJobCommandStart || snapshot.JobID != command.JobID {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobRevision
	}
	switch command.Kind {
	case AssimilationJobCommandAppendSourceEvents:
		if !validSourceBatchTransition(snapshot, command.SourceEvents) {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
		}
		event.Kind = AssimilationJobEventSourceEventsAppended
		event.SourceEvents = cloneSourceIntakeEvents(command.SourceEvents)
		event.State = jobStateFromSource(command.SourceEvents[len(command.SourceEvents)-1].State)
		intake, ok := applySourceEventsToSnapshot(snapshot.Intake, command.SourceEvents)
		if !ok {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
		}
		next.State = event.State
		next.LastSourceSequence = command.SourceEvents[len(command.SourceEvents)-1].Sequence
		next.Intake = intake
	case AssimilationJobCommandSaveCheckpoint:
		wantOrdinal := uint64(1)
		if snapshot.Checkpoint != nil {
			wantOrdinal = snapshot.Checkpoint.Ordinal + 1
		}
		if command.Checkpoint.SourceSequence != snapshot.LastSourceSequence ||
			command.Checkpoint.Ordinal != wantOrdinal {
			return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
		}
		event.Kind = AssimilationJobEventCheckpointSaved
		event.State = snapshot.State
		event.Checkpoint = cloneAssimilationCheckpointPointer(command.Checkpoint)
		next.Checkpoint = cloneAssimilationCheckpointPointer(command.Checkpoint)
	case AssimilationJobCommandCancel:
		event.Kind = AssimilationJobEventCancelled
		event.State = AssimilationJobCancelled
		event.ReasonCode = command.ReasonCode
		next.State = AssimilationJobCancelled
	default:
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	next.Revision = event.Revision
	if event.Validate() != nil {
		return AssimilationJobEvent{}, AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	return event, next, nil
}

func cloneAssimilationJobCommand(command AssimilationJobCommand) AssimilationJobCommand {
	command.SourceEvents = cloneSourceIntakeEvents(command.SourceEvents)
	command.Checkpoint = cloneAssimilationCheckpointPointer(command.Checkpoint)
	return command
}

func cloneAssimilationJobEvent(event AssimilationJobEvent) AssimilationJobEvent {
	event.SourceEvents = cloneSourceIntakeEvents(event.SourceEvents)
	event.Checkpoint = cloneAssimilationCheckpointPointer(event.Checkpoint)
	return event
}

func cloneAssimilationJobSnapshot(snapshot AssimilationJobSnapshot) AssimilationJobSnapshot {
	snapshot.Intake = cloneSourceIntakeSnapshot(snapshot.Intake)
	snapshot.Checkpoint = cloneAssimilationCheckpointPointer(snapshot.Checkpoint)
	return snapshot
}

func sourceIntakeSnapshotFromInitial(events []SourceIntakeEvent) (SourceIntakeSnapshot, bool) {
	if len(events) != 3 || events[0].Inventory == nil || events[1].Question == nil {
		return SourceIntakeSnapshot{}, false
	}
	snapshot := SourceIntakeSnapshot{
		Version: SourceIntakeStateVersion, TaskID: events[0].TaskID,
		State: events[2].State, Sequence: events[2].Sequence,
		Inventory: cloneSourceInventory(*events[0].Inventory),
		Question:  cloneSourceQuestionPointer(events[1].Question),
	}
	return snapshot, snapshot.Validate() == nil
}

func applySourceEventsToSnapshot(
	snapshot SourceIntakeSnapshot,
	events []SourceIntakeEvent,
) (SourceIntakeSnapshot, bool) {
	next := cloneSourceIntakeSnapshot(snapshot)
	for _, event := range events {
		next.State = event.State
		next.Sequence = event.Sequence
		switch event.Kind {
		case SourceIntakeEventQuestion:
			next.Question = cloneSourceQuestionPointer(event.Question)
		case SourceIntakeEventAccepted:
			next.Selection = cloneSourceSelectionPointer(event.Selection)
			next.Question = nil
		case SourceIntakeEventProgress:
			if event.Progress == nil ||
				!validSourceProgress(*event.Progress, next.Inventory, next.Progress) {
				return SourceIntakeSnapshot{}, false
			}
			next.Progress = *event.Progress
		case SourceIntakeEventResumed, SourceIntakeEventCancelled:
			next.Question = nil
		}
	}
	if next.Validate() != nil {
		return SourceIntakeSnapshot{}, false
	}
	return next, true
}

func cloneAssimilationCheckpointPointer(value *AssimilationCheckpoint) *AssimilationCheckpoint {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSourceIntakeEvents(events []SourceIntakeEvent) []SourceIntakeEvent {
	if events == nil {
		return nil
	}
	cloned := make([]SourceIntakeEvent, len(events))
	for index, event := range events {
		cloned[index] = event
		cloned[index].Inventory = cloneSourceInventoryPointer(event.Inventory)
		cloned[index].Question = cloneSourceQuestionPointer(event.Question)
		cloned[index].Selection = cloneSourceSelectionPointer(event.Selection)
		cloned[index].Progress = cloneSourceProgressPointer(event.Progress)
		cloned[index].Issue = cloneSourceIssuePointer(event.Issue)
		cloned[index].Answer = cloneSourceAnswerPointer(event.Answer)
		cloned[index].Cancellation = cloneSourceCancellationPointer(event.Cancellation)
	}
	return cloned
}

func equalAssimilationJobEvent(left, right AssimilationJobEvent) bool {
	return reflect.DeepEqual(left, right)
}
