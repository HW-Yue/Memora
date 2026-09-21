package history

import "time"

const Version = "memora.history/v1"

type SourceKind string

const (
	SourceConversationAssertion SourceKind = "conversation_assertion"
	SourceDocumentAnchor        SourceKind = "document_anchor"
	SourceRepositoryAnchor      SourceKind = "repository_anchor"
	SourceReviewed              SourceKind = "reviewed_source"
)

type Operation string

const (
	OperationInsert     Operation = "INSERT"
	OperationUpdate     Operation = "UPDATE"
	OperationDelete     Operation = "DELETE"
	OperationCompensate Operation = "COMPENSATE"
	OperationSplit      Operation = "SPLIT"
	OperationMerge      Operation = "MERGE"
)

// Origin names a Row whose history this one continues from: the Row a SPLIT
// split, or one of the Rows a MERGE merged. It is recorded on the new Row's
// first history record, so reading a Row's history back to its origin does not
// depend on resolving successor_ids across Rows.
type Origin struct {
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type Record struct {
	Version           string         `json:"version"`
	DatabaseID        string         `json:"database_id"`
	TableID           string         `json:"table_id"`
	RowID             string         `json:"row_id"`
	SchemaVersion     uint64         `json:"schema_version"`
	Revision          uint64         `json:"revision"`
	CommitSequence    uint64         `json:"commit_sequence"`
	Operation         Operation      `json:"operation"`
	State             string         `json:"row_state"`
	Values            map[string]any `json:"values"`
	Actor             string         `json:"actor"`
	Source            string         `json:"source"`
	SourceKind        SourceKind     `json:"source_kind"`
	SourceReceiptID   string         `json:"source_receipt_id,omitempty"`
	SourceLocator     string         `json:"source_locator,omitempty"`
	SourceContentHash string         `json:"source_content_hash,omitempty"`
	Reason            string         `json:"reason"`
	CreatedAt         time.Time      `json:"created_at"`
	UpdatedAt         time.Time      `json:"updated_at"`
	RecordedAt        time.Time      `json:"recorded_at"`
	// Origins is set on the first record of a Row that a reshape created, and
	// empty everywhere else: a Row's own later edits do not repeat it.
	Origins []Origin `json:"origins,omitempty"`
}

type Metadata struct {
	Actor             string
	Source            string
	SourceKind        SourceKind
	SourceReceiptID   string
	SourceLocator     string
	SourceContentHash string
	Reason            string
}
