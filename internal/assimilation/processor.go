package assimilation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	taskBucket        = "assimilation.tasks.v1"
	eventBucket       = "assimilation.events.v1"
	maximumUnits      = 2048
	maximumUnread     = 128
	maximumUnitExtent = uint64(1_000_000_000)
)

type taskState struct {
	Version         string                `json:"version"`
	TaskID          string                `json:"task_id"`
	Workspace       string                `json:"workspace"`
	Revision        uint64                `json:"revision"`
	Inventory       Inventory             `json:"inventory"`
	Coverage        map[string][]interval `json:"coverage"`
	Windows         map[string]string     `json:"windows"`
	Checkpoint      *Checkpoint           `json:"checkpoint,omitempty"`
	Complete        bool                  `json:"complete"`
	ReviewChallenge string                `json:"review_challenge,omitempty"`
}

type eventRecord struct {
	Digest       string  `json:"digest"`
	Kind         Kind    `json:"kind"`
	TaskID       string  `json:"task_id"`
	WindowUnitID string  `json:"window_unit_id,omitempty"`
	WindowEnd    uint64  `json:"window_end,omitempty"`
	Receipt      Receipt `json:"receipt"`
}

type Processor struct{ store store.Store }

func New(database store.Store) *Processor { return &Processor{store: database} }

func (processor *Processor) Process(ctx context.Context, event Event) (Receipt, error) {
	if err := validateEvent(event); err != nil {
		return Receipt{}, err
	}
	if processor == nil || processor.store == nil {
		return Receipt{}, assimilationError(result.CodeInternal, "coverage processor is not configured")
	}
	digest := eventDigest(event)
	tx, err := processor.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Receipt{}, assimilationError(result.CodeInternal, "begin coverage event: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replayed, found, err := replayEvent(ctx, tx, event.EventID, digest); err != nil {
		return Receipt{}, err
	} else if found {
		return replayed, nil
	}

	var state taskState
	if event.Kind == KindInventory {
		if _, err := tx.Get(ctx, taskBucket, event.TaskID); err == nil {
			return Receipt{}, assimilationError(result.CodeRevisionConflict, "task %q already exists", event.TaskID)
		} else if !errors.Is(err, store.ErrNotFound) {
			return Receipt{}, assimilationError(result.CodeInternal, "read coverage task: %v", err)
		}
		if err := validateInventory(*event.Inventory); err != nil {
			return Receipt{}, err
		}
		state = taskState{
			Version: EventVersion, TaskID: event.TaskID, Workspace: event.Workspace, Revision: 1,
			Inventory: *event.Inventory, Coverage: map[string][]interval{}, Windows: map[string]string{},
		}
	} else {
		state, err = loadTask(ctx, tx, event.TaskID)
		if err != nil {
			return Receipt{}, err
		}
		if state.Workspace != event.Workspace {
			return Receipt{}, assimilationError(result.CodePermissionDenied, "task workspace does not match the event")
		}
	}

	status := statusForState(state)
	duplicateWindow := false
	writeTask := event.Kind == KindInventory
	switch event.Kind {
	case KindInventory:
	case KindWindow:
		if state.Complete {
			return Receipt{}, assimilationError(result.CodeRevisionConflict, "coverage-complete task is frozen until clear")
		}
		unit, found := findUnit(state.Inventory.Units, event.Window.UnitID)
		if !found || unit.Extent == 0 || event.Window.Start >= event.Window.End || event.Window.End > unit.Extent ||
			!validDigest(event.Window.Fingerprint) {
			return Receipt{}, assimilationError(result.CodeValidation, "window range, unit, or fingerprint is invalid")
		}
		signature := windowSignature(*event.Window)
		if fingerprint, exists := state.Windows[signature]; exists {
			if fingerprint != event.Window.Fingerprint {
				return Receipt{}, assimilationError(result.CodeRevisionConflict, "window %s changed fingerprint", signature)
			}
			duplicateWindow = true
			break
		}
		if err := requireRevision(event.ExpectedRevision, state.Revision); err != nil {
			return Receipt{}, err
		}
		merged, changed := mergeCoverage(state.Coverage[unit.ID], interval{Start: event.Window.Start, End: event.Window.End})
		if !changed {
			duplicateWindow = true
			break
		}
		state.Coverage[unit.ID] = merged
		state.Windows[signature] = event.Window.Fingerprint
		state.Revision++
		writeTask = true
	case KindCheckpoint:
		if state.Complete {
			return Receipt{}, assimilationError(result.CodeRevisionConflict, "coverage-complete task is frozen until clear")
		}
		if err := requireRevision(event.ExpectedRevision, state.Revision); err != nil {
			return Receipt{}, err
		}
		if err := validateCheckpoint(ctx, tx, *event.Checkpoint, state); err != nil {
			return Receipt{}, err
		}
		checkpoint := *event.Checkpoint
		state.Checkpoint = &checkpoint
		state.Revision++
		writeTask = true
	case KindStatus:
	case KindFinish:
		if err := requireRevision(event.ExpectedRevision, state.Revision); err != nil {
			return Receipt{}, err
		}
		if len(allUnread(state)) != 0 {
			status = StatusIncomplete
			break
		}
		status = StatusCoverageComplete
		if !state.Complete {
			state.Complete = true
			state.Revision++
			state.ReviewChallenge = sha256Digest([]byte(fmt.Sprintf(
				"%s\x00%s\x00%d\x00%s\x00%s",
				state.TaskID, event.EventID, state.Revision,
				state.Inventory.Source.ContentHash, eventDigest(event),
			)))
			writeTask = true
		}
	case KindClear:
		if err := requireRevision(event.ExpectedRevision, state.Revision); err != nil {
			return Receipt{}, err
		}
		status = StatusCleared
		writeTask = false
		if err := tx.Delete(ctx, taskBucket, event.TaskID); err != nil {
			return Receipt{}, assimilationError(result.CodeInternal, "clear coverage task: %v", err)
		}
		if err := clearTaskEvents(ctx, tx, event.TaskID); err != nil {
			return Receipt{}, err
		}
	}
	if writeTask {
		encoded, encodeErr := json.Marshal(state)
		if encodeErr != nil {
			return Receipt{}, assimilationError(result.CodeInternal, "encode coverage task: %v", encodeErr)
		}
		if err := tx.Put(ctx, taskBucket, event.TaskID, encoded); err != nil {
			return Receipt{}, assimilationError(result.CodeInternal, "write coverage task: %v", err)
		}
	}
	receipt := buildReceipt(event, state, status, duplicateWindow)
	record := eventRecord{Digest: digest, Kind: event.Kind, TaskID: event.TaskID, Receipt: receipt}
	if event.Window != nil {
		record.WindowUnitID = event.Window.UnitID
		record.WindowEnd = event.Window.End
	}
	recordBytes, _ := json.Marshal(record)
	if err := tx.Put(ctx, eventBucket, event.EventID, recordBytes); err != nil {
		return Receipt{}, assimilationError(result.CodeInternal, "write coverage event receipt: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, assimilationError(result.CodeInternal, "commit coverage event: %v", err)
	}
	return receipt, nil
}

func validateEvent(event Event) error {
	if event.Version != EventVersion {
		return assimilationError(result.CodeValidation, "event version = %q, want %q", event.Version, EventVersion)
	}
	if !validShort(event.EventID, 200) || !validShort(event.TaskID, 200) || !validShort(event.Workspace, 500) {
		return assimilationError(result.CodeValidation, "event identity, task, and workspace are required and bounded")
	}
	payloads := 0
	if event.Inventory != nil {
		payloads++
	}
	if event.Window != nil {
		payloads++
	}
	if event.Checkpoint != nil {
		payloads++
	}
	switch event.Kind {
	case KindInventory:
		if payloads != 1 || event.Inventory == nil || event.ExpectedRevision != 0 {
			return assimilationError(result.CodeValidation, "inventory event requires only inventory at revision zero")
		}
	case KindWindow:
		if payloads != 1 || event.Window == nil || event.ExpectedRevision == 0 {
			return assimilationError(result.CodeValidation, "window event requires only a window and expected revision")
		}
	case KindCheckpoint:
		if payloads != 1 || event.Checkpoint == nil || event.ExpectedRevision == 0 {
			return assimilationError(result.CodeValidation, "checkpoint event requires only a checkpoint and expected revision")
		}
	case KindFinish, KindClear:
		if payloads != 0 || event.ExpectedRevision == 0 {
			return assimilationError(result.CodeValidation, "%s event requires expected revision and no payload", event.Kind)
		}
	case KindStatus:
		if payloads != 0 || event.ExpectedRevision != 0 {
			return assimilationError(result.CodeValidation, "status event cannot carry payload or expected revision")
		}
	default:
		return assimilationError(result.CodeValidation, "unsupported assimilation event kind %q", event.Kind)
	}
	return nil
}

func validateInventory(inventory Inventory) error {
	if !validShort(inventory.Source.ID, 200) || !validShort(inventory.Source.Title, 200) ||
		!validShort(inventory.Source.Locator, 1000) || !validDigest(inventory.Source.ContentHash) {
		return assimilationError(result.CodeValidation, "source identity, locator, and SHA-256 are required and bounded")
	}
	if inventory.Source.Kind != history.SourceDocumentAnchor &&
		inventory.Source.Kind != history.SourceRepositoryAnchor {
		return assimilationError(result.CodeValidation, "assimilation source kind must be document_anchor or repository_anchor")
	}
	if len(inventory.Units) == 0 || len(inventory.Units) > maximumUnits {
		return assimilationError(result.CodeValidation, "inventory requires 1 to %d units", maximumUnits)
	}
	units := make(map[string]Unit, len(inventory.Units))
	root := ""
	requiredReadable := 0
	for index, unit := range inventory.Units {
		if !validShort(unit.ID, 200) || !validShort(unit.Label, 200) ||
			(unit.Anchor != "" && !validShort(unit.Anchor, 200)) || unit.Extent > maximumUnitExtent {
			return assimilationError(result.CodeValidation, "inventory unit %d metadata or extent is invalid", index)
		}
		if _, duplicate := units[unit.ID]; duplicate || !validUnitKind(unit.Kind) {
			return assimilationError(result.CodeValidation, "inventory unit %d has duplicate identity or unsupported kind", index)
		}
		if unit.Kind == UnitSource {
			if root != "" || unit.ParentID != "" {
				return assimilationError(result.CodeValidation, "inventory requires exactly one parentless source unit")
			}
			root = unit.ID
		} else if unit.ParentID == "" {
			return assimilationError(result.CodeValidation, "non-source unit %q requires a parent", unit.ID)
		}
		if unit.Extent > 0 && !unit.Optional {
			requiredReadable++
		}
		units[unit.ID] = unit
	}
	if root == "" || requiredReadable == 0 {
		return assimilationError(result.CodeValidation, "inventory requires a source root and at least one readable required unit")
	}
	for _, unit := range inventory.Units {
		if unit.ParentID != "" {
			if _, found := units[unit.ParentID]; !found {
				return assimilationError(result.CodeValidation, "unit %q has unknown parent %q", unit.ID, unit.ParentID)
			}
		}
		seen := map[string]bool{}
		current := unit
		for current.ParentID != "" {
			if seen[current.ID] {
				return assimilationError(result.CodeValidation, "inventory contains a parent cycle at %q", current.ID)
			}
			seen[current.ID] = true
			current = units[current.ParentID]
		}
		if current.ID != root {
			return assimilationError(result.CodeValidation, "unit %q is disconnected from the source root", unit.ID)
		}
	}
	return nil
}

func validateCheckpoint(ctx context.Context, tx store.Tx, checkpoint Checkpoint, state taskState) error {
	unit, found := findUnit(state.Inventory.Units, checkpoint.ActiveUnitID)
	if !found || unit.Extent == 0 || checkpoint.Offset > unit.Extent ||
		!validShort(checkpoint.LastWindowEventID, 200) ||
		(checkpoint.HostCursor != "" && !validShort(checkpoint.HostCursor, 500)) {
		return assimilationError(result.CodeValidation, "checkpoint unit, offset, cursor, or last event is invalid")
	}
	encoded, err := tx.Get(ctx, eventBucket, checkpoint.LastWindowEventID)
	if err != nil {
		return assimilationError(result.CodeValidation, "checkpoint last window event was not recorded")
	}
	var record eventRecord
	if err := json.Unmarshal(encoded, &record); err != nil || record.Kind != KindWindow ||
		record.TaskID != state.TaskID || record.WindowUnitID != checkpoint.ActiveUnitID ||
		record.WindowEnd != checkpoint.Offset {
		return assimilationError(result.CodeValidation, "checkpoint does not match its last window event")
	}
	return nil
}

func buildReceipt(event Event, state taskState, status Status, duplicate bool) Receipt {
	if status == StatusCleared {
		return Receipt{
			Version: ReceiptVersion, EventID: event.EventID, TaskID: event.TaskID,
			Status: status, Revision: state.Revision, Unread: []UnreadRange{},
		}
	}
	unread := allUnread(state)
	coveredUnits, totalUnits := 0, 0
	var coveredExtent, totalExtent uint64
	for _, unit := range state.Inventory.Units {
		if unit.Optional || unit.Extent == 0 {
			continue
		}
		totalUnits++
		totalExtent += unit.Extent
		covered := coveredLength(state.Coverage[unit.ID])
		coveredExtent += covered
		if covered == unit.Extent {
			coveredUnits++
		}
	}
	visible := unread
	truncated := false
	if len(visible) > maximumUnread {
		visible = visible[:maximumUnread]
		truncated = true
	}
	var checkpoint *Checkpoint
	if state.Checkpoint != nil {
		copy := *state.Checkpoint
		checkpoint = &copy
	}
	return Receipt{
		Version: ReceiptVersion, EventID: event.EventID, TaskID: event.TaskID,
		Status: status, Revision: state.Revision, DuplicateWindow: duplicate,
		CoveredUnits: coveredUnits, TotalUnits: totalUnits,
		CoveredExtent: coveredExtent, TotalExtent: totalExtent,
		UnreadCount: len(unread), Unread: append([]UnreadRange{}, visible...), UnreadTruncated: truncated,
		Checkpoint: checkpoint, ReviewChallenge: state.ReviewChallenge,
	}
}

func clearTaskEvents(ctx context.Context, tx store.Tx, taskID string) error {
	entries, err := tx.Scan(ctx, eventBucket)
	if err != nil {
		return assimilationError(result.CodeInternal, "scan coverage event receipts: %v", err)
	}
	for _, entry := range entries {
		var record eventRecord
		if err := json.Unmarshal(entry.Value, &record); err != nil {
			return assimilationError(result.CodeInternal, "decode coverage event receipt during clear: %v", err)
		}
		if record.TaskID == taskID {
			if err := tx.Delete(ctx, eventBucket, entry.Key); err != nil {
				return assimilationError(result.CodeInternal, "clear coverage event receipt: %v", err)
			}
		}
	}
	return nil
}

func allUnread(state taskState) []UnreadRange {
	values := []UnreadRange{}
	for _, unit := range state.Inventory.Units {
		values = append(values, missingRanges(unit, state.Coverage[unit.ID])...)
	}
	return values
}

func replayEvent(ctx context.Context, tx store.Tx, eventID, digest string) (Receipt, bool, error) {
	encoded, err := tx.Get(ctx, eventBucket, eventID)
	if errors.Is(err, store.ErrNotFound) {
		return Receipt{}, false, nil
	}
	if err != nil {
		return Receipt{}, false, assimilationError(result.CodeInternal, "read coverage event receipt: %v", err)
	}
	var record eventRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return Receipt{}, false, assimilationError(result.CodeInternal, "decode coverage event receipt: %v", err)
	}
	if record.Digest != digest {
		return Receipt{}, false, assimilationError(result.CodeRevisionConflict, "event_id %q was already used by different content", eventID)
	}
	record.Receipt.Replayed = true
	return record.Receipt, true, nil
}

func loadTask(ctx context.Context, tx store.Tx, taskID string) (taskState, error) {
	encoded, err := tx.Get(ctx, taskBucket, taskID)
	if errors.Is(err, store.ErrNotFound) {
		return taskState{}, assimilationError(result.CodeNotFound, "assimilation task %q was not found", taskID)
	}
	if err != nil {
		return taskState{}, assimilationError(result.CodeInternal, "read coverage task: %v", err)
	}
	var state taskState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return taskState{}, assimilationError(result.CodeInternal, "decode coverage task: %v", err)
	}
	return state, nil
}

func requireRevision(expected, current uint64) error {
	if expected != current {
		return assimilationError(result.CodeRevisionConflict, "expected task revision %d, got %d", expected, current)
	}
	return nil
}

func statusForState(state taskState) Status {
	if state.Complete {
		return StatusCoverageComplete
	}
	return StatusInProgress
}

func findUnit(units []Unit, id string) (Unit, bool) {
	for _, unit := range units {
		if unit.ID == id {
			return unit, true
		}
	}
	return Unit{}, false
}

func validUnitKind(kind UnitKind) bool {
	switch kind {
	case UnitSource, UnitDirectory, UnitChapter, UnitPage, UnitTable, UnitAttachment:
		return true
	default:
		return false
	}
}

func validShort(value string, maximum int) bool {
	length := utf8.RuneCountInString(value)
	return strings.TrimSpace(value) != "" && length <= maximum
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func windowSignature(window Window) string {
	return fmt.Sprintf("%s:%d:%d", window.UnitID, window.Start, window.End)
}

func eventDigest(event Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
