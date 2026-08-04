package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	WriteProfileVersion  = "memora.agent-write-profile/v1"
	WriteProposalVersion = "memora.agent-write-proposal/v1"
	WriteApprovalVersion = "memora.agent-write-approval/v1"
	WriteApprovalAction  = "AGENT_WRITE_PROPOSAL"
)

var (
	ErrInvalidWriteProfile   = errors.New("Agent Write Profile is invalid")
	ErrInvalidWriteProposal  = errors.New("Agent Write Proposal is invalid")
	ErrWriteApprovalRequired = errors.New("Agent Write Proposal requires exact user approval")
	ErrWriteApprovalConsumed = errors.New("Agent Write Approval was already consumed")
)

type WriteProfile struct {
	Version             string   `json:"version"`
	ProfileID           string   `json:"profile_id"`
	Actor               string   `json:"actor"`
	AuthorizedDatabases []string `json:"authorized_databases"`
	MaxStatements       uint64   `json:"max_statements"`
	MaxAffectedRows     uint64   `json:"max_affected_rows"`
}

type WriteStatement struct {
	Parameters protocolmsql.Parameters      `json:"parameters,omitempty"`
	Mutation   protocolmsql.MutationOptions `json:"mutation"`
}

type WriteDraft struct {
	ProposalID string           `json:"proposal_id"`
	Source     string           `json:"source"`
	Statements []WriteStatement `json:"statements"`
}

type WriteProposal struct {
	Version    string               `json:"version"`
	ProposalID string               `json:"proposal_id"`
	ProfileID  string               `json:"profile_id"`
	SHA256     string               `json:"sha256"`
	Request    protocolmsql.Request `json:"request"`
}

type WriteApproval struct {
	Version        string `json:"version"`
	ProposalSHA256 string `json:"proposal_sha256"`
	ApprovedBy     string `json:"approved_by"`
	Confirmed      bool   `json:"confirmed"`
}

type WriteGateway struct {
	mu       sync.Mutex
	runtime  *Runtime
	profile  WriteProfile
	consumed map[string]struct{}
}

func NewWriteGateway(executor MSQLExecutor, profile WriteProfile) (*WriteGateway, error) {
	if executor == nil || !validWriteProfile(profile) {
		return nil, ErrInvalidWriteProfile
	}
	runtime, err := New(Dependencies{MSQL: executor})
	if err != nil {
		return nil, ErrInvalidWriteProfile
	}
	return &WriteGateway{
		runtime: runtime, profile: cloneWriteProfile(profile), consumed: make(map[string]struct{}),
	}, nil
}

func (gateway *WriteGateway) Prepare(draft WriteDraft) (WriteProposal, error) {
	if gateway == nil || !validTraceLabel(draft.ProposalID, 120) ||
		strings.TrimSpace(draft.Source) == "" || !utf8.ValidString(draft.Source) || len(draft.Source) > 1<<20 ||
		len(draft.Statements) == 0 || uint64(len(draft.Statements)) > gateway.profile.MaxStatements {
		return WriteProposal{}, ErrInvalidWriteProposal
	}
	request := protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: draft.Source,
		Statements: make([]protocolmsql.StatementInput, len(draft.Statements)),
	}
	for index, statement := range draft.Statements {
		mutation := cloneWriteMutation(statement.Mutation)
		if !gateway.validMutation(mutation) {
			return WriteProposal{}, ErrInvalidWriteProposal
		}
		mutation.Actor = gateway.profile.Actor
		request.Statements[index] = protocolmsql.StatementInput{
			Parameters: cloneWriteParameters(statement.Parameters), Mutation: mutation,
			Authorization: gateway.authorization(nil),
		}
	}
	if err := request.Validate(); err != nil {
		return WriteProposal{}, ErrInvalidWriteProposal
	}
	proposal := WriteProposal{
		Version: WriteProposalVersion, ProposalID: draft.ProposalID,
		ProfileID: gateway.profile.ProfileID, Request: cloneWriteRequest(request),
	}
	digest, err := gateway.proposalDigest(proposal)
	if err != nil {
		return WriteProposal{}, ErrInvalidWriteProposal
	}
	proposal.SHA256 = digest
	return cloneWriteProposal(proposal), nil
}

func (gateway *WriteGateway) ExecuteApproved(
	ctx context.Context,
	proposal WriteProposal,
	approval WriteApproval,
) (protocolmsql.Envelope, error) {
	if gateway == nil || ctx == nil {
		return protocolmsql.Envelope{}, ErrInvalidWriteProposal
	}
	if err := ctx.Err(); err != nil {
		return protocolmsql.Envelope{}, err
	}
	if !gateway.validProposal(proposal) {
		return protocolmsql.Envelope{}, ErrInvalidWriteProposal
	}
	if !validWriteApproval(approval, proposal.SHA256) {
		return protocolmsql.Envelope{}, ErrWriteApprovalRequired
	}

	gateway.mu.Lock()
	if _, exists := gateway.consumed[proposal.SHA256]; exists {
		gateway.mu.Unlock()
		return protocolmsql.Envelope{}, ErrWriteApprovalConsumed
	}
	gateway.consumed[proposal.SHA256] = struct{}{}
	gateway.mu.Unlock()

	request := cloneWriteRequest(proposal.Request)
	protocolApproval := &protocolmsql.Approval{
		Version: protocolmsql.ApprovalVersion, Action: WriteApprovalAction,
		SubjectSHA256: strings.TrimPrefix(proposal.SHA256, "sha256:"), Confirmed: true,
	}
	for index := range request.Statements {
		request.Statements[index].Authorization = gateway.authorization(protocolApproval)
	}
	return gateway.runtime.ExecuteMSQL(ctx, request)
}

func (gateway *WriteGateway) validProposal(proposal WriteProposal) bool {
	if proposal.Version != WriteProposalVersion || proposal.ProfileID != gateway.profile.ProfileID ||
		!validTraceLabel(proposal.ProposalID, 120) || !validWriteSHA256(proposal.SHA256) ||
		len(proposal.Request.Statements) == 0 ||
		uint64(len(proposal.Request.Statements)) > gateway.profile.MaxStatements ||
		proposal.Request.Validate() != nil {
		return false
	}
	wantAuthorization := gateway.authorization(nil)
	for _, statement := range proposal.Request.Statements {
		if !reflect.DeepEqual(statement.Authorization, wantAuthorization) ||
			statement.Mutation.Actor != gateway.profile.Actor || !gateway.validMutation(statement.Mutation) {
			return false
		}
	}
	digest, err := gateway.proposalDigest(proposal)
	return err == nil && digest == proposal.SHA256
}

func (gateway *WriteGateway) validMutation(mutation protocolmsql.MutationOptions) bool {
	return mutation.ExpectedSchemaVersion > 0 && mutation.MaxAffectedRows >= 1 &&
		mutation.MaxAffectedRows <= gateway.profile.MaxAffectedRows &&
		validWriteText(mutation.Source, 1000) && validWriteText(mutation.Reason, 1000) &&
		validWriteSourceKind(mutation.SourceKind)
}

func (gateway *WriteGateway) authorization(approval *protocolmsql.Approval) protocolmsql.Authorization {
	levels := make(map[string]protocolmsql.RiskLevel, len(gateway.profile.AuthorizedDatabases))
	for _, database := range gateway.profile.AuthorizedDatabases {
		levels[database] = protocolmsql.LevelWrite
	}
	var clonedApproval *protocolmsql.Approval
	if approval != nil {
		value := *approval
		clonedApproval = &value
	}
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: gateway.profile.Actor,
		AuthorizedDatabases: append([]string(nil), gateway.profile.AuthorizedDatabases...),
		DefaultLevel:        protocolmsql.LevelWrite, DatabaseLevels: levels, Approval: clonedApproval,
	}
}

func (gateway *WriteGateway) proposalDigest(proposal WriteProposal) (string, error) {
	payload := struct {
		Version    string               `json:"version"`
		ProposalID string               `json:"proposal_id"`
		Profile    WriteProfile         `json:"profile"`
		Request    protocolmsql.Request `json:"request"`
	}{
		Version: proposal.Version, ProposalID: proposal.ProposalID,
		Profile: cloneWriteProfile(gateway.profile), Request: cloneWriteRequest(proposal.Request),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func validWriteProfile(profile WriteProfile) bool {
	if profile.Version != WriteProfileVersion || !validTraceLabel(profile.ProfileID, 120) ||
		!validWriteText(profile.Actor, 160) || len(profile.AuthorizedDatabases) == 0 ||
		len(profile.AuthorizedDatabases) > 32 || profile.MaxStatements < 1 || profile.MaxStatements > 32 ||
		profile.MaxAffectedRows < 1 || profile.MaxAffectedRows > 1024 {
		return false
	}
	seen := make(map[string]bool, len(profile.AuthorizedDatabases))
	for _, database := range profile.AuthorizedDatabases {
		canonical := strings.ToLower(strings.TrimSpace(database))
		if !validWriteText(database, 200) || seen[canonical] {
			return false
		}
		seen[canonical] = true
	}
	return true
}

func validWriteApproval(approval WriteApproval, proposalSHA256 string) bool {
	return approval.Version == WriteApprovalVersion && approval.Confirmed &&
		approval.ProposalSHA256 == proposalSHA256 && validWriteText(approval.ApprovedBy, 160)
}

func validWriteText(value string, limit int) bool {
	if strings.TrimSpace(value) == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > limit {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validWriteSourceKind(kind protocolmsql.SourceKind) bool {
	return kind == protocolmsql.SourceConversationAssertion || kind == protocolmsql.SourceDocumentAnchor ||
		kind == protocolmsql.SourceRepositoryAnchor || kind == protocolmsql.SourceReviewed
}

func validWriteSHA256(value string) bool {
	if len(value) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func cloneWriteProfile(profile WriteProfile) WriteProfile {
	profile.AuthorizedDatabases = append([]string(nil), profile.AuthorizedDatabases...)
	return profile
}

func cloneWriteProposal(proposal WriteProposal) WriteProposal {
	proposal.Request = cloneWriteRequest(proposal.Request)
	return proposal
}

func cloneWriteRequest(request protocolmsql.Request) protocolmsql.Request {
	statements := request.Statements
	request.Statements = make([]protocolmsql.StatementInput, len(statements))
	for index, statement := range statements {
		request.Statements[index] = protocolmsql.StatementInput{
			Parameters:    cloneWriteParameters(statement.Parameters),
			Mutation:      cloneWriteMutation(statement.Mutation),
			Authorization: cloneWriteAuthorization(statement.Authorization),
		}
	}
	return request
}

func cloneWriteParameters(parameters protocolmsql.Parameters) protocolmsql.Parameters {
	parameters.Named = cloneQueryMap(parameters.Named)
	if parameters.Positional != nil {
		parameters.Positional = make([]any, len(parameters.Positional))
		for index, value := range parameters.Positional {
			parameters.Positional[index] = cloneQueryValue(value)
		}
	}
	return parameters
}

func cloneWriteMutation(mutation protocolmsql.MutationOptions) protocolmsql.MutationOptions {
	mutation.SourceRevisions = cloneWriteUint64Map(mutation.SourceRevisions)
	mutation.RelationTargetOrdinals = cloneWriteIntMap(mutation.RelationTargetOrdinals)
	mutation.RouteLeafIDs = append([]string(nil), mutation.RouteLeafIDs...)
	if mutation.TargetRouteLeafIDs != nil {
		mutation.TargetRouteLeafIDs = make([][]string, len(mutation.TargetRouteLeafIDs))
		for index, values := range mutation.TargetRouteLeafIDs {
			mutation.TargetRouteLeafIDs[index] = append([]string(nil), values...)
		}
	}
	routeUpdates := mutation.RouteUpdates
	mutation.RouteUpdates = make([]protocolmsql.RouteUpdate, len(routeUpdates))
	for index, update := range routeUpdates {
		mutation.RouteUpdates[index] = update
		if update.Synopsis != nil {
			value := *update.Synopsis
			mutation.RouteUpdates[index].Synopsis = &value
		}
	}
	return mutation
}

func cloneWriteAuthorization(authorization protocolmsql.Authorization) protocolmsql.Authorization {
	authorization.AuthorizedDatabases = append([]string(nil), authorization.AuthorizedDatabases...)
	if authorization.DatabaseLevels != nil {
		levels := authorization.DatabaseLevels
		authorization.DatabaseLevels = make(map[string]protocolmsql.RiskLevel, len(levels))
		for database, level := range levels {
			authorization.DatabaseLevels[database] = level
		}
	}
	if authorization.Approval != nil {
		approval := *authorization.Approval
		authorization.Approval = &approval
	}
	return authorization
}

func cloneWriteUint64Map(values map[string]uint64) map[string]uint64 {
	if values == nil {
		return nil
	}
	cloned := make(map[string]uint64, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneWriteIntMap(values map[string]int) map[string]int {
	if values == nil {
		return nil
	}
	cloned := make(map[string]int, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
