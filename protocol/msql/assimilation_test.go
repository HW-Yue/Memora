package msql_test

import (
	"errors"
	"strings"
	"testing"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAssimilationPlanSealIsDeterministicValidatedAndDeepCopied(t *testing.T) {
	t.Parallel()
	proposal := validAssimilationProposal()
	first, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		t.Fatal(err)
	}
	second, err := protocolmsql.SealAssimilationPlan(validAssimilationProposal())
	if err != nil {
		t.Fatal(err)
	}
	if first.Validate() != nil || first.SHA256 != second.SHA256 || first.PlanID != proposal.ProposalID {
		t.Fatalf("sealed plans = %#v / %#v", first, second)
	}
	proposal.Statements[0].Parameters.Named["title"] = "caller mutation"
	proposal.Evidence.Reviewers[0] = "caller mutation"
	proposal.Evidence.ReviewArtifactSHA256s[0] = testAssimilationSHA("f")
	nested := proposal.Statements[0].Parameters.Named["metadata"].(map[string]any)
	nested["tags"].([]any)[0] = "caller mutation"
	if first.Proposal.Statements[0].Parameters.Named["title"] == "caller mutation" ||
		first.Proposal.Statements[0].Parameters.Named["metadata"].(map[string]any)["tags"].([]any)[0] == "caller mutation" ||
		first.Proposal.Evidence.Reviewers[0] == "caller mutation" ||
		first.Proposal.Evidence.ReviewArtifactSHA256s[0] == testAssimilationSHA("f") {
		t.Fatalf("sealed plan aliased caller proposal: %#v", first)
	}
	tampered := first
	tampered.Proposal.Statements[0].MSQL = "DELETE FROM work.notes WHERE row_id = :row"
	if err := tampered.Validate(); !errors.Is(err, protocolmsql.ErrInvalidRequest) {
		t.Fatalf("tampered plan error = %v", err)
	}
}

func TestAssimilationProposalRejectsIncompleteCoverageAndUnsafeMutationMetadata(t *testing.T) {
	t.Parallel()
	tests := []func(*protocolmsql.AssimilationProposal){
		func(value *protocolmsql.AssimilationProposal) { value.CoveredNodes-- },
		func(value *protocolmsql.AssimilationProposal) { value.Statements[0].Mutation.ExpectedSchemaVersion = 0 },
		func(value *protocolmsql.AssimilationProposal) { value.Statements[0].Mutation.MaxAffectedRows = 0 },
		func(value *protocolmsql.AssimilationProposal) { value.Statements[0].Mutation.Actor = "agent:other" },
		func(value *protocolmsql.AssimilationProposal) {
			value.Statements[0].Mutation.SourceKind = protocolmsql.SourceReviewed
		},
		func(value *protocolmsql.AssimilationProposal) { value.Statements[0].Mutation.SourceLocator = "other" },
		func(value *protocolmsql.AssimilationProposal) { value.Statements = nil },
	}
	for index, mutate := range tests {
		proposal := validAssimilationProposal()
		mutate(&proposal)
		if err := proposal.Validate(); !errors.Is(err, protocolmsql.ErrInvalidRequest) {
			t.Fatalf("mutation %d error = %v", index, err)
		}
	}
}

func TestAssimilationReceiptRejectsContentlessCommittedOrMalformedResults(t *testing.T) {
	t.Parallel()
	receipt := protocolmsql.AssimilationReceipt{
		Version: protocolmsql.AssimilationReceiptVersion, ReceiptID: "proposal-1", PlanID: "proposal-1",
		PlanSHA256: testAssimilationSHA("a"), Database: "work", SourceID: "source-1",
		SourceSHA256: testAssimilationSHA("b"), DocumentSHA256: testAssimilationSHA("c"),
		Evidence: reviewedAssimilationEvidence(), Status: protocolmsql.AssimilationCommitted,
		Statements: []protocolmsql.AssimilationStatementReceipt{{
			Index: 0, Kind: "INSERT", AffectedRows: 1, ObjectIDs: []string{"row-1"},
		}},
	}
	if err := receipt.Validate(); err != nil {
		t.Fatal(err)
	}
	receipt.Statements = nil
	if err := receipt.Validate(); !errors.Is(err, protocolmsql.ErrInvalidEnvelope) {
		t.Fatalf("nil statements error = %v", err)
	}
	receipt.Statements = []protocolmsql.AssimilationStatementReceipt{{
		Index: 0, Kind: "INSERT", AffectedRows: 2, ObjectIDs: []string{"row-1"},
	}}
	if err := receipt.Validate(); !errors.Is(err, protocolmsql.ErrInvalidEnvelope) {
		t.Fatalf("object inventory error = %v", err)
	}
}

func validAssimilationProposal() protocolmsql.AssimilationProposal {
	return protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: "proposal-1",
		Database: "work", Author: "agent:author", SourceID: "source-1",
		SourceLocator: "book.epub", SourceSHA256: testAssimilationSHA("b"),
		DocumentSHA256: testAssimilationSHA("c"), CoverageRevision: 4, CoveredNodes: 8, TotalNodes: 8,
		Evidence: reviewedAssimilationEvidence(),
		Statements: []protocolmsql.AssimilationStatement{{
			MSQL: "INSERT INTO work.notes (title) VALUES (:title)",
			Parameters: protocolmsql.Parameters{Named: map[string]any{
				"title": "Semantic fact", "metadata": map[string]any{"tags": []any{"one", "two"}},
			}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:author",
				Source: "source-1", Reason: "assimilate reviewed semantic module",
				SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: "book.epub",
				SourceContentHash: testAssimilationSHA("b"),
			},
		}},
	}
}

func reviewedAssimilationEvidence() *protocolmsql.AssimilationEvidence {
	return &protocolmsql.AssimilationEvidence{
		Version: protocolmsql.AssimilationEvidenceVersion, Author: "agent:author",
		Reviewers:             []string{"agent:reviewer"},
		ReviewArtifactSHA256s: []string{testAssimilationSHA("d")},
		CoverageRevision:      4, CoveredNodes: 8, TotalNodes: 8,
	}
}

func testAssimilationSHA(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}
