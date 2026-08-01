// Package skilldiscovery plans and audits the Canonical Skill's speculative
// navigation calls. It does not choose a Table, read facts, or answer a task.
package skilldiscovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routeexact"
	"github.com/HW-Yue/Memora/internal/security"
)

const Version = "memora.speculative-discovery/v1"

const (
	maxDatabases      = 4
	maxCandidates     = 8
	maxCandidateBytes = 4096
	maxPrefetchTables = 2
	maxToolCalls      = 10
	maxTableRows      = 16
	maxRootRows       = 12
	maxContextBytes   = 12000
)

var ErrInvalid = errors.New("invalid speculative discovery")

type CallKind string

const (
	CallCatalog      CallKind = "catalog"
	CallTables       CallKind = "tables"
	CallLexical      CallKind = "lexical"
	CallVector       CallKind = "vector"
	CallRootPrefetch CallKind = "root_prefetch"
	CallRootFallback CallKind = "root_fallback"
)

type VectorQuery struct {
	Values      []float32
	SpaceDigest string
}

type TableRef struct {
	Database string
	Table    string
}

type Request struct {
	TopicID                string
	Actor                  string
	AuthorizedDatabases    []string
	LexicalQuery           string
	Vector                 *VectorQuery
	PrefetchTables         []TableRef
	PrefetchFrameTopicID   string
	CandidateLimit         int
	CandidateUTF8ByteLimit int
	TableLimit             int
	RootLimit              int
	ContextUTF8ByteLimit   int
}

type Budget struct {
	MaxToolCalls           int
	CandidateLimit         int
	CandidateUTF8ByteLimit int
	PrefetchTableLimit     int
	ContextUTF8ByteLimit   int
}

type Call struct {
	ID                     string
	Kind                   CallKind
	MSQL                   string
	Input                  executor.StatementInput
	Table                  *TableRef
	CandidateLimit         int
	CandidateUTF8ByteLimit int
}

type Plan struct {
	Version             string
	TopicID             string
	AuthorizedDatabases []string
	Calls               []Call
	Budget              Budget
	rootLimit           int
	actor               string
}

type Audit struct {
	ToolCalls              int
	MSQLStatements         int
	OutputUTF8Bytes        int
	CandidatesUsed         int
	CandidateUTF8BytesUsed int
	PrefetchedTables       int
}

type Predictor struct {
	Predictor       string
	Status          discovery.PredictorStatus
	Snapshot        string
	CatalogRevision string
	CandidateCount  uint64
	Truncated       bool
}

type CatalogTable struct {
	TableRef
	DatabaseID string
	TableID    string
	Row        result.Row
}

type PrefetchedRoot struct {
	TableRef
	Snapshot  string
	Routes    []result.Row
	Truncated bool
}

type Frame struct {
	Version              string
	TopicID              string
	CatalogRevision      string
	CatalogPageSnapshots []string
	CatalogDatabases     []result.Row
	CatalogTables        []CatalogTable
	Predictors           []Predictor
	Candidates           []discovery.Candidate
	PrefetchedRoots      []PrefetchedRoot
	Audit                Audit
	Truncated            bool
	invalidated          bool
	authorizedDatabases  []string
	rootLimit            int
	actor                string
}

type Selection struct {
	TableRef
	UsedPrefetch        bool
	DiscardedStaleFrame bool
	Routes              []result.Row
	RootSnapshot        string
	Fallback            *Call
}

func Build(request Request) (Plan, error) {
	if err := validateRequest(request); err != nil {
		return Plan{}, err
	}
	authorized := append([]string(nil), request.AuthorizedDatabases...)
	budget := Budget{
		MaxToolCalls: maxToolCalls, CandidateLimit: request.CandidateLimit,
		CandidateUTF8ByteLimit: request.CandidateUTF8ByteLimit,
		PrefetchTableLimit:     maxPrefetchTables, ContextUTF8ByteLimit: request.ContextUTF8ByteLimit,
	}
	plan := Plan{
		Version: Version, TopicID: request.TopicID, AuthorizedDatabases: authorized,
		Calls: []Call{}, Budget: budget, rootLimit: request.RootLimit, actor: request.Actor,
	}
	plan.Calls = append(plan.Calls, Call{
		ID: "discover-01-catalog", Kind: CallCatalog,
		MSQL: "SHOW CONFIGURATION; SHOW DATABASES LIMIT 32 COMPACT",
	})
	for _, database := range authorized {
		plan.Calls = append(plan.Calls, Call{
			ID: fmt.Sprintf("discover-%02d-tables", len(plan.Calls)+1), Kind: CallTables,
			MSQL:  fmt.Sprintf("SHOW TABLES FROM %s LIMIT %d COMPACT", quoteIdentifier(database), request.TableLimit),
			Input: authorizedInput(request.Actor, authorized, nil),
			Table: &TableRef{Database: database},
		})
	}
	lexicalLimit, lexicalBytes := request.CandidateLimit, request.CandidateUTF8ByteLimit
	vectorLimit, vectorBytes := 0, 0
	if request.Vector != nil {
		lexicalLimit = (request.CandidateLimit + 1) / 2
		vectorLimit = request.CandidateLimit - lexicalLimit
		lexicalBytes = (request.CandidateUTF8ByteLimit + 1) / 2
		vectorBytes = request.CandidateUTF8ByteLimit - lexicalBytes
	}
	plan.Calls = append(plan.Calls, Call{
		ID: fmt.Sprintf("discover-%02d-lexical", len(plan.Calls)+1), Kind: CallLexical,
		MSQL: "SHOW ROUTE CANDIDATES FROM ALL TABLES USING LEXICAL :lexical_query LIMIT :lexical_limit BYTES :lexical_bytes",
		Input: authorizedInput(request.Actor, authorized, map[string]any{
			"lexical_query": request.LexicalQuery, "lexical_limit": int64(lexicalLimit),
			"lexical_bytes": int64(lexicalBytes),
		}),
		CandidateLimit: lexicalLimit, CandidateUTF8ByteLimit: lexicalBytes,
	})
	if request.Vector != nil {
		plan.Calls = append(plan.Calls, Call{
			ID: fmt.Sprintf("discover-%02d-vector", len(plan.Calls)+1), Kind: CallVector,
			MSQL: "SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query_vector SPACE :space_digest LIMIT :vector_limit BYTES :vector_bytes",
			Input: authorizedInput(request.Actor, authorized, map[string]any{
				"query_vector": append([]float32(nil), request.Vector.Values...),
				"space_digest": request.Vector.SpaceDigest, "vector_limit": int64(vectorLimit),
				"vector_bytes": int64(vectorBytes),
			}),
			CandidateLimit: vectorLimit, CandidateUTF8ByteLimit: vectorBytes,
		})
	}
	for _, table := range request.PrefetchTables {
		cloned := table
		plan.Calls = append(plan.Calls, Call{
			ID: fmt.Sprintf("discover-%02d-root", len(plan.Calls)+1), Kind: CallRootPrefetch,
			MSQL: fmt.Sprintf(
				"SHOW ROUTES FROM TABLE %s.%s AT ROOT LIMIT :root_limit",
				quoteIdentifier(table.Database), quoteIdentifier(table.Table),
			),
			Input: authorizedInput(request.Actor, authorized, map[string]any{"root_limit": int64(request.RootLimit)}),
			Table: &cloned,
		})
	}
	if len(plan.Calls) > maxToolCalls {
		return Plan{}, invalid("plan exceeds the tool-call ceiling")
	}
	return clonePlan(plan), nil
}

func Finalize(plan Plan, responses []result.Envelope) (Frame, error) {
	if plan.Version != Version || strings.TrimSpace(plan.TopicID) == "" || len(plan.Calls) == 0 ||
		len(plan.Calls) != len(responses) {
		return Frame{}, invalid("plan and response count do not match")
	}
	frame := Frame{
		Version: Version, TopicID: plan.TopicID,
		CatalogDatabases: []result.Row{}, CatalogTables: []CatalogTable{},
		Predictors: []Predictor{}, Candidates: []discovery.Candidate{},
		PrefetchedRoots: []PrefetchedRoot{}, authorizedDatabases: append([]string(nil), plan.AuthorizedDatabases...),
		rootLimit: plan.rootLimit, actor: plan.actor, CatalogPageSnapshots: []string{},
	}
	for index, call := range plan.Calls {
		envelope := responses[index]
		if envelope.RequestID != call.ID {
			return Frame{}, invalid("response %d is not bound to call %q", index, call.ID)
		}
		if err := envelope.Validate(); err != nil {
			return Frame{}, invalid("response %q is invalid: %v", call.ID, err)
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			return Frame{}, invalid("response %q cannot be audited: %v", call.ID, err)
		}
		frame.Audit.ToolCalls++
		frame.Audit.MSQLStatements += len(envelope.Results)
		frame.Audit.OutputUTF8Bytes += len(encoded)
		switch call.Kind {
		case CallCatalog:
			if !envelope.OK || len(envelope.Results) != 2 || envelope.Results[1].Page == nil ||
				envelope.Results[1].Page.Snapshot == "" {
				return Frame{}, invalid("Catalog discovery failed")
			}
			frame.CatalogDatabases = cloneRows(envelope.Results[1].Rows)
			frame.CatalogPageSnapshots = append(frame.CatalogPageSnapshots, envelope.Results[1].Page.Snapshot)
			frame.Truncated = frame.Truncated || envelope.Truncated
		case CallTables:
			if !envelope.OK || len(envelope.Results) != 1 || call.Table == nil ||
				envelope.Results[0].Page == nil || envelope.Results[0].Page.Snapshot == "" {
				return Frame{}, invalid("Table discovery failed")
			}
			frame.CatalogPageSnapshots = append(frame.CatalogPageSnapshots, envelope.Results[0].Page.Snapshot)
			for _, row := range envelope.Results[0].Rows {
				name, _ := row["name"].(string)
				databaseID, _ := row["database_id"].(string)
				tableID, _ := row["table_id"].(string)
				if name == "" || databaseID == "" || tableID == "" {
					return Frame{}, invalid("Table discovery row is incomplete")
				}
				frame.CatalogTables = append(frame.CatalogTables, CatalogTable{
					TableRef:   TableRef{Database: call.Table.Database, Table: name},
					DatabaseID: databaseID, TableID: tableID, Row: cloneRow(row),
				})
			}
			frame.Truncated = frame.Truncated || envelope.Truncated
		case CallLexical, CallVector:
			if err := consumePredictor(&frame, call, envelope); err != nil {
				return Frame{}, err
			}
		case CallRootPrefetch:
			if err := consumeRoot(&frame, call, envelope); err != nil {
				return Frame{}, err
			}
		default:
			return Frame{}, invalid("plan contains unsupported call kind %q", call.Kind)
		}
	}
	if frame.Audit.CandidatesUsed > plan.Budget.CandidateLimit ||
		frame.Audit.CandidateUTF8BytesUsed > plan.Budget.CandidateUTF8ByteLimit ||
		frame.Audit.PrefetchedTables > plan.Budget.PrefetchTableLimit ||
		frame.Audit.ToolCalls > plan.Budget.MaxToolCalls {
		return Frame{}, invalid("speculative discovery exceeded its global budget")
	}
	if frame.Audit.OutputUTF8Bytes > plan.Budget.ContextUTF8ByteLimit {
		frame.invalidated = true
		frame.Truncated = true
		frame.Candidates = []discovery.Candidate{}
		frame.PrefetchedRoots = []PrefetchedRoot{}
	}
	return cloneFrame(frame), nil
}

func (frame Frame) AnswerReady() bool { return false }

func (frame Frame) Reusable(topicID string) bool {
	return frame.Version == Version && !frame.invalidated && topicID != "" && topicID == frame.TopicID
}

func (frame Frame) SelectTable(topicID string, table TableRef) (Selection, error) {
	if !validName(table.Database) || !validName(table.Table) || !containsString(frame.authorizedDatabases, table.Database) {
		return Selection{}, invalid("selected Table is outside the authorized discovery scope")
	}
	current := frame.Reusable(topicID)
	if current && !frameHasTable(frame, table) {
		return Selection{}, invalid("selected Table is absent from the current Catalog frame")
	}
	selection := Selection{TableRef: table, DiscardedStaleFrame: !current}
	if current {
		for _, root := range frame.PrefetchedRoots {
			if equalTable(root.TableRef, table) {
				selection.UsedPrefetch = true
				selection.Routes = cloneRows(root.Routes)
				selection.RootSnapshot = root.Snapshot
				return selection, nil
			}
		}
	}
	fallback := fallbackCall(frame.actor, frame.authorizedDatabases, frame.rootLimit, table)
	selection.Fallback = &fallback
	return selection, nil
}

func consumePredictor(frame *Frame, call Call, envelope result.Envelope) error {
	if !envelope.OK || len(envelope.Results) != 1 || envelope.Results[0].Discovery == nil {
		frame.Predictors = append(frame.Predictors, Predictor{
			Predictor: string(call.Kind) + "-route/v1", Status: discovery.PredictorUnavailable,
		})
		return nil
	}
	value := envelope.Results[0].Discovery
	if value.Usage != discovery.UsageNavigationOnly || len(value.Predictors) != 1 ||
		int(value.Budget.CandidateLimit) > call.CandidateLimit ||
		int(value.Budget.UTF8ByteLimit) > call.CandidateUTF8ByteLimit {
		return invalid("predictor %q violated its allocated navigation budget", call.ID)
	}
	if frame.CatalogRevision == "" {
		frame.CatalogRevision = value.CatalogRevision
	} else if frame.CatalogRevision != value.CatalogRevision {
		return invalid("predictor Catalog revisions do not match")
	}
	receipt := value.Predictors[0]
	frame.Predictors = append(frame.Predictors, Predictor{
		Predictor: receipt.Predictor, Status: receipt.Status, Snapshot: value.Snapshot,
		CatalogRevision: value.CatalogRevision, CandidateCount: receipt.CandidateCount,
		Truncated: receipt.Truncated,
	})
	frame.Audit.CandidatesUsed += int(value.Budget.CandidatesUsed)
	frame.Audit.CandidateUTF8BytesUsed += int(value.Budget.UTF8BytesUsed)
	frame.Candidates = append(frame.Candidates, cloneCandidates(value.Candidates)...)
	frame.Truncated = frame.Truncated || value.Truncated || envelope.Truncated
	return nil
}

func consumeRoot(frame *Frame, call Call, envelope result.Envelope) error {
	if call.Table == nil {
		return invalid("root prefetch has no Table scope")
	}
	if !envelope.OK {
		return nil
	}
	if len(envelope.Results) != 1 || envelope.Results[0].Page == nil || envelope.Results[0].Page.Snapshot == "" {
		return invalid("root prefetch has no auditable page snapshot")
	}
	table, ok := findCatalogTable(frame.CatalogTables, *call.Table)
	if !ok {
		return invalid("root prefetch Table is absent from the current Catalog frame")
	}
	for _, row := range envelope.Results[0].Rows {
		databaseID, _ := row["database_id"].(string)
		tableID, _ := row["table_id"].(string)
		routeID, _ := row["route_id"].(string)
		if (databaseID != "" && databaseID != table.DatabaseID) ||
			(tableID != "" && tableID != table.TableID) || routeID == "" {
			return invalid("root prefetch row violates its Table scope")
		}
		if _, leaked := row["row_id"]; leaked {
			return invalid("root prefetch leaked a factual Row locator")
		}
	}
	frame.PrefetchedRoots = append(frame.PrefetchedRoots, PrefetchedRoot{
		TableRef: *call.Table, Snapshot: envelope.Results[0].Page.Snapshot,
		Routes: cloneRows(envelope.Results[0].Rows), Truncated: envelope.Results[0].Truncated,
	})
	frame.Audit.PrefetchedTables++
	frame.Truncated = frame.Truncated || envelope.Truncated
	return nil
}

func validateRequest(request Request) error {
	if !validTopic(request.TopicID) || !validName(request.Actor) ||
		len(request.AuthorizedDatabases) == 0 || len(request.AuthorizedDatabases) > maxDatabases {
		return invalid("topic, actor, and 1 to %d authorized Databases are required", maxDatabases)
	}
	seen := make(map[string]struct{}, len(request.AuthorizedDatabases))
	for _, database := range request.AuthorizedDatabases {
		if !validName(database) {
			return invalid("authorized Database identity is invalid")
		}
		key := strings.ToLower(database)
		if _, duplicate := seen[key]; duplicate {
			return invalid("authorized Databases must be unique")
		}
		seen[key] = struct{}{}
	}
	if !utf8.ValidString(request.LexicalQuery) || strings.TrimSpace(request.LexicalQuery) == "" ||
		utf8.RuneCountInString(request.LexicalQuery) > 256 {
		return invalid("lexical query must contain 1 to 256 Unicode characters")
	}
	if request.CandidateLimit < 1 || request.CandidateLimit > maxCandidates ||
		request.CandidateUTF8ByteLimit < 256 || request.CandidateUTF8ByteLimit > maxCandidateBytes ||
		request.TableLimit < 1 || request.TableLimit > maxTableRows ||
		request.RootLimit < 1 || request.RootLimit > maxRootRows ||
		request.ContextUTF8ByteLimit < 256 || request.ContextUTF8ByteLimit > maxContextBytes {
		return invalid("discovery budgets exceed the canonical profile")
	}
	if request.Vector != nil {
		if request.CandidateLimit < 2 || request.CandidateUTF8ByteLimit < 512 {
			return invalid("two predictors require at least two candidates and 512 bytes")
		}
		if _, err := routeexact.Search(routeexact.Query{
			SpaceDigest: request.Vector.SpaceDigest,
			Vector:      append([]float32(nil), request.Vector.Values...), Limit: 1,
		}, nil); err != nil {
			return invalid("vector query is invalid: %v", err)
		}
	}
	if len(request.PrefetchTables) > maxPrefetchTables {
		return invalid("at most %d Table roots may be prefetched", maxPrefetchTables)
	}
	if len(request.PrefetchTables) > 0 && request.PrefetchFrameTopicID != request.TopicID {
		return invalid("prefetched roots must come from the current topic Frame")
	}
	seenTables := make(map[string]struct{}, len(request.PrefetchTables))
	for _, table := range request.PrefetchTables {
		if !validName(table.Database) || !validName(table.Table) ||
			!containsString(request.AuthorizedDatabases, table.Database) {
			return invalid("prefetched Table is outside the authorized scope")
		}
		key := strings.ToLower(table.Database + "\x00" + table.Table)
		if _, duplicate := seenTables[key]; duplicate {
			return invalid("prefetched Tables must be unique")
		}
		seenTables[key] = struct{}{}
	}
	return nil
}

func authorizedInput(actor string, databases []string, named map[string]any) executor.StatementInput {
	parameters := make(map[string]any, len(named))
	for key, value := range named {
		parameters[key] = cloneValue(value)
	}
	return executor.StatementInput{
		Parameters: executor.Parameters{Named: parameters},
		Authorization: security.Authorization{
			Version: security.AuthorizationVersion, Actor: actor,
			AuthorizedDatabases: append([]string(nil), databases...),
		},
	}
}

func fallbackCall(actor string, databases []string, rootLimit int, table TableRef) Call {
	cloned := table
	return Call{
		ID:   "fallback-root-" + sanitizeID(table.Database) + "-" + sanitizeID(table.Table),
		Kind: CallRootFallback,
		MSQL: fmt.Sprintf("SHOW ROUTES FROM TABLE %s.%s AT ROOT LIMIT :root_limit",
			quoteIdentifier(table.Database), quoteIdentifier(table.Table)),
		Input: authorizedInput(actor, databases, map[string]any{"root_limit": int64(rootLimit)}),
		Table: &cloned,
	}
}

func frameHasTable(frame Frame, table TableRef) bool {
	_, ok := findCatalogTable(frame.CatalogTables, table)
	return ok
}

func findCatalogTable(values []CatalogTable, table TableRef) (CatalogTable, bool) {
	for _, value := range values {
		if equalTable(value.TableRef, table) {
			return value, true
		}
	}
	return CatalogTable{}, false
}

func equalTable(left, right TableRef) bool {
	return strings.EqualFold(left.Database, right.Database) && strings.EqualFold(left.Table, right.Table)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(value, target) {
			return true
		}
	}
	return false
}

func validTopic(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" &&
		utf8.RuneCountInString(value) <= 128 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func validName(value string) bool {
	return utf8.ValidString(value) && strings.TrimSpace(value) == value && value != "" &&
		utf8.RuneCountInString(value) <= 200 && !strings.ContainsAny(value, "\x00\r\n\t")
}

func quoteIdentifier(value string) string { return "`" + strings.ReplaceAll(value, "`", "``") + "`" }

func sanitizeID(value string) string {
	value = strings.ToLower(value)
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '-' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('-')
		}
	}
	return strings.Trim(builder.String(), "-")
}

func clonePlan(plan Plan) Plan {
	plan.AuthorizedDatabases = append([]string(nil), plan.AuthorizedDatabases...)
	calls := plan.Calls
	plan.Calls = make([]Call, len(calls))
	for index, call := range calls {
		plan.Calls[index] = cloneCall(call)
	}
	return plan
}

func cloneCall(call Call) Call {
	call.Input.Authorization.AuthorizedDatabases = append([]string(nil), call.Input.Authorization.AuthorizedDatabases...)
	call.Input.Parameters.Named = cloneMap(call.Input.Parameters.Named)
	call.Input.Parameters.Positional = append([]any(nil), call.Input.Parameters.Positional...)
	if call.Table != nil {
		table := *call.Table
		call.Table = &table
	}
	return call
}

func cloneFrame(frame Frame) Frame {
	frame.CatalogDatabases = cloneRows(frame.CatalogDatabases)
	frame.CatalogPageSnapshots = append([]string(nil), frame.CatalogPageSnapshots...)
	frame.CatalogTables = append([]CatalogTable(nil), frame.CatalogTables...)
	for index := range frame.CatalogTables {
		frame.CatalogTables[index].Row = cloneRow(frame.CatalogTables[index].Row)
	}
	frame.Predictors = append([]Predictor(nil), frame.Predictors...)
	frame.Candidates = cloneCandidates(frame.Candidates)
	frame.PrefetchedRoots = append([]PrefetchedRoot(nil), frame.PrefetchedRoots...)
	for index := range frame.PrefetchedRoots {
		frame.PrefetchedRoots[index].Routes = cloneRows(frame.PrefetchedRoots[index].Routes)
	}
	frame.authorizedDatabases = append([]string(nil), frame.authorizedDatabases...)
	return frame
}

func cloneCandidates(values []discovery.Candidate) []discovery.Candidate {
	result := make([]discovery.Candidate, len(values))
	for index, value := range values {
		result[index] = value
		if value.Score != nil {
			score := *value.Score
			result[index].Score = &score
		}
		result[index].MatchedFields = append([]string(nil), value.MatchedFields...)
	}
	return result
}

func cloneRows(values []result.Row) []result.Row {
	cloned := make([]result.Row, len(values))
	for index, row := range values {
		cloned[index] = cloneRow(row)
	}
	return cloned
}

func cloneRow(row result.Row) result.Row {
	cloned := make(result.Row, len(row))
	for key, value := range row {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneMap(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneValue(value)
	}
	return cloned
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case []float32:
		return append([]float32(nil), typed...)
	case []string:
		return append([]string(nil), typed...)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneValue(item)
		}
		return result
	case map[string]any:
		return cloneMap(typed)
	default:
		return value
	}
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
