package row

import (
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/router"
)

// Link is one entry of a Row's links field (docs/product/row-links.md): which
// Row it points at, where that Row lives, its summary, and the revision the
// summary was taken from. Table and Database are persisted rather than derived —
// a RowID is unique Instance-wide, but uniqueness is not addressability, and a
// reader that only holds an ID would have nowhere to point-read.
type Link struct {
	RelationID  string `json:"relation_id,omitempty"`
	Direction   string `json:"direction,omitempty"`
	RowID       string `json:"row_id"`
	DatabaseID  string `json:"database_id,omitempty"`
	TableID     string `json:"table_id,omitempty"`
	Table       string `json:"table,omitempty"`
	Summary     string `json:"summary"`
	Revision    uint64 `json:"revision"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// LinkRef names the Row a write wants to link to. Table is only needed when the
// target lives in another Table of the same Database: a bare ID resolves inside
// the Row's own Table first, and an ID that resolves nowhere is refused rather
// than guessed at.
type LinkRef struct {
	RowID string `json:"row_id"`
	Table string `json:"table,omitempty"`
}

type State string

const (
	StateLive       State = "live"
	StateDeleted    State = "deleted"
	StateSuperseded State = "superseded"
)

type Row struct {
	ID             string         `json:"row_id"`
	DatabaseID     string         `json:"database_id"`
	TableID        string         `json:"table_id"`
	SchemaVersion  uint64         `json:"schema_version"`
	Revision       uint64         `json:"revision"`
	CommitSequence uint64         `json:"commit_sequence"`
	State          State          `json:"row_state"`
	Values         map[string]any `json:"values"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	// ChangeSequence identifies the committed transaction that wrote this
	// revision. Attribution — who wrote it, why, from what source — is recorded
	// once per transaction in the Change Log, so a revision stores the key rather
	// than a copy of the attribution text.
	//
	// It is not the same counter as CommitSequence: that one orders Row commits
	// for MVCC visibility, this one orders every committed change including
	// Catalog ones. Zero means the revision predates the link, and its
	// attribution has to be resolved some other way.
	//
	// It never crosses an export boundary. A logical snapshot carries no Change
	// Log, so a sequence restored into another Instance would point at a
	// transaction that does not exist there; snapshots carry attribution
	// explicitly instead. Restored Rows therefore have no link and resolve their
	// attribution from the History the snapshot brought with it.
	ChangeSequence uint64 `json:"-"`
	// RouteLeafIDs are the semantic-tree leaves this Row hangs under.
	//
	// It is stored rather than looked up because the write order already knows
	// it: a RowID is mounted on its leaves before the Row itself is written, so
	// the list is in hand at the moment the Row is encoded. Answering a
	// write-time-known question with a separate structure is the structure that
	// can go stale. See docs/storage/leaf-rowid-v1.md §5.
	RouteLeafIDs []string `json:"route_leaf_ids,omitempty"`
	Links        []Link   `json:"links,omitempty"`
}

type WriteOptions struct {
	ExpectedRevision      uint64
	ExpectedSchemaVersion uint64
	Metadata              WriteMetadata
	// RouteLeafIDs names the leaf to mount on; RoutePath names a path for the
	// engine to resolve and complete. They are mutually exclusive.
	RouteLeafIDs []string
	RoutePath    []router.PathSegment
	// Origins carries the history this Row continues, set by a reshape.
	Origins []history.Origin
	// Links is the Row's complete link membership: nil leaves it untouched and
	// an empty list clears it, the same distinction the mount snapshot makes.
	Links []LinkRef
}

type WriteMetadata struct {
	Actor             string
	Source            string
	SourceKind        history.SourceKind
	SourceReceiptID   string
	SourceLocator     string
	SourceContentHash string
	Reason            string
}

// ReshapeOptions is the complete semantic-index snapshot for an atomic
// SPLIT/MERGE. RelationTargetOrdinals uses one-based target ordinals.
type ReshapeOptions struct {
	ExpectedSchemaVersion  uint64
	ExpectedRevision       uint64
	SourceRevisions        map[string]uint64
	TargetRouteLeafIDs     [][]string
	RelationTargetOrdinals map[string]int
	RouteUpdates           []RouteUpdate
	Metadata               WriteMetadata
}

type RouteUpdate struct {
	RouteID          string  `json:"route_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
	Purpose          string  `json:"purpose,omitempty"`
	Synopsis         *string `json:"synopsis,omitempty"`
}
