package history

import "time"

const Version = "memora.history/v1"

type Operation string

const (
	OperationInsert     Operation = "INSERT"
	OperationUpdate     Operation = "UPDATE"
	OperationDelete     Operation = "DELETE"
	OperationCompensate Operation = "COMPENSATE"
)

type Record struct {
	Version        string         `json:"version"`
	DatabaseID     string         `json:"database_id"`
	TableID        string         `json:"table_id"`
	RowID          string         `json:"row_id"`
	SchemaVersion  uint64         `json:"schema_version"`
	Revision       uint64         `json:"revision"`
	CommitSequence uint64         `json:"commit_sequence"`
	Operation      Operation      `json:"operation"`
	State          string         `json:"row_state"`
	Values         map[string]any `json:"values"`
	Actor          string         `json:"actor"`
	Source         string         `json:"source"`
	Reason         string         `json:"reason"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
	RecordedAt     time.Time      `json:"recorded_at"`
}

type Metadata struct {
	Actor  string
	Source string
	Reason string
}
