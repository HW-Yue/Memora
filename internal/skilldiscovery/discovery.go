// Package skilldiscovery plans and audits the Canonical Skill's speculative
// navigation calls. It does not choose a Table, read facts, or answer a task.
package skilldiscovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routeexact"
	"github.com/HW-Yue/Memora/internal/security"
)

const Version = "memora.speculative-discovery/v2"

const (
	maxDatabases      = 32
	maxCandidates     = 8
	maxCandidateBytes = 4096
	maxPrefetchTables = 2
	maxToolCalls      = 10
	maxAtlasEntries   = 64
	maxAtlasBytes     = 8192
	maxRootRows       = 12
	maxContextBytes   = 12000
)

var ErrInvalid = errors.New("invalid speculative discovery")

type CallKind string

const (
	CallCatalog      CallKind = "catalog"
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
	AtlasEntryLimit        int
	AtlasUTF8ByteLimit     int
	RootLimit              int
	ContextUTF8ByteLimit   int
}

type Budget struct {
	MaxToolCalls           int
	CandidateLimit         int
	CandidateUTF8ByteLimit int
	PrefetchTableLimit     int
	ContextUTF8ByteLimit   int
	AtlasEntryLimit        int
	AtlasUTF8ByteLimit     int
}

type Call struct {
	ID                     string
	Kind                   CallKind
	MSQL                   string
	Input                  executor.StatementInput
	Table                  *TableRef
	CandidateLimit         int
	CandidateUTF8ByteLimit int
	AtlasEntryLimit        int
	AtlasUTF8ByteLimit     int
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

// PredictorStatus is the Agent's own bookkeeping. The Discovery Frame stopped
// reporting whether a predictor succeeded — a caller that knows which predictor
// answered starts choosing between them — but the discovery loop still has to
// record which of its own calls came back empty-handed.
type PredictorStatus string

const (
	PredictorSucceeded   PredictorStatus = "succeeded"
	PredictorUnavailable PredictorStatus = "unavailable"
)

type Predictor struct {
	Predictor       string
	Status          PredictorStatus
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

type CatalogCoverage struct {
	Snapshot    string
	Pages       int
	EntriesSeen int
	Complete    bool
	NextCursor  string
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
	CatalogCoverage      CatalogCoverage
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
	atlasEntryLimit      int
	atlasUTF8ByteLimit   int
	contextUTF8ByteLimit int
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
		AtlasEntryLimit: request.AtlasEntryLimit, AtlasUTF8ByteLimit: request.AtlasUTF8ByteLimit,
	}
	plan := Plan{
		Version: Version, TopicID: request.TopicID, AuthorizedDatabases: authorized,
		Calls: []Call{}, Budget: budget, rootLimit: request.RootLimit, actor: request.Actor,
	}
	plan.Calls = append(plan.Calls, Call{
		ID: "discover-01-catalog", Kind: CallCatalog,
		MSQL: "SHOW CATALOG ATLAS LIMIT :atlas_limit BYTES :atlas_bytes COMPACT",
		Input: authorizedInput(request.Actor, authorized, map[string]any{
			"atlas_limit": int64(request.AtlasEntryLimit), "atlas_bytes": int64(request.AtlasUTF8ByteLimit),
		}),
		AtlasEntryLimit: request.AtlasEntryLimit, AtlasUTF8ByteLimit: request.AtlasUTF8ByteLimit,
	})
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
		atlasEntryLimit: plan.Budget.AtlasEntryLimit, atlasUTF8ByteLimit: plan.Budget.AtlasUTF8ByteLimit,
		contextUTF8ByteLimit: plan.Budget.ContextUTF8ByteLimit,
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
			if err := consumeCatalogAtlas(&frame, call, envelope, true); err != nil {
				return Frame{}, err
			}
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
	if current && frame.CatalogCoverage.Complete && !frameHasTable(frame, table) {
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

func (frame Frame) NextCatalogPage(topicID string) (Call, bool, error) {
	if !frame.Reusable(topicID) {
		return Call{}, false, invalid("Catalog continuation requires the current topic Frame")
	}
	if frame.CatalogCoverage.Complete {
		return Call{}, false, nil
	}
	if frame.CatalogCoverage.NextCursor == "" {
		return Call{}, false, invalid("partial Catalog coverage omitted its continuation")
	}
	call := Call{ID: fmt.Sprintf("discover-catalog-page-%02d", frame.CatalogCoverage.Pages+1), Kind: CallCatalog,
		MSQL: "SHOW CATALOG ATLAS CURSOR :atlas_cursor LIMIT :atlas_limit BYTES :atlas_bytes COMPACT",
		Input: authorizedInput(frame.actor, frame.authorizedDatabases, map[string]any{
			"atlas_cursor": frame.CatalogCoverage.NextCursor, "atlas_limit": int64(frame.atlasEntryLimit),
			"atlas_bytes": int64(frame.atlasUTF8ByteLimit),
		}), AtlasEntryLimit: frame.atlasEntryLimit, AtlasUTF8ByteLimit: frame.atlasUTF8ByteLimit}
	return cloneCall(call), true, nil
}

func (frame Frame) ContinueCatalog(topicID string, call Call, envelope result.Envelope) (Frame, error) {
	want, available, err := frame.NextCatalogPage(topicID)
	if err != nil || !available {
		if err != nil {
			return Frame{}, err
		}
		return Frame{}, invalid("Catalog coverage is already complete")
	}
	if call.ID != want.ID || call.Kind != want.Kind || call.MSQL != want.MSQL ||
		!reflectCallInput(call.Input, want.Input) {
		return Frame{}, invalid("Catalog continuation call does not match the current cursor")
	}
	if envelope.RequestID != call.ID {
		return Frame{}, invalid("Catalog continuation response is not bound to its call")
	}
	if err := envelope.Validate(); err != nil {
		return Frame{}, invalid("Catalog continuation response is invalid: %v", err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return Frame{}, invalid("Catalog continuation cannot be audited: %v", err)
	}
	next := cloneFrame(frame)
	next.Audit.ToolCalls++
	next.Audit.MSQLStatements += len(envelope.Results)
	next.Audit.OutputUTF8Bytes += len(encoded)
	if err := consumeCatalogAtlas(&next, call, envelope, false); err != nil {
		return Frame{}, err
	}
	if next.Audit.OutputUTF8Bytes > next.contextUTF8ByteLimit {
		next.invalidated, next.Truncated = true, true
		next.Candidates, next.PrefetchedRoots = []discovery.Candidate{}, []PrefetchedRoot{}
	}
	return cloneFrame(next), nil
}

func consumePredictor(frame *Frame, call Call, envelope result.Envelope) error {
	if !envelope.OK || len(envelope.Results) != 1 || envelope.Results[0].Discovery == nil {
		frame.Predictors = append(frame.Predictors, Predictor{
			Predictor: string(call.Kind) + "-route/v1", Status: PredictorUnavailable,
		})
		return nil
	}
	value := envelope.Results[0].Discovery
	if value.Usage != discovery.UsageNavigationOnly || int(value.Limit) > call.CandidateLimit {
		return invalid("predictor %q violated its allocated navigation budget", call.ID)
	}
	// The frame no longer reports how many bytes it used, so the loop measures
	// what it received rather than trusting a self-report.
	candidateBytes, err := candidateUTF8Bytes(value.Candidates)
	if err != nil || candidateBytes > call.CandidateUTF8ByteLimit {
		return invalid("predictor %q violated its allocated navigation budget", call.ID)
	}
	if frame.CatalogRevision == "" {
		frame.CatalogRevision = value.CatalogRevision
	} else if frame.CatalogRevision != value.CatalogRevision {
		return invalid("predictor Catalog revisions do not match")
	}
	// The predictor is named from the call the loop made, not from the answer:
	// the frame deliberately no longer says who produced it.
	frame.Predictors = append(frame.Predictors, Predictor{
		Predictor: string(call.Kind) + "-route/v1", Status: PredictorSucceeded, Snapshot: value.Snapshot,
		CatalogRevision: value.CatalogRevision, CandidateCount: uint64(len(value.Candidates)),
		Truncated: value.Truncated,
	})
	frame.Audit.CandidatesUsed += len(value.Candidates)
	frame.Audit.CandidateUTF8BytesUsed += candidateBytes
	frame.Candidates = append(frame.Candidates, cloneCandidates(value.Candidates)...)
	frame.Truncated = frame.Truncated || value.Truncated || envelope.Truncated
	return nil
}

func consumeCatalogAtlas(frame *Frame, call Call, envelope result.Envelope, initial bool) error {
	resultIndex := 0
	if initial {
		if !envelope.OK || len(envelope.Results) != 1 {
			return invalid("initial Catalog Atlas discovery failed")
		}
	} else if !envelope.OK || len(envelope.Results) != 1 {
		return invalid("Catalog Atlas continuation failed")
	}
	statement := envelope.Results[resultIndex]
	if statement.Page == nil || statement.Page.Snapshot == "" || len(statement.Rows) > call.AtlasEntryLimit ||
		(statement.Page.Truncated && len(statement.Rows) == 0) {
		return invalid("Catalog Atlas page is missing its bounded snapshot")
	}
	if statement.Page.Truncated != statement.Truncated || statement.Truncated != envelope.Truncated {
		return invalid("Catalog Atlas truncation metadata is inconsistent")
	}
	if statement.Truncated == (statement.NextCursor == "") || statement.NextCursor != statement.Page.NextCursor {
		return invalid("Catalog Atlas continuation metadata is inconsistent")
	}
	if frame.CatalogCoverage.Snapshot == "" {
		frame.CatalogCoverage.Snapshot = statement.Page.Snapshot
	} else if frame.CatalogCoverage.Snapshot != statement.Page.Snapshot {
		return invalid("Catalog Atlas continuation changed snapshot")
	}
	knownDatabases, knownTables := map[string]bool{}, map[string]bool{}
	for _, row := range frame.CatalogDatabases {
		identity, _ := row["database_id"].(string)
		knownDatabases[identity] = true
	}
	for _, table := range frame.CatalogTables {
		knownTables[table.TableID] = true
	}
	for _, row := range statement.Rows {
		if _, leaked := row["row_id"]; leaked {
			return invalid("Catalog Atlas leaked a Row locator")
		}
		if _, leaked := row["columns"]; leaked {
			return invalid("Catalog Atlas expanded Columns")
		}
		if _, leaked := row["route_id"]; leaked {
			return invalid("Catalog Atlas leaked a Route")
		}
		kind, _ := row["kind"].(string)
		database, _ := row["database"].(string)
		databaseID, _ := row["database_id"].(string)
		if database == "" || databaseID == "" || !catalogDatabaseAuthorized(*frame, row) {
			return invalid("Catalog Atlas row is outside the authorized scope")
		}
		switch kind {
		case "database":
			if knownDatabases[databaseID] {
				return invalid("Catalog Atlas repeated a Database")
			}
			knownDatabases[databaseID] = true
			frame.CatalogDatabases = append(frame.CatalogDatabases, cloneRow(row))
		case "table":
			table, _ := row["table"].(string)
			tableID, _ := row["table_id"].(string)
			if table == "" || tableID == "" || knownTables[tableID] {
				return invalid("Catalog Atlas Table row is incomplete or repeated")
			}
			knownTables[tableID] = true
			frame.CatalogTables = append(frame.CatalogTables, CatalogTable{TableRef: TableRef{
				Database: database, Table: table}, DatabaseID: databaseID, TableID: tableID, Row: cloneRow(row)})
		default:
			return invalid("Catalog Atlas row kind is unsupported")
		}
	}
	frame.CatalogPageSnapshots = append(frame.CatalogPageSnapshots, statement.Page.Snapshot)
	frame.CatalogCoverage.Pages++
	frame.CatalogCoverage.EntriesSeen += len(statement.Rows)
	frame.CatalogCoverage.Complete = !statement.Truncated
	frame.CatalogCoverage.NextCursor = statement.NextCursor
	return nil
}

func catalogDatabaseAuthorized(frame Frame, row result.Row) bool {
	selectors := []string{}
	for _, key := range []string{"database_id", "database"} {
		if value, ok := row[key].(string); ok {
			selectors = append(selectors, value)
		}
	}
	appendAliases := func(value any) {
		switch aliases := value.(type) {
		case []string:
			selectors = append(selectors, aliases...)
		case []any:
			for _, alias := range aliases {
				if text, ok := alias.(string); ok {
					selectors = append(selectors, text)
				}
			}
		}
	}
	if kind, _ := row["kind"].(string); kind == "database" {
		appendAliases(row["aliases"])
	}
	databaseID, _ := row["database_id"].(string)
	for _, database := range frame.CatalogDatabases {
		knownID, _ := database["database_id"].(string)
		if knownID == databaseID {
			appendAliases(database["aliases"])
		}
	}
	for _, selector := range selectors {
		if containsString(frame.authorizedDatabases, selector) {
			return true
		}
	}
	return false
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
		request.AtlasEntryLimit < 1 || request.AtlasEntryLimit > maxAtlasEntries ||
		request.AtlasUTF8ByteLimit < 512 || request.AtlasUTF8ByteLimit > maxAtlasBytes ||
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
			DefaultLevel:        security.LevelRead,
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

// cloneCandidates copies the candidate list. A Candidate is now a plain value
// with no pointers or slices in it, so the copy is the whole clone.
func cloneCandidates(values []discovery.Candidate) []discovery.Candidate {
	return append([]discovery.Candidate(nil), values...)
}

// candidateUTF8Bytes measures the encoded size of a candidate list.
func candidateUTF8Bytes(values []discovery.Candidate) (int, error) {
	total := 0
	for _, value := range values {
		encoded, err := json.Marshal(value)
		if err != nil {
			return 0, err
		}
		total += len(encoded)
	}
	return total, nil
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

func reflectCallInput(left, right executor.StatementInput) bool {
	return reflect.DeepEqual(left, right)
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
