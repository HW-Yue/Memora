package agentindex

const (
	Version      = "memora.agent-index/v1"
	SourceAgent  = "agent"
	StateActive  = "active"
	StateInvalid = "invalid"
)

type Locator struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Revision   uint64 `json:"revision"`
}

type Snapshot struct {
	Version string `json:"version"`
	Locator
	Source string   `json:"source"`
	State  string   `json:"state"`
	Terms  []string `json:"terms"`
}

type Posting struct {
	Locator
	Source string `json:"source"`
}

type postingRecord struct {
	Version  string    `json:"version"`
	Postings []Posting `json:"postings"`
}

type Options struct {
	TargetTerms int
	MaxTerms    int
}
