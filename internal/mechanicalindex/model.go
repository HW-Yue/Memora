package mechanicalindex

const (
	Version          = "memora.mechanical-index/v1"
	TokenizerVersion = "memora.tokenizer/v1"
	SourceMechanical = "mechanical"
	StateActive      = "active"
	StateInvalid     = "invalid"
	StateDisabled    = "disabled"
)

type Locator struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Revision   uint64 `json:"revision"`
}

type Snapshot struct {
	Version          string `json:"version"`
	TokenizerVersion string `json:"tokenizer_version"`
	Locator
	Source    string   `json:"source"`
	State     string   `json:"state"`
	Terms     []string `json:"terms"`
	Truncated bool     `json:"truncated"`
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
	Disabled           bool
	MaxTerms           int
	MaxInputCharacters int
}
