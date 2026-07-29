package relation

import "time"

const Version = "memora.relation/v1"

const indexVersion = "memora.relation-index/v1"

type State string

const (
	StateLive    State = "live"
	StateDeleted State = "deleted"
)

type Endpoint struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
}

type Definition struct {
	Source         Endpoint
	Type           string
	Target         Endpoint
	Description    string
	CommitSequence uint64
}

type Relation struct {
	Version        string    `json:"version"`
	ID             string    `json:"relation_id"`
	Source         Endpoint  `json:"source"`
	Type           string    `json:"relation_type"`
	Target         Endpoint  `json:"target"`
	Description    string    `json:"description"`
	Revision       uint64    `json:"revision"`
	CommitSequence uint64    `json:"commit_sequence"`
	State          State     `json:"state"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type indexRecord struct {
	Version     string   `json:"version"`
	RelationIDs []string `json:"relation_ids"`
}
