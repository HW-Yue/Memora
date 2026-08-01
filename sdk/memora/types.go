package memora

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const (
	RequestVersion       = "memora.go-sdk-request/v1"
	EnvelopeVersion      = "memora.result/v1"
	AuthorizationVersion = "memora.authorization/v2"
	ApprovalVersion      = "memora.approval/v1"
)

var (
	ErrInvalidRequest  = errors.New("memora SDK request is invalid")
	ErrInvalidEnvelope = errors.New("memora SDK envelope is invalid")
)

type RiskLevel string

const (
	LevelRead         RiskLevel = "L0"
	LevelWrite        RiskLevel = "L1"
	LevelStructural   RiskLevel = "L2"
	LevelIrreversible RiskLevel = "L3"
)

type Approval struct {
	Version       string `json:"version"`
	Action        string `json:"action"`
	SubjectSHA256 string `json:"subject_sha256"`
	Confirmed     bool   `json:"confirmed"`
}

type Authorization struct {
	Version             string               `json:"version"`
	Actor               string               `json:"actor"`
	AuthorizedDatabases []string             `json:"authorized_databases"`
	DefaultLevel        RiskLevel            `json:"default_level,omitempty"`
	DatabaseLevels      map[string]RiskLevel `json:"database_levels,omitempty"`
	Approval            *Approval            `json:"approval,omitempty"`
}

type Parameters struct {
	Named      map[string]any `json:"named,omitempty"`
	Positional []any          `json:"positional,omitempty"`
}

type SourceKind string

const (
	SourceConversationAssertion SourceKind = "conversation_assertion"
	SourceDocumentAnchor        SourceKind = "document_anchor"
	SourceRepositoryAnchor      SourceKind = "repository_anchor"
	SourceReviewed              SourceKind = "reviewed_source"
)

type RouteUpdate struct {
	RouteID          string  `json:"route_id"`
	ExpectedRevision uint64  `json:"expected_revision"`
	Purpose          string  `json:"purpose,omitempty"`
	Synopsis         *string `json:"synopsis,omitempty"`
}

type MutationOptions struct {
	ExpectedSchemaVersion  uint64            `json:"expected_schema_version,omitempty"`
	ExpectedRevision       uint64            `json:"expected_revision,omitempty"`
	SourceRevisions        map[string]uint64 `json:"source_revisions,omitempty"`
	MaxAffectedRows        uint64            `json:"max_affected_rows,omitempty"`
	Actor                  string            `json:"actor,omitempty"`
	Source                 string            `json:"source,omitempty"`
	Reason                 string            `json:"reason,omitempty"`
	SourceKind             SourceKind        `json:"source_kind,omitempty"`
	SourceReceiptID        string            `json:"source_receipt_id,omitempty"`
	SourceLocator          string            `json:"source_locator,omitempty"`
	SourceContentHash      string            `json:"source_content_hash,omitempty"`
	RouteLeafIDs           []string          `json:"route_leaf_ids,omitempty"`
	TargetRouteLeafIDs     [][]string        `json:"target_route_leaf_ids,omitempty"`
	RelationTargetOrdinals map[string]int    `json:"relation_target_ordinals,omitempty"`
	RouteUpdates           []RouteUpdate     `json:"route_updates,omitempty"`
}

func (options MutationOptions) MarshalJSON() ([]byte, error) {
	type wire MutationOptions
	value := struct {
		wire
		RouteLeafIDs       *[]string   `json:"route_leaf_ids,omitempty"`
		TargetRouteLeafIDs *[][]string `json:"target_route_leaf_ids,omitempty"`
	}{wire: wire(options)}
	if options.RouteLeafIDs != nil {
		value.RouteLeafIDs = &options.RouteLeafIDs
	}
	if options.TargetRouteLeafIDs != nil {
		value.TargetRouteLeafIDs = &options.TargetRouteLeafIDs
	}
	return json.Marshal(value)
}

type StatementInput struct {
	Parameters    Parameters      `json:"parameters,omitempty"`
	Mutation      MutationOptions `json:"mutation,omitempty"`
	Authorization Authorization   `json:"authorization"`
}

type Request struct {
	Version    string           `json:"version"`
	Source     string           `json:"source"`
	Statements []StatementInput `json:"statements"`
}

func (request Request) Validate() error {
	if request.Version != RequestVersion || strings.TrimSpace(request.Source) == "" || len(request.Source) > 1<<20 ||
		len(request.Statements) == 0 || len(request.Statements) > 128 {
		return ErrInvalidRequest
	}
	for _, statement := range request.Statements {
		if err := statement.Authorization.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (authorization Authorization) validate() error {
	if authorization.Version != AuthorizationVersion || strings.TrimSpace(authorization.Actor) == "" ||
		len(authorization.Actor) > 160 || len(authorization.AuthorizedDatabases) == 0 || len(authorization.AuthorizedDatabases) > 32 ||
		!validLevel(authorization.DefaultLevel) {
		return ErrInvalidRequest
	}
	scoped := make(map[string]bool, len(authorization.AuthorizedDatabases))
	for _, database := range authorization.AuthorizedDatabases {
		name := strings.ToLower(strings.TrimSpace(database))
		if name == "" || scoped[name] {
			return ErrInvalidRequest
		}
		scoped[name] = true
	}
	for database, level := range authorization.DatabaseLevels {
		if !scoped[strings.ToLower(strings.TrimSpace(database))] || !validLevel(level) {
			return ErrInvalidRequest
		}
	}
	if authorization.Approval != nil {
		approval := authorization.Approval
		decoded, hashErr := hex.DecodeString(approval.SubjectSHA256)
		if approval.Version != ApprovalVersion || strings.TrimSpace(approval.Action) == "" || hashErr != nil || len(decoded) != 32 || !approval.Confirmed {
			return ErrInvalidRequest
		}
	}
	return nil
}

func validLevel(level RiskLevel) bool {
	return level == "" || level == LevelRead || level == LevelWrite || level == LevelStructural || level == LevelIrreversible
}

type Code string
type Status string
type Row map[string]any

type ResultError struct {
	Code           Code           `json:"code"`
	Message        string         `json:"message"`
	Retryable      bool           `json:"retryable"`
	StatementIndex *int           `json:"statement_index,omitempty"`
	Details        map[string]any `json:"details,omitempty"`
}

type Notice struct {
	Code    Code           `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

type Column struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	ColumnID     string `json:"column_id,omitempty"`
	Purpose      string `json:"purpose,omitempty"`
	SemanticRole string `json:"semantic_role,omitempty"`
}

type ListPage struct {
	Version    string `json:"version"`
	Limit      uint64 `json:"limit"`
	Cursor     string `json:"cursor"`
	Snapshot   string `json:"snapshot"`
	Truncated  bool   `json:"truncated"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type StatementResult struct {
	Index          int             `json:"index"`
	Statement      string          `json:"statement"`
	Source         string          `json:"source"`
	Status         Status          `json:"status"`
	Columns        []Column        `json:"columns"`
	Rows           []Row           `json:"rows"`
	AffectedRows   uint64          `json:"affected_rows"`
	Revision       *uint64         `json:"revision,omitempty"`
	CommitSequence *uint64         `json:"commit_sequence,omitempty"`
	Truncated      bool            `json:"truncated"`
	NextCursor     string          `json:"next_cursor,omitempty"`
	Page           *ListPage       `json:"page,omitempty"`
	RowDetail      json.RawMessage `json:"row_detail,omitempty"`
	Discovery      json.RawMessage `json:"discovery,omitempty"`
	Warnings       []Notice        `json:"warnings"`
	Error          *ResultError    `json:"error,omitempty"`
}

type Envelope struct {
	Version    string            `json:"version"`
	RequestID  string            `json:"request_id"`
	OK         bool              `json:"ok"`
	Results    []StatementResult `json:"results"`
	Truncated  bool              `json:"truncated"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Warnings   []Notice          `json:"warnings"`
	Error      *ResultError      `json:"error,omitempty"`
	raw        json.RawMessage
}

func (envelope *Envelope) UnmarshalJSON(encoded []byte) error {
	type wire Envelope
	var decoded wire
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	value := Envelope(decoded)
	value.raw = append(json.RawMessage(nil), encoded...)
	if err := value.Validate(); err != nil {
		return err
	}
	*envelope = value
	return nil
}

func (envelope Envelope) Validate() error {
	if envelope.Version != EnvelopeVersion || strings.TrimSpace(envelope.RequestID) == "" || envelope.Results == nil || envelope.Warnings == nil {
		return ErrInvalidEnvelope
	}
	for index, statement := range envelope.Results {
		if statement.Index != index || statement.Columns == nil || statement.Rows == nil || statement.Warnings == nil || statement.Status == "" {
			return fmt.Errorf("%w: invalid statement result %d", ErrInvalidEnvelope, index)
		}
	}
	return nil
}

func (envelope Envelope) RawJSON() json.RawMessage {
	return append(json.RawMessage(nil), envelope.raw...)
}
