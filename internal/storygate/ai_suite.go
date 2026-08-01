package storygate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/hostcontract"
)

const AIJourneySuiteVersion = "memora.ai-story-journey-suite/v1"

type AIStorySpecification struct {
	Story          string   `json:"story"`
	Prompt         string   `json:"prompt"`
	RequiredChecks []string `json:"required_checks"`
}

type AIJourneySuite struct {
	Version string                 `json:"version"`
	ID      string                 `json:"id"`
	Stories []AIStorySpecification `json:"stories"`
	Hash    string                 `json:"hash"`
}

func GenerateAIJourneySuite() (AIJourneySuite, error) {
	suite := AIJourneySuite{
		Version: AIJourneySuiteVersion, ID: "ai-native-product-stories-v2",
		Stories: aiStorySpecifications(),
	}
	hash, err := aiSuiteDigest(suite)
	if err != nil {
		return AIJourneySuite{}, err
	}
	suite.Hash = hash
	if err := suite.Validate(); err != nil {
		return AIJourneySuite{}, err
	}
	return suite, nil
}

func (suite AIJourneySuite) Validate() error {
	if suite.Version != AIJourneySuiteVersion || suite.ID != "ai-native-product-stories-v2" ||
		len(suite.Stories) != len(RequiredStories()) {
		return fmt.Errorf("AI journey suite identity or story count is invalid")
	}
	for index, specification := range suite.Stories {
		if specification.Story != RequiredStories()[index] || strings.TrimSpace(specification.Prompt) == "" ||
			!utf8.ValidString(specification.Prompt) || utf8.RuneCountInString(specification.Prompt) > 4096 ||
			len(specification.RequiredChecks) == 0 {
			return fmt.Errorf("AI journey suite story %d is invalid", index)
		}
		lower := strings.ToLower(specification.Prompt)
		for _, forbidden := range []string{"select ", "show ", "insert ", "update ", "delete ",
			"db_", "tbl_", "route_", "row_"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("AI journey %s prompt leaks protocol or stable identity", specification.Story)
			}
		}
		seen := map[string]bool{}
		for _, check := range specification.RequiredChecks {
			if !validCheckName(check) || seen[check] || strings.Contains(lower, strings.ToLower(check)) {
				return fmt.Errorf("AI journey %s checks are invalid, duplicated, or leaked", specification.Story)
			}
			seen[check] = true
		}
	}
	want, err := aiSuiteDigest(suite)
	if err != nil {
		return err
	}
	if suite.Hash != want {
		return fmt.Errorf("AI journey suite hash mismatch")
	}
	return nil
}

func (suite AIJourneySuite) Task(story string) (hostcontract.Task, error) {
	if err := suite.Validate(); err != nil {
		return hostcontract.Task{}, err
	}
	for _, specification := range suite.Stories {
		if specification.Story == story {
			return hostcontract.Task{
				Version: hostcontract.TaskVersion, ID: "ai-" + strings.ToLower(specification.Story),
				Prompt: specification.Prompt, AuthorizedDatabases: []string{"work"}, CorpusDigest: suite.Hash,
				Budget: hostcontract.Budget{MaxToolCalls: 64, MaxContextCharacters: 12000, MaxElapsedMillis: 600000},
			}, nil
		}
	}
	return hostcontract.Task{}, fmt.Errorf("AI journey story %q was not found", story)
}

func aiSuiteDigest(suite AIJourneySuite) (string, error) {
	suite.Hash = ""
	encoded, err := json.Marshal(suite)
	if err != nil {
		return "", fmt.Errorf("encode AI journey suite identity: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validCheckName(value string) bool {
	if value == "" || len(value) > 96 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && character == '_' {
			continue
		}
		return false
	}
	return true
}

func aiStorySpecifications() []AIStorySpecification {
	return []AIStorySpecification{
		{"US-HUMAN", "Organize the supplied goal without asking the user to design database internals.", []string{"goal_completed", "no_physical_operation_requested"}},
		{"US-COLD", "Take over an unfamiliar authorized knowledge database and identify where a requested topic belongs.", []string{"bounded_catalog_discovery", "root_navigation_started"}},
		{"US-READ", "Find and answer one stored fact through the semantic navigation workflow, including its current revision.", []string{"rowid_sql_evidence", "revision_evidence", "no_candidate_as_fact"}},
		{"US-INSERT", "Store one durable semantic module after checking for an existing equivalent and verify that it can be found again.", []string{"dedup_checked", "atomic_row_and_membership", "rediscovered_from_root"}},
		{"US-UPDATE", "Correct one existing semantic module while preserving history and changing no unrelated item.", []string{"expected_revision_enforced", "history_preserved", "unrelated_rows_unchanged", "rediscovered_from_root"}},
		{"US-DELETE", "Remove one authorized semantic module with an impact preview, audit trail, and recoverable history.", []string{"impact_previewed", "all_index_references_invalidated", "history_recoverable", "rediscovered_from_root"}},
		{"US-CORRECT", "Apply a user correction to the relevant stored fact and explain the committed semantic change.", []string{"feedback_bound_to_target", "correction_receipt_verified", "rediscovered_from_root"}},
		{"US-SCHEMA", "Evolve the logical structure for a new requirement without losing the existing stored data.", []string{"synonym_schema_checked", "affected_rows_migrated", "old_data_queryable"}},
		{"US-DBA", "Inspect semantic organization debt and produce a bounded reversible maintenance proposal.", []string{"semantic_debt_detected", "bounded_plan_produced", "facts_unchanged"}},
		{"US-OPTIMIZE", "Use observed navigation difficulty to improve only the affected semantic tree region and verify the result.", []string{"navigation_evidence_used", "quality_not_regressed", "facts_unchanged", "rediscovered_from_root"}},
		{"US-SPLIT", "Split a multi-topic semantic module into independently maintainable modules while preserving relationships and provenance.", []string{"semantic_boundaries_preserved", "relationships_and_provenance_preserved", "rediscovered_from_root"}},
		{"US-CONFLICT", "Handle two contradictory authorized sources without choosing a winner until the user resolves them.", []string{"evidence_presented_in_parallel", "user_resolution_required", "no_engine_truth_guess"}},
		{"US-ASSIMILATE", "Absorb reviewed external material as complete semantic modules with coverage and source evidence, without retaining mechanical chunks.", []string{"coverage_complete", "source_receipt_verified", "no_mechanical_chunks", "rediscovered_from_root"}},
		{"US-RECOVER", "Resume the knowledge task after a process restart using durable database state rather than prior chat context.", []string{"restart_completed", "state_recovered", "rediscovered_from_root"}},
		{"US-ENGINE", "Attempt stale, over-budget, and unauthorized changes and preserve an all-or-nothing logical result.", []string{"stale_revision_rejected", "budget_rejected", "authorization_rejected", "atomicity_preserved", "rediscovered_from_root"}},
		{"US-DEVELOPER", "Verify that the installed product and skill expose the declared public contract and deterministic diagnostics.", []string{"public_contract_verified", "deterministic_diagnostics"}},
	}
}
