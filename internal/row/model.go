package row

import "time"

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
}

type WriteOptions struct {
	ExpectedRevision      uint64
	ExpectedSchemaVersion uint64
	Metadata              WriteMetadata
	IndexTerms            []string
	RouteLeafIDs          []string
}

type WriteMetadata struct {
	Actor  string
	Source string
	Reason string
}

type storedRow struct {
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
}
