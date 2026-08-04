package service_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/result"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAgentWriteProfileExecutesL1InsertAndEngineRejectsHiddenStructuralSQL(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, rows, closeFixture := newFixture(t, ctx)
	defer closeFixture()
	session, err := service.OpenSession("agent:write-profile")
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := agent.NewWriteGateway(session, agent.WriteProfile{
		Version: agent.WriteProfileVersion, ProfileID: "profile-integration",
		Actor: "agent:embedded-writer", AuthorizedDatabases: []string{"work"},
		MaxStatements: 2, MaxAffectedRows: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	insert, err := gateway.Prepare(agent.WriteDraft{
		ProposalID: "proposal-insert",
		Source:     "INSERT INTO work.notes (title, count) VALUES (:title, :count)",
		Statements: []agent.WriteStatement{{
			Parameters: protocolmsql.Parameters{Named: map[string]any{"title": "approved", "count": int64(1)}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
				Source: "conversation:integration", Reason: "verify approved Agent L1 write",
				SourceKind: protocolmsql.SourceConversationAssertion,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := gateway.ExecuteApproved(ctx, insert, integrationWriteApproval(insert))
	if err != nil || !envelope.OK || envelope.Results[0].AffectedRows != 1 {
		t.Fatalf("approved INSERT = %#v, %v", envelope, err)
	}
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 1 || persisted[0].Values["title"] != "approved" {
		t.Fatalf("persisted rows = %#v, %v", persisted, err)
	}

	structural, err := gateway.Prepare(agent.WriteDraft{
		ProposalID: "proposal-hidden-structural",
		Source: "CREATE TABLE work.forbidden PURPOSE 'Forbidden' ROW SEMANTICS 'One forbidden row' " +
			"(body TEXT PURPOSE 'Body')",
		Statements: []agent.WriteStatement{{Mutation: protocolmsql.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
			Source: "conversation:integration", Reason: "attempt hidden structural escalation",
			SourceKind: protocolmsql.SourceConversationAssertion,
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	denied, err := gateway.ExecuteApproved(ctx, structural, integrationWriteApproval(structural))
	if err != nil {
		t.Fatal(err)
	}
	if denied.OK || len(denied.Results) != 1 || denied.Results[0].Error == nil ||
		denied.Results[0].Error.Code != protocolmsql.Code(result.CodePermissionDenied) {
		t.Fatalf("hidden structural statement escaped L1 Policy: %#v", denied)
	}
}

func integrationWriteApproval(proposal agent.WriteProposal) agent.WriteApproval {
	return agent.WriteApproval{
		Version: agent.WriteApprovalVersion, ProposalSHA256: proposal.SHA256,
		ApprovedBy: "user:integration", Confirmed: true,
	}
}
