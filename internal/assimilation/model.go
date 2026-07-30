package assimilation

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	EventVersion   = "memora.assimilation-event/v1"
	ReceiptVersion = "memora.assimilation-receipt/v1"
)

type Kind string

const (
	KindInventory  Kind = "inventory"
	KindWindow     Kind = "window"
	KindCheckpoint Kind = "checkpoint"
	KindStatus     Kind = "status"
	KindFinish     Kind = "finish"
	KindClear      Kind = "clear"
)

type UnitKind string

const (
	UnitSource     UnitKind = "source"
	UnitDirectory  UnitKind = "directory"
	UnitChapter    UnitKind = "chapter"
	UnitPage       UnitKind = "page"
	UnitTable      UnitKind = "table"
	UnitAttachment UnitKind = "attachment"
)

type Source struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Locator     string             `json:"locator"`
	ContentHash string             `json:"content_hash"`
	Kind        history.SourceKind `json:"kind"`
}

type Unit struct {
	ID       string   `json:"id"`
	ParentID string   `json:"parent_id,omitempty"`
	Kind     UnitKind `json:"kind"`
	Label    string   `json:"label"`
	Anchor   string   `json:"anchor,omitempty"`
	Extent   uint64   `json:"extent,omitempty"`
	Optional bool     `json:"optional,omitempty"`
}

type Inventory struct {
	Source Source `json:"source"`
	Units  []Unit `json:"units"`
}

type Window struct {
	UnitID      string `json:"unit_id"`
	Start       uint64 `json:"start"`
	End         uint64 `json:"end"`
	Fingerprint string `json:"fingerprint"`
}

type Checkpoint struct {
	ActiveUnitID      string `json:"active_unit_id"`
	Offset            uint64 `json:"offset"`
	HostCursor        string `json:"host_cursor,omitempty"`
	LastWindowEventID string `json:"last_window_event_id"`
}

type Event struct {
	Version          string      `json:"version"`
	EventID          string      `json:"event_id"`
	TaskID           string      `json:"task_id"`
	Workspace        string      `json:"workspace"`
	Kind             Kind        `json:"kind"`
	ExpectedRevision uint64      `json:"expected_revision,omitempty"`
	Inventory        *Inventory  `json:"inventory,omitempty"`
	Window           *Window     `json:"window,omitempty"`
	Checkpoint       *Checkpoint `json:"checkpoint,omitempty"`
}

type Status string

const (
	StatusInProgress       Status = "in_progress"
	StatusIncomplete       Status = "incomplete"
	StatusCoverageComplete Status = "coverage_complete"
	StatusCleared          Status = "cleared"
)

type UnreadRange struct {
	UnitID string `json:"unit_id"`
	Start  uint64 `json:"start"`
	End    uint64 `json:"end"`
}

type Receipt struct {
	Version         string        `json:"version"`
	EventID         string        `json:"event_id"`
	TaskID          string        `json:"task_id"`
	Status          Status        `json:"status"`
	Revision        uint64        `json:"revision"`
	Replayed        bool          `json:"replayed"`
	DuplicateWindow bool          `json:"duplicate_window"`
	CoveredUnits    int           `json:"covered_units"`
	TotalUnits      int           `json:"total_units"`
	CoveredExtent   uint64        `json:"covered_extent"`
	TotalExtent     uint64        `json:"total_extent"`
	UnreadCount     int           `json:"unread_count"`
	Unread          []UnreadRange `json:"unread"`
	UnreadTruncated bool          `json:"unread_truncated"`
	Checkpoint      *Checkpoint   `json:"checkpoint,omitempty"`
	ReviewChallenge string        `json:"review_challenge,omitempty"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "assimilation coverage: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func assimilationError(code result.Code, format string, arguments ...any) error {
	return &Error{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
