// Package archive holds the shape of what a deletion leaves behind. The engine
// writes it and the Agent rebuilds from it; the read model lives here the way
// the history read model lives in package history, so the executor can speak it
// without depending on the storage layer.
package archive

import "github.com/HW-Yue/Memora/internal/router"

// Segment is one step of the semantic path a deleted Row lived under, stored
// root-first so a reader can rebuild it in the order it was navigated.
type Segment struct {
	RouteID string      `json:"route_id"`
	Name    string      `json:"name"`
	Kind    router.Kind `json:"kind"`
	Purpose string      `json:"purpose"`
}

// Row is the Row as it was stored, keyed exactly as the data table keys it, so
// a rebuild does not depend on this package guessing at columns.
type Row struct {
	RowID          string         `json:"row_id"`
	SchemaVersion  uint64         `json:"schema_version"`
	Revision       uint64         `json:"revision"`
	CommitSequence uint64         `json:"commit_sequence"`
	RowState       string         `json:"row_state"`
	Values         map[string]any `json:"values"`
	RouteLeafIDs   []string       `json:"route_leaf_ids"`
	Links          []Link         `json:"links"`
	SuccessorIDs   []string       `json:"successor_ids"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// Link mirrors the storage layer's own link entry field for field: a rebuild
// needs the relations as they were, not as they would resolve now, so nothing
// may be dropped on the way into the archive.
type Link struct {
	RelationID  string `json:"relation_id,omitempty"`
	Direction   string `json:"direction,omitempty"`
	RowID       string `json:"row_id"`
	DatabaseID  string `json:"database_id,omitempty"`
	TableID     string `json:"table_id,omitempty"`
	Summary     string `json:"summary"`
	Revision    uint64 `json:"revision"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
}

// Summary is what SHOW ARCHIVE lists: enough to decide whether a record is
// worth opening, and deliberately not the record itself.
type Summary struct {
	ArchiveID  string `json:"archive_id"`
	RowID      string `json:"row_id"`
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	Revision   uint64 `json:"revision"`
	DeletedAt  string `json:"deleted_at"`
	Actor      string `json:"actor"`
	Source     string `json:"source"`
	Reason     string `json:"reason"`
	Path       string `json:"path"`
}

// Page reports how a bounded listing ended. Snapshot identifies the view this
// walk faced: the archive is append-only, so for one scope that identity never
// changes and every page of a walk reports the same value.
type Page struct {
	Snapshot   string
	NextCursor string
	Truncated  bool
}

// Record is everything one deletion left behind, which is what an Agent
// rebuilds from. The engine stops at handing it over.
type Record struct {
	Summary Summary
	Path    []Segment
	Row     Row
}
