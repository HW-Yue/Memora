package benchmark

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

const ReleaseCorpusVersion = "memora.ai-native-release-corpus/v1"

type ReleaseCorpus struct {
	Version string          `json:"version"`
	ID      string          `json:"id"`
	Records []ReleaseRecord `json:"records"`
	Queries []ReleaseQuery  `json:"queries"`
}

type ReleaseRecord struct {
	ID         string `json:"id"`
	ScenarioID string `json:"scenario_id"`
	Project    string `json:"project"`
	Text       string `json:"text"`
	Durable    bool   `json:"durable"`
	Supersedes string `json:"supersedes,omitempty"`
}

type ReleaseQuery struct {
	ID          string   `json:"id"`
	ScenarioID  string   `json:"scenario_id"`
	Project     string   `json:"project"`
	Text        string   `json:"text"`
	RelevantIDs []string `json:"relevant_ids"`
	Takeover    bool     `json:"takeover,omitempty"`
}

func LoadReleaseCorpus(path string) (ReleaseCorpus, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return ReleaseCorpus{}, benchmarkError(result.CodeInternal, "read release corpus: %v", err)
	}
	var corpus ReleaseCorpus
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return ReleaseCorpus{}, benchmarkError(result.CodeValidation, "decode release corpus: %v", err)
	}
	if err := corpus.Validate(); err != nil {
		return ReleaseCorpus{}, err
	}
	return corpus, nil
}

func (corpus ReleaseCorpus) Validate() error {
	if corpus.Version != ReleaseCorpusVersion || strings.TrimSpace(corpus.ID) == "" ||
		len(corpus.Records) == 0 || len(corpus.Queries) == 0 {
		return benchmarkError(result.CodeValidation, "release corpus version, ID, records, and queries are required")
	}
	records := make(map[string]ReleaseRecord, len(corpus.Records))
	for index, record := range corpus.Records {
		if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.ScenarioID) == "" ||
			strings.TrimSpace(record.Project) == "" || strings.TrimSpace(record.Text) == "" {
			return benchmarkError(result.CodeValidation, "release record %d is incomplete", index)
		}
		if _, exists := records[record.ID]; exists {
			return benchmarkError(result.CodeValidation, "release record %q is duplicated", record.ID)
		}
		records[record.ID] = record
	}
	for _, record := range corpus.Records {
		if record.Supersedes == "" {
			continue
		}
		previous, exists := records[record.Supersedes]
		if !exists || previous.Project != record.Project || previous.ScenarioID != record.ScenarioID {
			return benchmarkError(result.CodeValidation, "release record %q has invalid supersedes target", record.ID)
		}
	}
	queries := map[string]bool{}
	for index, query := range corpus.Queries {
		if strings.TrimSpace(query.ID) == "" || queries[query.ID] ||
			strings.TrimSpace(query.ScenarioID) == "" || strings.TrimSpace(query.Project) == "" ||
			strings.TrimSpace(query.Text) == "" || len(query.RelevantIDs) == 0 {
			return benchmarkError(result.CodeValidation, "release query %d is incomplete or duplicated", index)
		}
		queries[query.ID] = true
		relevant := map[string]bool{}
		for _, recordID := range query.RelevantIDs {
			record, exists := records[recordID]
			if !exists || record.ScenarioID != query.ScenarioID || relevant[recordID] {
				return benchmarkError(result.CodeValidation, "release query %q has invalid relevant record %q", query.ID, recordID)
			}
			relevant[recordID] = true
		}
	}
	return nil
}
