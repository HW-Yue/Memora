package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"
)

const (
	SourceInventoryVersion   = "memora.source-inventory/v1"
	SourceIntakeEventVersion = "memora.source-intake-event/v1"
	SourceIntakeStateVersion = "memora.source-intake-state/v1"
)

var (
	ErrInvalidSourceIntake  = errors.New("Source Intake input is invalid")
	ErrSourceIntakeState    = errors.New("Source Intake state does not permit this command")
	ErrSourceIntakeQuestion = errors.New(
		"Source Intake question does not match the outstanding question",
	)
)

type SourceIntakeState string

const (
	SourceIntakeNew         SourceIntakeState = "new"
	SourceIntakeWaitingUser SourceIntakeState = "waiting_user"
	SourceIntakeAccepted    SourceIntakeState = "accepted"
	SourceIntakeCancelled   SourceIntakeState = "cancelled"
)

type SourceDescriptor struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	Locator       string `json:"locator"`
	ContentSHA256 string `json:"content_sha256"`
	Kind          string `json:"kind"`
}

type SourceUnit struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Extent   uint64 `json:"extent,omitempty"`
	Optional bool   `json:"optional,omitempty"`
}

type SourceInventory struct {
	Version string           `json:"version"`
	Source  SourceDescriptor `json:"source"`
	Units   []SourceUnit     `json:"units"`
}

type SourceSelectionMode string

const (
	SourceSelectionAll      SourceSelectionMode = "all"
	SourceSelectionSelected SourceSelectionMode = "selected"
)

type SourceSelection struct {
	Mode            SourceSelectionMode `json:"mode"`
	UnitIDs         []string            `json:"unit_ids,omitempty"`
	TargetDatabases []string            `json:"target_databases"`
}

type SourceQuestionKind string

const (
	SourceQuestionScope SourceQuestionKind = "scope"
	SourceQuestionIssue SourceQuestionKind = "issue"
)

type SourceQuestion struct {
	ID      string             `json:"id"`
	Kind    SourceQuestionKind `json:"kind"`
	Prompt  string             `json:"prompt"`
	Options []string           `json:"options,omitempty"`
	Code    string             `json:"code,omitempty"`
}

type SourceProgress struct {
	Completed uint64 `json:"completed"`
	Total     uint64 `json:"total"`
	UnitID    string `json:"unit_id,omitempty"`
}

type SourceIssue struct {
	Code         string   `json:"code"`
	Message      string   `json:"message"`
	RequiresUser bool     `json:"requires_user"`
	QuestionID   string   `json:"question_id,omitempty"`
	Prompt       string   `json:"prompt,omitempty"`
	Options      []string `json:"options,omitempty"`
}

type SourceAnswerReceipt struct {
	UTF8Bytes uint64 `json:"utf8_bytes"`
	SHA256    string `json:"sha256"`
}

type SourceCancellation struct {
	Reason string `json:"reason"`
}

type SourceIntakeEventKind string

const (
	SourceIntakeEventInventoryReady SourceIntakeEventKind = "inventory_ready"
	SourceIntakeEventQuestion       SourceIntakeEventKind = "question"
	SourceIntakeEventWaitingUser    SourceIntakeEventKind = "waiting_user"
	SourceIntakeEventAccepted       SourceIntakeEventKind = "intake_accepted"
	SourceIntakeEventProgress       SourceIntakeEventKind = "progress"
	SourceIntakeEventIssue          SourceIntakeEventKind = "issue"
	SourceIntakeEventAnswerReceived SourceIntakeEventKind = "answer_received"
	SourceIntakeEventResumed        SourceIntakeEventKind = "intake_resumed"
	SourceIntakeEventCancelled      SourceIntakeEventKind = "cancelled"
)

type SourceIntakeEvent struct {
	Version      string                `json:"version"`
	TaskID       string                `json:"task_id"`
	Sequence     uint64                `json:"sequence"`
	Kind         SourceIntakeEventKind `json:"kind"`
	State        SourceIntakeState     `json:"state"`
	Inventory    *SourceInventory      `json:"inventory,omitempty"`
	Question     *SourceQuestion       `json:"question,omitempty"`
	Selection    *SourceSelection      `json:"selection,omitempty"`
	Progress     *SourceProgress       `json:"progress,omitempty"`
	Issue        *SourceIssue          `json:"issue,omitempty"`
	Answer       *SourceAnswerReceipt  `json:"answer,omitempty"`
	Cancellation *SourceCancellation   `json:"cancellation,omitempty"`
}

func (event SourceIntakeEvent) Validate() error {
	if event.Version != SourceIntakeEventVersion || !validSourceIntakeID(event.TaskID, 128) ||
		event.Sequence == 0 || !validSourceIntakeState(event.State) {
		return ErrInvalidSourceIntake
	}
	payloads := 0
	for _, present := range []bool{
		event.Inventory != nil, event.Question != nil, event.Selection != nil,
		event.Progress != nil, event.Issue != nil, event.Answer != nil,
		event.Cancellation != nil,
	} {
		if present {
			payloads++
		}
	}
	switch event.Kind {
	case SourceIntakeEventInventoryReady:
		if payloads != 1 || event.Inventory == nil || !validSourceInventory(*event.Inventory) ||
			event.State != SourceIntakeWaitingUser {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventQuestion:
		if payloads != 1 || event.Question == nil || !validSourceQuestion(*event.Question) ||
			event.State != SourceIntakeWaitingUser {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventWaitingUser:
		if payloads != 0 || event.State != SourceIntakeWaitingUser {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventAccepted:
		if payloads != 1 || event.Selection == nil || !validSourceSelectionShape(*event.Selection) ||
			event.State != SourceIntakeAccepted {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventProgress:
		if payloads != 1 || event.Progress == nil || !validSourceProgressShape(*event.Progress) ||
			event.State != SourceIntakeAccepted {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventIssue:
		if payloads != 1 || event.Issue == nil || !validSourceIssue(*event.Issue) ||
			(event.State != SourceIntakeAccepted && event.State != SourceIntakeWaitingUser) ||
			(event.Issue.RequiresUser != (event.State == SourceIntakeWaitingUser)) {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventAnswerReceived:
		if payloads != 1 || event.Answer == nil || event.Answer.UTF8Bytes == 0 ||
			!validSourceSHA256(event.Answer.SHA256) || event.State != SourceIntakeAccepted {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventResumed:
		if payloads != 0 || event.State != SourceIntakeAccepted {
			return ErrInvalidSourceIntake
		}
	case SourceIntakeEventCancelled:
		if payloads != 1 || event.Cancellation == nil ||
			!validSourceIntakeText(event.Cancellation.Reason, 1000) ||
			event.State != SourceIntakeCancelled {
			return ErrInvalidSourceIntake
		}
	default:
		return ErrInvalidSourceIntake
	}
	return nil
}

type SourceIntakeSnapshot struct {
	Version   string            `json:"version"`
	TaskID    string            `json:"task_id"`
	State     SourceIntakeState `json:"state"`
	Sequence  uint64            `json:"sequence"`
	Inventory SourceInventory   `json:"inventory"`
	Selection *SourceSelection  `json:"selection,omitempty"`
	Question  *SourceQuestion   `json:"question,omitempty"`
	Progress  SourceProgress    `json:"progress"`
}

type SourceIntake struct {
	mu        sync.Mutex
	taskID    string
	state     SourceIntakeState
	sequence  uint64
	inventory SourceInventory
	selection *SourceSelection
	question  *SourceQuestion
	progress  SourceProgress
}

func NewSourceIntake(taskID string) (*SourceIntake, error) {
	if !validSourceIntakeID(taskID, 128) {
		return nil, ErrInvalidSourceIntake
	}
	return &SourceIntake{taskID: taskID, state: SourceIntakeNew}, nil
}

func (session *SourceIntake) Begin(inventory SourceInventory) ([]SourceIntakeEvent, error) {
	if session == nil || !validSourceInventory(inventory) {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeNew {
		return nil, ErrSourceIntakeState
	}
	question := SourceQuestion{
		ID: "scope-selection", Kind: SourceQuestionScope,
		Prompt:  "Absorb all units or selected units, and which databases should receive them?",
		Options: []string{string(SourceSelectionAll), string(SourceSelectionSelected)},
	}
	session.inventory = cloneSourceInventory(inventory)
	session.question = cloneSourceQuestionPointer(&question)
	session.state = SourceIntakeWaitingUser
	events := session.eventsLocked(
		sourceIntakeEventPayload{kind: SourceIntakeEventInventoryReady, inventory: &inventory},
		sourceIntakeEventPayload{kind: SourceIntakeEventQuestion, question: &question},
		sourceIntakeEventPayload{kind: SourceIntakeEventWaitingUser},
	)
	return events, nil
}

func (session *SourceIntake) Confirm(selection SourceSelection) ([]SourceIntakeEvent, error) {
	if session == nil {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeWaitingUser || session.question == nil ||
		session.question.Kind != SourceQuestionScope {
		return nil, ErrSourceIntakeState
	}
	if !validSourceSelection(selection, session.inventory) {
		return nil, ErrInvalidSourceIntake
	}
	cloned := cloneSourceSelection(selection)
	session.selection = &cloned
	session.question = nil
	session.state = SourceIntakeAccepted
	return session.eventsLocked(sourceIntakeEventPayload{
		kind: SourceIntakeEventAccepted, selection: &cloned,
	}), nil
}

func (session *SourceIntake) Progress(progress SourceProgress) ([]SourceIntakeEvent, error) {
	if session == nil {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeAccepted {
		return nil, ErrSourceIntakeState
	}
	if !validSourceProgress(progress, session.inventory, session.progress) {
		return nil, ErrSourceIntakeState
	}
	session.progress = progress
	return session.eventsLocked(sourceIntakeEventPayload{
		kind: SourceIntakeEventProgress, progress: &progress,
	}), nil
}

func (session *SourceIntake) Issue(issue SourceIssue) ([]SourceIntakeEvent, error) {
	if session == nil || !validSourceIssue(issue) {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeAccepted {
		return nil, ErrSourceIntakeState
	}
	if !issue.RequiresUser {
		return session.eventsLocked(sourceIntakeEventPayload{
			kind: SourceIntakeEventIssue, issue: &issue,
		}), nil
	}
	question := SourceQuestion{
		ID: issue.QuestionID, Kind: SourceQuestionIssue, Prompt: issue.Prompt,
		Options: append([]string(nil), issue.Options...), Code: issue.Code,
	}
	session.question = cloneSourceQuestionPointer(&question)
	session.state = SourceIntakeWaitingUser
	return session.eventsLocked(
		sourceIntakeEventPayload{kind: SourceIntakeEventIssue, issue: &issue},
		sourceIntakeEventPayload{kind: SourceIntakeEventQuestion, question: &question},
		sourceIntakeEventPayload{kind: SourceIntakeEventWaitingUser},
	), nil
}

func (session *SourceIntake) Answer(questionID, value string) ([]SourceIntakeEvent, error) {
	if session == nil {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeWaitingUser || session.question == nil ||
		session.question.Kind != SourceQuestionIssue || session.question.ID != questionID {
		return nil, ErrSourceIntakeQuestion
	}
	if !validSourceIntakeText(value, 4096) {
		return nil, ErrInvalidSourceIntake
	}
	digest := sha256.Sum256([]byte(value))
	receipt := SourceAnswerReceipt{
		UTF8Bytes: uint64(len([]byte(value))),
		SHA256:    "sha256:" + hex.EncodeToString(digest[:]),
	}
	session.question = nil
	session.state = SourceIntakeAccepted
	return session.eventsLocked(
		sourceIntakeEventPayload{kind: SourceIntakeEventAnswerReceived, answer: &receipt},
		sourceIntakeEventPayload{kind: SourceIntakeEventResumed},
	), nil
}

func (session *SourceIntake) Cancel(reason string) ([]SourceIntakeEvent, error) {
	if session == nil || !validSourceIntakeText(reason, 1000) {
		return nil, ErrInvalidSourceIntake
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.state != SourceIntakeWaitingUser && session.state != SourceIntakeAccepted {
		return nil, ErrSourceIntakeState
	}
	cancellation := SourceCancellation{Reason: reason}
	session.question = nil
	session.state = SourceIntakeCancelled
	return session.eventsLocked(sourceIntakeEventPayload{
		kind: SourceIntakeEventCancelled, cancellation: &cancellation,
	}), nil
}

func (session *SourceIntake) Snapshot() SourceIntakeSnapshot {
	if session == nil {
		return SourceIntakeSnapshot{}
	}
	session.mu.Lock()
	defer session.mu.Unlock()
	return SourceIntakeSnapshot{
		Version: SourceIntakeStateVersion, TaskID: session.taskID, State: session.state,
		Sequence: session.sequence, Inventory: cloneSourceInventory(session.inventory),
		Selection: cloneSourceSelectionPointer(session.selection),
		Question:  cloneSourceQuestionPointer(session.question), Progress: session.progress,
	}
}

type sourceIntakeEventPayload struct {
	kind         SourceIntakeEventKind
	inventory    *SourceInventory
	question     *SourceQuestion
	selection    *SourceSelection
	progress     *SourceProgress
	issue        *SourceIssue
	answer       *SourceAnswerReceipt
	cancellation *SourceCancellation
}

func (session *SourceIntake) eventsLocked(payloads ...sourceIntakeEventPayload) []SourceIntakeEvent {
	events := make([]SourceIntakeEvent, len(payloads))
	for index, payload := range payloads {
		session.sequence++
		events[index] = SourceIntakeEvent{
			Version: SourceIntakeEventVersion, TaskID: session.taskID, Sequence: session.sequence,
			Kind: payload.kind, State: session.state,
			Inventory:    cloneSourceInventoryPointer(payload.inventory),
			Question:     cloneSourceQuestionPointer(payload.question),
			Selection:    cloneSourceSelectionPointer(payload.selection),
			Progress:     cloneSourceProgressPointer(payload.progress),
			Issue:        cloneSourceIssuePointer(payload.issue),
			Answer:       cloneSourceAnswerPointer(payload.answer),
			Cancellation: cloneSourceCancellationPointer(payload.cancellation),
		}
	}
	return events
}

func validSourceInventory(inventory SourceInventory) bool {
	if inventory.Version != SourceInventoryVersion ||
		!validSourceIntakeID(inventory.Source.ID, 128) ||
		!validSourceIntakeText(inventory.Source.Title, 1000) ||
		!validSourceIntakeText(inventory.Source.Locator, 4096) ||
		!validSourceSHA256(inventory.Source.ContentSHA256) ||
		!validSourceIntakeID(inventory.Source.Kind, 64) || len(inventory.Units) == 0 ||
		len(inventory.Units) > 100000 {
		return false
	}
	units := make(map[string]SourceUnit, len(inventory.Units))
	roots := 0
	sourceUnits := 0
	readableRequired := false
	for _, unit := range inventory.Units {
		if !validSourceIntakeID(unit.ID, 160) || !validSourceIntakeID(unit.Kind, 64) ||
			!validSourceIntakeText(unit.Label, 1000) {
			return false
		}
		if unit.ParentID != "" && !validSourceIntakeID(unit.ParentID, 160) {
			return false
		}
		if _, duplicate := units[unit.ID]; duplicate {
			return false
		}
		if unit.ParentID == "" {
			roots++
		}
		if unit.Kind == "source" {
			sourceUnits++
			if unit.ParentID != "" {
				return false
			}
		}
		if unit.Extent > 0 && !unit.Optional {
			readableRequired = true
		}
		units[unit.ID] = unit
	}
	if roots != 1 || sourceUnits != 1 || !readableRequired {
		return false
	}
	for _, unit := range inventory.Units {
		if unit.ParentID != "" {
			if _, exists := units[unit.ParentID]; !exists || unit.ParentID == unit.ID {
				return false
			}
		}
		seen := map[string]struct{}{unit.ID: {}}
		parent := unit.ParentID
		for parent != "" {
			if _, cycle := seen[parent]; cycle {
				return false
			}
			seen[parent] = struct{}{}
			parent = units[parent].ParentID
		}
	}
	return true
}

func validSourceSelection(selection SourceSelection, inventory SourceInventory) bool {
	if !validSourceSelectionShape(selection) {
		return false
	}
	known := make(map[string]struct{}, len(inventory.Units))
	for _, unit := range inventory.Units {
		known[unit.ID] = struct{}{}
	}
	seen := make(map[string]struct{}, len(selection.UnitIDs))
	for _, unitID := range selection.UnitIDs {
		if _, exists := known[unitID]; !exists {
			return false
		}
		if _, duplicate := seen[unitID]; duplicate {
			return false
		}
		seen[unitID] = struct{}{}
	}
	return true
}

func validSourceSelectionShape(selection SourceSelection) bool {
	switch selection.Mode {
	case SourceSelectionAll:
		if len(selection.UnitIDs) != 0 {
			return false
		}
	case SourceSelectionSelected:
		if len(selection.UnitIDs) == 0 || len(selection.UnitIDs) > 100000 {
			return false
		}
	default:
		return false
	}
	if len(selection.TargetDatabases) == 0 || len(selection.TargetDatabases) > 256 {
		return false
	}
	seen := make(map[string]struct{}, len(selection.TargetDatabases))
	for _, database := range selection.TargetDatabases {
		if !validSourceIntakeText(database, 128) || strings.TrimSpace(database) != database {
			return false
		}
		canonical := strings.ToLower(database)
		if _, duplicate := seen[canonical]; duplicate {
			return false
		}
		seen[canonical] = struct{}{}
	}
	return true
}

func validSourceQuestion(question SourceQuestion) bool {
	if !validSourceIntakeID(question.ID, 160) || !validSourceIntakeText(question.Prompt, 2000) ||
		len(question.Options) > 32 {
		return false
	}
	for _, option := range question.Options {
		if !validSourceIntakeText(option, 256) {
			return false
		}
	}
	switch question.Kind {
	case SourceQuestionScope:
		return question.Code == "" && len(question.Options) == 2
	case SourceQuestionIssue:
		return validSourceIntakeID(question.Code, 80)
	default:
		return false
	}
}

func validSourceIssue(issue SourceIssue) bool {
	if !validSourceIntakeID(issue.Code, 80) || !validSourceIntakeText(issue.Message, 2000) ||
		len(issue.Options) > 32 {
		return false
	}
	if !issue.RequiresUser {
		return issue.QuestionID == "" && issue.Prompt == "" && len(issue.Options) == 0
	}
	if !validSourceIntakeID(issue.QuestionID, 160) || !validSourceIntakeText(issue.Prompt, 2000) {
		return false
	}
	for _, option := range issue.Options {
		if !validSourceIntakeText(option, 256) {
			return false
		}
	}
	return true
}

func validSourceProgress(progress SourceProgress, inventory SourceInventory, previous SourceProgress) bool {
	if !validSourceProgressShape(progress) || progress.Completed < previous.Completed ||
		progress.Total < previous.Total {
		return false
	}
	for _, unit := range inventory.Units {
		if unit.ID == progress.UnitID {
			return true
		}
	}
	return false
}

func validSourceProgressShape(progress SourceProgress) bool {
	return progress.Total > 0 && progress.Completed <= progress.Total &&
		validSourceIntakeID(progress.UnitID, 160)
}

func validSourceIntakeState(state SourceIntakeState) bool {
	switch state {
	case SourceIntakeNew, SourceIntakeWaitingUser, SourceIntakeAccepted, SourceIntakeCancelled:
		return true
	default:
		return false
	}
}

func validSourceIntakeID(value string, limit int) bool {
	return validSourceIntakeText(value, limit) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\r\n\t")
}

func validSourceIntakeText(value string, limit int) bool {
	return value != "" && len(value) <= limit && utf8.ValidString(value) && strings.TrimSpace(value) != ""
}

func validSourceSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func cloneSourceInventory(value SourceInventory) SourceInventory {
	value.Units = append([]SourceUnit(nil), value.Units...)
	return value
}

func cloneSourceInventoryPointer(value *SourceInventory) *SourceInventory {
	if value == nil {
		return nil
	}
	cloned := cloneSourceInventory(*value)
	return &cloned
}

func cloneSourceSelection(value SourceSelection) SourceSelection {
	value.UnitIDs = append([]string(nil), value.UnitIDs...)
	value.TargetDatabases = append([]string(nil), value.TargetDatabases...)
	return value
}

func cloneSourceSelectionPointer(value *SourceSelection) *SourceSelection {
	if value == nil {
		return nil
	}
	cloned := cloneSourceSelection(*value)
	return &cloned
}

func cloneSourceQuestionPointer(value *SourceQuestion) *SourceQuestion {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Options = append([]string(nil), value.Options...)
	return &cloned
}

func cloneSourceProgressPointer(value *SourceProgress) *SourceProgress {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSourceIssuePointer(value *SourceIssue) *SourceIssue {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Options = append([]string(nil), value.Options...)
	return &cloned
}

func cloneSourceAnswerPointer(value *SourceAnswerReceipt) *SourceAnswerReceipt {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneSourceCancellationPointer(value *SourceCancellation) *SourceCancellation {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
