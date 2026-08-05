package retrievalscore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (suite *Suite) Seal() error {
	if suite == nil {
		return errors.New("retrieval suite is nil")
	}
	suite.Hash = ""
	if err := validateSuiteContent(*suite); err != nil {
		return err
	}
	digest, err := contentDigest(*suite)
	if err != nil {
		return err
	}
	suite.Hash = digest
	return nil
}

func (suite Suite) Validate() error {
	if !validDigest(suite.Hash) {
		return errors.New("retrieval suite hash is invalid")
	}
	if err := validateSuiteContent(suite); err != nil {
		return err
	}
	want := suite.Hash
	suite.Hash = ""
	digest, err := contentDigest(suite)
	if err != nil {
		return err
	}
	if digest != want {
		return errors.New("retrieval suite hash mismatch")
	}
	return nil
}

func validateSuiteContent(suite Suite) error {
	if suite.Version != SuiteVersion || !validText(suite.SuiteID) || !validText(suite.DatasetID) ||
		!validText(suite.Split) || len(suite.Queries) == 0 || suite.Qrels == nil {
		return errors.New("retrieval suite identity and collections are required")
	}
	queries := make(map[string]struct{}, len(suite.Queries))
	positive := make(map[string]bool, len(suite.Queries))
	for _, query := range suite.Queries {
		if !validText(query.QueryID) || !validText(query.Group) {
			return errors.New("retrieval suite query is invalid")
		}
		if _, duplicate := queries[query.QueryID]; duplicate {
			return fmt.Errorf("retrieval suite query %q is duplicated", query.QueryID)
		}
		queries[query.QueryID] = struct{}{}
	}
	pairs := make(map[string]struct{}, len(suite.Qrels))
	for _, qrel := range suite.Qrels {
		if _, exists := queries[qrel.QueryID]; !exists || !validText(qrel.DocumentID) || qrel.Relevance < 0 {
			return errors.New("retrieval suite qrel is invalid")
		}
		pair := qrel.QueryID + "\x00" + qrel.DocumentID
		if _, duplicate := pairs[pair]; duplicate {
			return errors.New("retrieval suite qrel is duplicated")
		}
		pairs[pair] = struct{}{}
		if qrel.Relevance > 0 {
			positive[qrel.QueryID] = true
		}
	}
	for queryID := range queries {
		if !positive[queryID] {
			return fmt.Errorf("retrieval suite query %q has no positive qrel", queryID)
		}
	}
	return nil
}

func (run *Run) Seal() error {
	if run == nil {
		return errors.New("retrieval run is nil")
	}
	run.Hash = ""
	if err := validateRunContent(*run); err != nil {
		return err
	}
	digest, err := contentDigest(*run)
	if err != nil {
		return err
	}
	run.Hash = digest
	return nil
}

func (run Run) Validate() error {
	if !validDigest(run.Hash) {
		return errors.New("retrieval run hash is invalid")
	}
	if err := validateRunContent(run); err != nil {
		return err
	}
	want := run.Hash
	run.Hash = ""
	digest, err := contentDigest(run)
	if err != nil {
		return err
	}
	if digest != want {
		return errors.New("retrieval run hash mismatch")
	}
	return nil
}

func validateRunContent(run Run) error {
	if run.Version != RunVersion || !validText(run.RunID) || !validText(run.Arm) || !validDigest(run.SuiteHash) || len(run.Results) == 0 {
		return errors.New("retrieval run identity and results are required")
	}
	queries := make(map[string]struct{}, len(run.Results))
	for _, result := range run.Results {
		if !validText(result.QueryID) {
			return errors.New("retrieval run query identity is invalid")
		}
		if _, duplicate := queries[result.QueryID]; duplicate {
			return fmt.Errorf("retrieval run query %q is duplicated", result.QueryID)
		}
		queries[result.QueryID] = struct{}{}
		switch result.Status {
		case "completed":
		case "failed":
			if len(result.CandidateDocumentIDs) != 0 {
				return errors.New("failed retrieval result must not contain candidates")
			}
		default:
			return errors.New("retrieval result status is invalid")
		}
		candidates := make(map[string]struct{}, len(result.CandidateDocumentIDs))
		for _, documentID := range result.CandidateDocumentIDs {
			if !validText(documentID) {
				return errors.New("retrieval candidate document identity is invalid")
			}
			if _, duplicate := candidates[documentID]; duplicate {
				return errors.New("retrieval candidate document is duplicated")
			}
			candidates[documentID] = struct{}{}
		}
	}
	return nil
}

func (run Run) ValidateAgainst(suite Suite) error {
	if err := suite.Validate(); err != nil {
		return err
	}
	if err := run.Validate(); err != nil {
		return err
	}
	if run.SuiteHash != suite.Hash || len(run.Results) != len(suite.Queries) {
		return errors.New("retrieval run does not cover the bound suite")
	}
	for index, query := range suite.Queries {
		if run.Results[index].QueryID != query.QueryID {
			return fmt.Errorf("retrieval run result %d does not match suite query order", index)
		}
	}
	return nil
}

func (report *Report) seal() error {
	if report == nil {
		return errors.New("retrieval evaluation report is nil")
	}
	report.Hash = ""
	if report.Version != ReportVersion || !validText(report.SuiteID) || !validDigest(report.SuiteHash) ||
		report.K <= 0 || !validText(report.BaselineRun) || len(report.Runs) == 0 {
		return errors.New("retrieval evaluation report identity is invalid")
	}
	digest, err := contentDigest(*report)
	if err != nil {
		return err
	}
	report.Hash = digest
	return nil
}

func (report Report) Validate() error {
	if !validDigest(report.Hash) {
		return errors.New("retrieval evaluation report hash is invalid")
	}
	want := report.Hash
	report.Hash = ""
	digest, err := contentDigest(report)
	if err != nil {
		return err
	}
	if digest != want {
		return errors.New("retrieval evaluation report hash mismatch")
	}
	return nil
}

func contentDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func validText(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 512 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}
