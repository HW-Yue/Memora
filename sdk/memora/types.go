package memora

import protocolmsql "github.com/HW-Yue/Memora/protocol/msql"

const (
	RequestVersion       = protocolmsql.RequestVersion
	EnvelopeVersion      = protocolmsql.EnvelopeVersion
	AuthorizationVersion = protocolmsql.AuthorizationVersion
	ApprovalVersion      = protocolmsql.ApprovalVersion
)

var (
	ErrInvalidRequest  = protocolmsql.ErrInvalidRequest
	ErrInvalidEnvelope = protocolmsql.ErrInvalidEnvelope
)

type RiskLevel = protocolmsql.RiskLevel

const (
	LevelRead         = protocolmsql.LevelRead
	LevelWrite        = protocolmsql.LevelWrite
	LevelStructural   = protocolmsql.LevelStructural
	LevelIrreversible = protocolmsql.LevelIrreversible
)

type Approval = protocolmsql.Approval
type Authorization = protocolmsql.Authorization
type Parameters = protocolmsql.Parameters
type SourceKind = protocolmsql.SourceKind

const (
	SourceConversationAssertion = protocolmsql.SourceConversationAssertion
	SourceDocumentAnchor        = protocolmsql.SourceDocumentAnchor
	SourceRepositoryAnchor      = protocolmsql.SourceRepositoryAnchor
	SourceReviewed              = protocolmsql.SourceReviewed
)

type RouteUpdate = protocolmsql.RouteUpdate
type MutationOptions = protocolmsql.MutationOptions
type StatementInput = protocolmsql.StatementInput
type Request = protocolmsql.Request
type Code = protocolmsql.Code
type Status = protocolmsql.Status
type Row = protocolmsql.Row
type ResultError = protocolmsql.ResultError
type Notice = protocolmsql.Notice
type Column = protocolmsql.Column
type ListPage = protocolmsql.ListPage
type StatementResult = protocolmsql.StatementResult
type Envelope = protocolmsql.Envelope
