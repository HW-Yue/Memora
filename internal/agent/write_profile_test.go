package agent_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestWriteGatewayPreparesScopedHashBoundProposalWithoutCallingMSQL(t *testing.T) {
	t.Parallel()

	executor := &recordingWriteMSQL{envelope: successfulWriteEnvelope()}
	gateway := newWriteGateway(t, executor)
	draft := validWriteDraft("proposal-prepare")
	proposal, err := gateway.Prepare(draft)
	if err != nil {
		t.Fatal(err)
	}
	if executor.Calls() != 0 {
		t.Fatalf("Prepare() made %d MSQL calls", executor.Calls())
	}
	if proposal.Version != agent.WriteProposalVersion || proposal.ProposalID != draft.ProposalID ||
		proposal.ProfileID != "profile-short-write" || !validTestSHA256(proposal.SHA256) {
		t.Fatalf("proposal = %#v", proposal)
	}
	if len(proposal.Request.Statements) != 1 {
		t.Fatalf("request = %#v", proposal.Request)
	}
	statement := proposal.Request.Statements[0]
	wantAuthorization := protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "agent:embedded-writer",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: protocolmsql.LevelWrite,
		DatabaseLevels: map[string]protocolmsql.RiskLevel{"work": protocolmsql.LevelWrite},
	}
	if !reflect.DeepEqual(statement.Authorization, wantAuthorization) ||
		statement.Mutation.Actor != "agent:embedded-writer" || statement.Mutation.ExpectedSchemaVersion != 7 ||
		statement.Mutation.ExpectedRevision != 11 || statement.Mutation.MaxAffectedRows != 1 {
		t.Fatalf("prepared statement = %#v", statement)
	}

	draft.Statements[0].Parameters.Named["body"] = "tampered original"
	draft.Statements[0].Mutation.SourceRevisions["row-source"] = 999
	if proposal.Request.Statements[0].Parameters.Named["body"] != "new fact" ||
		proposal.Request.Statements[0].Mutation.SourceRevisions["row-source"] != 3 {
		t.Fatalf("proposal aliased draft = %#v", proposal)
	}
}

func TestWriteGatewayRequiresExactApprovalAndRejectsTamperingBeforeMSQL(t *testing.T) {
	t.Parallel()

	executor := &recordingWriteMSQL{envelope: successfulWriteEnvelope()}
	gateway := newWriteGateway(t, executor)
	proposal, err := gateway.Prepare(validWriteDraft("proposal-approved"))
	if err != nil {
		t.Fatal(err)
	}
	invalidApprovals := []agent.WriteApproval{
		{},
		{Version: agent.WriteApprovalVersion, ProposalSHA256: proposal.SHA256, ApprovedBy: "user:owner"},
		{Version: agent.WriteApprovalVersion, ProposalSHA256: "sha256:" + strings.Repeat("a", 64), ApprovedBy: "user:owner", Confirmed: true},
		{Version: agent.WriteApprovalVersion, ProposalSHA256: proposal.SHA256, ApprovedBy: " ", Confirmed: true},
	}
	for _, approval := range invalidApprovals {
		if _, err := gateway.ExecuteApproved(context.Background(), proposal, approval); !errors.Is(err, agent.ErrWriteApprovalRequired) {
			t.Fatalf("ExecuteApproved(%#v) error = %v", approval, err)
		}
	}
	tampered := proposal
	tampered.Request.Source += "; DELETE FROM work.notes WHERE row_id = :row"
	if _, err := gateway.ExecuteApproved(context.Background(), tampered, approveWrite(proposal)); !errors.Is(err, agent.ErrInvalidWriteProposal) {
		t.Fatalf("tampered ExecuteApproved() error = %v", err)
	}
	if executor.Calls() != 0 {
		t.Fatalf("rejected attempts made %d MSQL calls", executor.Calls())
	}

	envelope, err := gateway.ExecuteApproved(context.Background(), proposal, approveWrite(proposal))
	if err != nil || !envelope.OK || executor.Calls() != 1 {
		t.Fatalf("approved ExecuteApproved() = %#v, %v calls=%d", envelope, err, executor.Calls())
	}
	request := executor.Requests()[0]
	for _, input := range request.Statements {
		if input.Authorization.DefaultLevel != protocolmsql.LevelWrite || input.Authorization.Approval == nil ||
			input.Authorization.Approval.Version != protocolmsql.ApprovalVersion ||
			input.Authorization.Approval.Action != agent.WriteApprovalAction ||
			"sha256:"+input.Authorization.Approval.SubjectSHA256 != proposal.SHA256 ||
			!input.Authorization.Approval.Confirmed {
			t.Fatalf("executed Authorization = %#v", input.Authorization)
		}
	}
	if _, err := gateway.ExecuteApproved(context.Background(), proposal, approveWrite(proposal)); !errors.Is(err, agent.ErrWriteApprovalConsumed) {
		t.Fatalf("replayed ExecuteApproved() error = %v", err)
	}
	if executor.Calls() != 1 {
		t.Fatalf("replay made %d MSQL calls", executor.Calls())
	}
}

func TestWriteGatewayConsumesApprovalBeforeConcurrentExecution(t *testing.T) {
	t.Parallel()

	executor := &recordingWriteMSQL{envelope: successfulWriteEnvelope()}
	gateway := newWriteGateway(t, executor)
	proposal, err := gateway.Prepare(validWriteDraft("proposal-concurrent"))
	if err != nil {
		t.Fatal(err)
	}
	approval := approveWrite(proposal)
	const contenders = 16
	start := make(chan struct{})
	errorsSeen := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			_, executeErr := gateway.ExecuteApproved(context.Background(), proposal, approval)
			errorsSeen <- executeErr
		}()
	}
	close(start)
	wait.Wait()
	close(errorsSeen)
	succeeded, consumed := 0, 0
	for executeErr := range errorsSeen {
		switch {
		case executeErr == nil:
			succeeded++
		case errors.Is(executeErr, agent.ErrWriteApprovalConsumed):
			consumed++
		default:
			t.Fatalf("unexpected concurrent error = %v", executeErr)
		}
	}
	if succeeded != 1 || consumed != contenders-1 || executor.Calls() != 1 {
		t.Fatalf("succeeded=%d consumed=%d calls=%d", succeeded, consumed, executor.Calls())
	}
}

func TestWriteGatewayRejectsInvalidProfilesAndMutationGuards(t *testing.T) {
	t.Parallel()

	executor := &recordingWriteMSQL{envelope: successfulWriteEnvelope()}
	profile := validWriteProfile()
	invalidProfiles := []agent.WriteProfile{
		{},
		func() agent.WriteProfile { value := profile; value.Version = "v0"; return value }(),
		func() agent.WriteProfile { value := profile; value.AuthorizedDatabases = nil; return value }(),
		func() agent.WriteProfile { value := profile; value.MaxAffectedRows = 0; return value }(),
	}
	for _, candidate := range invalidProfiles {
		if _, err := agent.NewWriteGateway(executor, candidate); !errors.Is(err, agent.ErrInvalidWriteProfile) {
			t.Fatalf("NewWriteGateway(%#v) error = %v", candidate, err)
		}
	}
	if _, err := agent.NewWriteGateway(nil, profile); !errors.Is(err, agent.ErrInvalidWriteProfile) {
		t.Fatalf("nil executor error = %v", err)
	}
	gateway := newWriteGateway(t, executor)
	valid := validWriteDraft("proposal-guards")
	invalidDrafts := []agent.WriteDraft{
		{},
		func() agent.WriteDraft { value := valid; value.ProposalID = " "; return value }(),
		func() agent.WriteDraft {
			value := valid
			value.Statements[0].Mutation.ExpectedSchemaVersion = 0
			return value
		}(),
		func() agent.WriteDraft {
			value := valid
			value.Statements[0].Mutation.MaxAffectedRows = 0
			return value
		}(),
		func() agent.WriteDraft {
			value := valid
			value.Statements[0].Mutation.MaxAffectedRows = 3
			return value
		}(),
		func() agent.WriteDraft { value := valid; value.Statements[0].Mutation.Reason = " "; return value }(),
		func() agent.WriteDraft {
			value := valid
			value.Statements[0].Mutation.SourceKind = "unknown"
			return value
		}(),
	}
	for _, draft := range invalidDrafts {
		if _, err := gateway.Prepare(draft); !errors.Is(err, agent.ErrInvalidWriteProposal) {
			t.Fatalf("Prepare(%#v) error = %v", draft, err)
		}
	}
	if executor.Calls() != 0 {
		t.Fatalf("invalid inputs made %d calls", executor.Calls())
	}
}

func validWriteProfile() agent.WriteProfile {
	return agent.WriteProfile{
		Version: agent.WriteProfileVersion, ProfileID: "profile-short-write",
		Actor: "agent:embedded-writer", AuthorizedDatabases: []string{"work"},
		MaxStatements: 2, MaxAffectedRows: 2,
	}
}

func validWriteDraft(proposalID string) agent.WriteDraft {
	return agent.WriteDraft{
		ProposalID: proposalID,
		Source:     "UPDATE work.notes SET body = :body WHERE row_id = :row",
		Statements: []agent.WriteStatement{{
			Parameters: protocolmsql.Parameters{Named: map[string]any{"body": "new fact", "row": "row-1"}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 7, ExpectedRevision: 11, MaxAffectedRows: 1,
				SourceRevisions: map[string]uint64{"row-source": 3}, Actor: "model:forged",
				Source: "conversation:test", Reason: "store the approved fact",
				SourceKind: protocolmsql.SourceConversationAssertion,
			},
		}},
	}
}

func newWriteGateway(t *testing.T, executor agent.MSQLExecutor) *agent.WriteGateway {
	t.Helper()
	gateway, err := agent.NewWriteGateway(executor, validWriteProfile())
	if err != nil {
		t.Fatal(err)
	}
	return gateway
}

func approveWrite(proposal agent.WriteProposal) agent.WriteApproval {
	return agent.WriteApproval{
		Version: agent.WriteApprovalVersion, ProposalSHA256: proposal.SHA256,
		ApprovedBy: "user:owner", Confirmed: true,
	}
}

func successfulWriteEnvelope() protocolmsql.Envelope {
	revision, sequence := uint64(12), uint64(44)
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "write-approved", OK: true,
		Results: []protocolmsql.StatementResult{{
			Index: 0, Statement: "UPDATE", Source: "UPDATE work.notes SET body = :body WHERE row_id = :row",
			Status: "succeeded", AffectedRows: 1, Revision: &revision, CommitSequence: &sequence,
			Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, Warnings: []protocolmsql.Notice{},
		}},
		Warnings: []protocolmsql.Notice{},
	}
}

func validTestSHA256(value string) bool {
	return len(value) == len("sha256:")+64 && strings.HasPrefix(value, "sha256:")
}

type recordingWriteMSQL struct {
	mu       sync.Mutex
	requests []protocolmsql.Request
	envelope protocolmsql.Envelope
	err      error
}

func (executor *recordingWriteMSQL) ExecuteMSQL(
	_ context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	executor.requests = append(executor.requests, request)
	return executor.envelope, executor.err
}

func (executor *recordingWriteMSQL) Calls() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return len(executor.requests)
}

func (executor *recordingWriteMSQL) Requests() []protocolmsql.Request {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return append([]protocolmsql.Request(nil), executor.requests...)
}
