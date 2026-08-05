package retrievalscore

const (
	SuiteVersion  = "memora.retrieval-suite/v1"
	RunVersion    = "memora.retrieval-run/v1"
	ReportVersion = "memora.retrieval-evaluation-report/v1"
)

type Suite struct {
	Version   string  `json:"version"`
	SuiteID   string  `json:"suite_id"`
	DatasetID string  `json:"dataset_id"`
	Split     string  `json:"split"`
	Queries   []Query `json:"queries"`
	Qrels     []Qrel  `json:"qrels"`
	Hash      string  `json:"hash"`
}

type Query struct {
	QueryID string `json:"query_id"`
	Group   string `json:"group"`
	Text    string `json:"text"`
}

type Qrel struct {
	QueryID    string `json:"query_id"`
	DocumentID string `json:"document_id"`
	Relevance  int    `json:"relevance"`
}

type Run struct {
	Version   string   `json:"version"`
	RunID     string   `json:"run_id"`
	Arm       string   `json:"arm"`
	SuiteHash string   `json:"suite_hash"`
	Results   []Result `json:"results"`
	Hash      string   `json:"hash"`
}

type Result struct {
	QueryID              string   `json:"query_id"`
	Status               string   `json:"status"`
	CandidateDocumentIDs []string `json:"candidate_document_ids"`
	InputTokens          uint64   `json:"input_tokens"`
	OutputTokens         uint64   `json:"output_tokens"`
	ToolCalls            uint64   `json:"tool_calls"`
	ModelCalls           uint64   `json:"model_calls"`
	ContextCharacters    uint64   `json:"context_characters"`
	EndToEndMicroseconds uint64   `json:"end_to_end_microseconds"`
}

type Counts struct {
	Queries              uint64 `json:"queries"`
	Completed            uint64 `json:"completed"`
	Failed               uint64 `json:"failed"`
	Relevant             uint64 `json:"relevant"`
	RecalledAtK          uint64 `json:"recalled_at_k"`
	HitsAtK              uint64 `json:"hits_at_k"`
	InputTokens          uint64 `json:"input_tokens"`
	OutputTokens         uint64 `json:"output_tokens"`
	ToolCalls            uint64 `json:"tool_calls"`
	ModelCalls           uint64 `json:"model_calls"`
	ContextCharacters    uint64 `json:"context_characters"`
	EndToEndMicroseconds uint64 `json:"end_to_end_microseconds"`
}

type Metrics struct {
	Coverage            float64  `json:"coverage"`
	RecallAtK           float64  `json:"recall_at_k"`
	HitRateAtK          float64  `json:"hit_rate_at_k"`
	MRR                 float64  `json:"mrr"`
	MeanInputTokens     float64  `json:"mean_input_tokens"`
	MeanToolCalls       float64  `json:"mean_tool_calls"`
	InputTokenReduction *float64 `json:"input_token_reduction"`
	ToolCallReduction   *float64 `json:"tool_call_reduction"`
}

type Bucket struct {
	Group   string  `json:"group"`
	Counts  Counts  `json:"counts"`
	Metrics Metrics `json:"metrics"`
}

type RunScore struct {
	RunID   string   `json:"run_id"`
	Arm     string   `json:"arm"`
	Counts  Counts   `json:"counts"`
	Metrics Metrics  `json:"metrics"`
	Buckets []Bucket `json:"buckets"`
}

type Report struct {
	Version     string     `json:"version"`
	SuiteID     string     `json:"suite_id"`
	SuiteHash   string     `json:"suite_hash"`
	K           int        `json:"k"`
	BaselineRun string     `json:"baseline_run"`
	Runs        []RunScore `json:"runs"`
	Hash        string     `json:"hash"`
}
