package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	BootstrapFrameVersion    = "memora.query-bootstrap-frame/v1"
	BootstrapUsageNavigation = "navigation_only"
)

var (
	ErrInvalidBootstrapRequest = errors.New("query Bootstrap request is invalid")
	ErrInvalidBootstrapResult  = errors.New("query Bootstrap MSQL result is invalid")
	ErrBootstrapSnapshot       = errors.New("query Bootstrap snapshot changed")
	ErrBootstrapBudget         = errors.New("query Bootstrap Frame exceeds its byte budget")
)

// BootstrapBudget bounds database output and the final model-facing Frame.
type BootstrapBudget struct {
	AtlasEntryLimit      uint64 `json:"atlas_entry_limit"`
	AtlasUTF8Bytes       uint64 `json:"atlas_utf8_bytes"`
	MaxAtlasPages        uint64 `json:"max_atlas_pages"`
	LexicalLocationLimit uint64 `json:"lexical_location_limit"`
	LexicalUTF8Bytes     uint64 `json:"lexical_utf8_bytes"`
	RootTableLimit       uint64 `json:"root_table_limit"`
	RootRouteLimit       uint64 `json:"root_route_limit"`
	FrameUTF8Bytes       uint64 `json:"frame_utf8_bytes"`
}

func DefaultBootstrapBudget() BootstrapBudget {
	return BootstrapBudget{
		AtlasEntryLimit: 64, AtlasUTF8Bytes: 8192, MaxAtlasPages: 8,
		LexicalLocationLimit: 16, LexicalUTF8Bytes: 2048,
		RootTableLimit: 2, RootRouteLimit: 12, FrameUTF8Bytes: 12000,
	}
}

type BootstrapRequest struct {
	Query         string
	Authorization protocolmsql.Authorization
	Budget        BootstrapBudget
}

type CatalogBootstrap struct {
	Entries    []protocolmsql.Row `json:"entries"`
	Snapshot   string             `json:"snapshot"`
	Pages      uint64             `json:"pages"`
	Complete   bool               `json:"complete"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type LexicalBootstrap struct {
	Locations  []protocolmsql.Row `json:"locations"`
	Snapshot   string             `json:"snapshot"`
	Truncated  bool               `json:"truncated"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type RootPrefetchStatus string

const (
	RootPrefetchSucceeded     RootPrefetchStatus = "succeeded"
	RootPrefetchUnavailable   RootPrefetchStatus = "unavailable"
	RootPrefetchBudgetSkipped RootPrefetchStatus = "budget_skipped"
)

type RootPrefetch struct {
	DatabaseID string             `json:"database_id"`
	TableID    string             `json:"table_id"`
	Database   string             `json:"database,omitempty"`
	Table      string             `json:"table,omitempty"`
	Status     RootPrefetchStatus `json:"status"`
	Code       protocolmsql.Code  `json:"code,omitempty"`
	Reason     string             `json:"reason,omitempty"`
	Routes     []protocolmsql.Row `json:"routes"`
	Snapshot   string             `json:"snapshot,omitempty"`
	Truncated  bool               `json:"truncated"`
	NextCursor string             `json:"next_cursor,omitempty"`
}

type BootstrapFrame struct {
	Version   string           `json:"version"`
	Usage     string           `json:"usage"`
	Catalog   CatalogBootstrap `json:"catalog"`
	Lexical   LexicalBootstrap `json:"lexical"`
	Roots     []RootPrefetch   `json:"root_prefetches"`
	UTF8Bytes int              `json:"utf8_bytes"`
	Truncated bool             `json:"truncated"`
}

// Root returns a usable prefetched root. Unavailable and skipped receipts are
// intentionally treated as misses.
func (frame BootstrapFrame) Root(databaseID, tableID string) (RootPrefetch, bool) {
	for _, root := range frame.Roots {
		if root.DatabaseID == databaseID && root.TableID == tableID && root.Status == RootPrefetchSucceeded {
			return root, true
		}
	}
	return RootPrefetch{}, false
}

func (frame BootstrapFrame) NeedsRootFallback(databaseID, tableID string) bool {
	_, found := frame.Root(databaseID, tableID)
	return !found
}

type BootstrapAssembler struct {
	msql MSQLExecutor
}

func NewBootstrapAssembler(msql MSQLExecutor) (*BootstrapAssembler, error) {
	if msql == nil {
		return nil, ErrInvalidDependencies
	}
	return &BootstrapAssembler{msql: msql}, nil
}

func (assembler *BootstrapAssembler) Assemble(
	ctx context.Context,
	request BootstrapRequest,
) (BootstrapFrame, error) {
	if err := validateBootstrapRequest(ctx, request); err != nil {
		return BootstrapFrame{}, err
	}
	initialRequest := makeInitialBootstrapRequest(request)
	if err := initialRequest.Validate(); err != nil {
		return BootstrapFrame{}, fmt.Errorf("%w: %v", ErrInvalidBootstrapRequest, err)
	}
	initial, err := assembler.execute(ctx, initialRequest)
	if err != nil {
		return BootstrapFrame{}, err
	}
	if len(initial.Results) != 2 {
		return BootstrapFrame{}, fmt.Errorf("%w: initial request returned %d statements", ErrInvalidBootstrapResult, len(initial.Results))
	}
	atlas, err := requiredPage(initial.Results[0], "Catalog Atlas")
	if err != nil {
		return BootstrapFrame{}, err
	}
	lexical, err := requiredPage(initial.Results[1], "lexical locations")
	if err != nil {
		return BootstrapFrame{}, err
	}
	frame := BootstrapFrame{
		Version: BootstrapFrameVersion, Usage: BootstrapUsageNavigation,
		Catalog: CatalogBootstrap{
			Entries: cloneProtocolRows(initial.Results[0].Rows), Snapshot: atlas.Snapshot, Pages: 1,
			Complete: !atlas.Truncated, NextCursor: atlas.NextCursor,
		},
		Lexical: LexicalBootstrap{
			Locations: cloneProtocolRows(initial.Results[1].Rows), Snapshot: lexical.Snapshot,
			Truncated: lexical.Truncated, NextCursor: lexical.NextCursor,
		},
		Roots:     []RootPrefetch{},
		Truncated: atlas.Truncated || lexical.Truncated,
	}
	if err := fitFrame(&frame, request.Budget.FrameUTF8Bytes); err != nil {
		return BootstrapFrame{}, err
	}
	if err := assembler.continueAtlas(ctx, request, &frame); err != nil {
		return BootstrapFrame{}, err
	}
	if err := assembler.prefetchRoots(ctx, request, &frame); err != nil {
		return BootstrapFrame{}, err
	}
	if err := fitFrame(&frame, request.Budget.FrameUTF8Bytes); err != nil {
		return BootstrapFrame{}, err
	}
	return frame, nil
}

func validateBootstrapRequest(ctx context.Context, request BootstrapRequest) error {
	if ctx == nil || strings.TrimSpace(request.Query) == "" || utf8.RuneCountInString(request.Query) > 256 {
		return ErrInvalidBootstrapRequest
	}
	budget := request.Budget
	if budget.AtlasEntryLimit < 1 || budget.AtlasEntryLimit > 256 ||
		budget.AtlasUTF8Bytes < 512 || budget.AtlasUTF8Bytes > 65536 ||
		budget.MaxAtlasPages < 1 || budget.MaxAtlasPages > 8 ||
		budget.LexicalLocationLimit < 1 || budget.LexicalLocationLimit > 64 ||
		budget.LexicalUTF8Bytes < 256 || budget.LexicalUTF8Bytes > 65536 ||
		budget.RootTableLimit > 2 || budget.RootRouteLimit < 1 || budget.RootRouteLimit > 64 ||
		budget.FrameUTF8Bytes < 4096 || budget.FrameUTF8Bytes > 65536 {
		return ErrInvalidBootstrapRequest
	}
	return nil
}

func (assembler *BootstrapAssembler) execute(
	ctx context.Context,
	request protocolmsql.Request,
) (protocolmsql.Envelope, error) {
	if err := request.Validate(); err != nil {
		return protocolmsql.Envelope{}, fmt.Errorf("%w: %v", ErrInvalidBootstrapRequest, err)
	}
	envelope, err := assembler.msql.ExecuteMSQL(ctx, request)
	if err != nil {
		return protocolmsql.Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return protocolmsql.Envelope{}, fmt.Errorf("%w: %v", ErrInvalidBootstrapResult, err)
	}
	return envelope, nil
}

func (assembler *BootstrapAssembler) continueAtlas(
	ctx context.Context,
	request BootstrapRequest,
	frame *BootstrapFrame,
) error {
	for !frame.Catalog.Complete && frame.Catalog.Pages < request.Budget.MaxAtlasPages {
		continuation := makeAtlasContinuationRequest(frame.Catalog.NextCursor, request)
		envelope, err := assembler.execute(ctx, continuation)
		if err != nil {
			return err
		}
		if len(envelope.Results) != 1 {
			return fmt.Errorf("%w: Atlas continuation returned %d statements", ErrInvalidBootstrapResult, len(envelope.Results))
		}
		page, err := requiredPage(envelope.Results[0], "Catalog Atlas continuation")
		if err != nil {
			return err
		}
		if page.Snapshot != frame.Catalog.Snapshot {
			return fmt.Errorf("%w: got %q, want %q", ErrBootstrapSnapshot, page.Snapshot, frame.Catalog.Snapshot)
		}
		candidate := *frame
		candidate.Catalog = frame.Catalog
		candidate.Catalog.Entries = append(cloneProtocolRows(frame.Catalog.Entries), cloneProtocolRows(envelope.Results[0].Rows)...)
		candidate.Catalog.Pages++
		candidate.Catalog.Complete = !page.Truncated
		candidate.Catalog.NextCursor = page.NextCursor
		candidate.Truncated = candidate.Lexical.Truncated || !candidate.Catalog.Complete || rootsTruncated(candidate.Roots)
		if err := fitFrame(&candidate, request.Budget.FrameUTF8Bytes); err != nil {
			frame.Truncated = true
			return fitFrame(frame, request.Budget.FrameUTF8Bytes)
		}
		*frame = candidate
	}
	if !frame.Catalog.Complete {
		frame.Truncated = true
	}
	return fitFrame(frame, request.Budget.FrameUTF8Bytes)
}

func (assembler *BootstrapAssembler) prefetchRoots(
	ctx context.Context,
	request BootstrapRequest,
	frame *BootstrapFrame,
) error {
	if request.Budget.RootTableLimit == 0 {
		return nil
	}
	tables := catalogTables(frame.Catalog.Entries)
	candidates := lexicalTables(frame.Lexical.Locations, request.Budget.RootTableLimit)
	queryable := make([]tableReference, 0, len(candidates))
	for _, candidate := range candidates {
		if table, ok := tables[candidate.key()]; ok {
			queryable = append(queryable, table)
			continue
		}
		receipt := RootPrefetch{
			DatabaseID: candidate.DatabaseID, TableID: candidate.TableID,
			Status: RootPrefetchUnavailable, Code: "catalog_partial",
			Reason: "Table metadata is outside the current Atlas coverage", Routes: []protocolmsql.Row{},
		}
		appendRootReceipt(frame, receipt, request.Budget.FrameUTF8Bytes)
	}
	if len(queryable) == 0 {
		return nil
	}
	rootRequest := makeRootRequest(queryable, request)
	envelope, err := assembler.execute(ctx, rootRequest)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		for _, table := range queryable {
			appendRootReceipt(frame, RootPrefetch{
				DatabaseID: table.DatabaseID, TableID: table.TableID,
				Database: table.Database, Table: table.Table,
				Status: RootPrefetchUnavailable, Code: "executor_error",
				Reason: boundedReason(err.Error()), Routes: []protocolmsql.Row{},
			}, request.Budget.FrameUTF8Bytes)
		}
		return fitFrame(frame, request.Budget.FrameUTF8Bytes)
	}
	if len(envelope.Results) != len(queryable) {
		return fmt.Errorf("%w: root prefetch returned %d statements, want %d", ErrInvalidBootstrapResult, len(envelope.Results), len(queryable))
	}
	for index, table := range queryable {
		result := envelope.Results[index]
		receipt := RootPrefetch{
			DatabaseID: table.DatabaseID, TableID: table.TableID,
			Database: table.Database, Table: table.Table, Routes: []protocolmsql.Row{},
		}
		if result.Status != protocolmsql.Status("succeeded") || result.Error != nil {
			receipt.Status = RootPrefetchUnavailable
			if result.Error != nil {
				receipt.Code = result.Error.Code
				receipt.Reason = boundedReason(result.Error.Message)
			} else {
				receipt.Code = "invalid_result"
				receipt.Reason = "root prefetch did not succeed"
			}
			appendRootReceipt(frame, receipt, request.Budget.FrameUTF8Bytes)
			continue
		}
		page, pageErr := validPage(result)
		if pageErr != nil {
			receipt.Status, receipt.Code = RootPrefetchUnavailable, "invalid_result"
			receipt.Reason = "root prefetch returned an invalid page"
			appendRootReceipt(frame, receipt, request.Budget.FrameUTF8Bytes)
			continue
		}
		receipt.Status = RootPrefetchSucceeded
		receipt.Routes = cloneProtocolRows(result.Rows)
		receipt.Snapshot = page.Snapshot
		receipt.Truncated = page.Truncated
		receipt.NextCursor = page.NextCursor
		if !appendRootReceipt(frame, receipt, request.Budget.FrameUTF8Bytes) {
			skipped := RootPrefetch{
				DatabaseID: table.DatabaseID, TableID: table.TableID,
				Database: table.Database, Table: table.Table,
				Status: RootPrefetchBudgetSkipped, Code: "frame_budget",
				Reason: "root prefetch exceeded the Bootstrap Frame byte budget",
				Routes: []protocolmsql.Row{},
			}
			appendRootReceipt(frame, skipped, request.Budget.FrameUTF8Bytes)
		}
	}
	return fitFrame(frame, request.Budget.FrameUTF8Bytes)
}

func requiredPage(result protocolmsql.StatementResult, label string) (*protocolmsql.ListPage, error) {
	if result.Status != protocolmsql.Status("succeeded") || result.Error != nil {
		return nil, fmt.Errorf("%w: %s failed", ErrInvalidBootstrapResult, label)
	}
	page, err := validPage(result)
	if err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalidBootstrapResult, label, err)
	}
	return page, nil
}

func validPage(result protocolmsql.StatementResult) (*protocolmsql.ListPage, error) {
	page := result.Page
	if page == nil || page.Version != "memora.list-page/v1" || strings.TrimSpace(page.Snapshot) == "" ||
		page.Truncated != result.Truncated || page.NextCursor != result.NextCursor ||
		(page.Truncated && page.NextCursor == "") || (!page.Truncated && page.NextCursor != "") {
		return nil, errors.New("page metadata is inconsistent")
	}
	return page, nil
}

func fitFrame(frame *BootstrapFrame, limit uint64) error {
	for range 8 {
		encoded, err := json.Marshal(frame)
		if err != nil {
			return fmt.Errorf("%w: Frame could not be encoded", ErrInvalidBootstrapResult)
		}
		if frame.UTF8Bytes == len(encoded) {
			if uint64(len(encoded)) > limit {
				return fmt.Errorf("%w: used %d of %d bytes", ErrBootstrapBudget, len(encoded), limit)
			}
			return nil
		}
		frame.UTF8Bytes = len(encoded)
	}
	return fmt.Errorf("%w: byte receipt did not converge", ErrInvalidBootstrapResult)
}

func appendRootReceipt(frame *BootstrapFrame, receipt RootPrefetch, limit uint64) bool {
	candidate := *frame
	candidate.Roots = append(append([]RootPrefetch(nil), frame.Roots...), receipt)
	candidate.Truncated = candidate.Truncated || receipt.Truncated || receipt.Status == RootPrefetchBudgetSkipped
	if err := fitFrame(&candidate, limit); err != nil {
		frame.Truncated = true
		_ = fitFrame(frame, limit)
		return false
	}
	*frame = candidate
	return true
}

func rootsTruncated(roots []RootPrefetch) bool {
	for _, root := range roots {
		if root.Truncated || root.Status == RootPrefetchBudgetSkipped {
			return true
		}
	}
	return false
}

type tableReference struct {
	DatabaseID string
	TableID    string
	Database   string
	Table      string
}

func (table tableReference) key() string { return table.DatabaseID + "\x00" + table.TableID }

func catalogTables(rows []protocolmsql.Row) map[string]tableReference {
	tables := make(map[string]tableReference)
	for _, row := range rows {
		if text(row["kind"]) != "table" {
			continue
		}
		table := tableReference{
			DatabaseID: text(row["database_id"]), TableID: text(row["table_id"]),
			Database: text(row["database"]), Table: text(row["table"]),
		}
		if table.DatabaseID == "" || table.TableID == "" || table.Database == "" || table.Table == "" {
			continue
		}
		tables[table.key()] = table
	}
	return tables
}

func lexicalTables(rows []protocolmsql.Row, limit uint64) []tableReference {
	tables := make([]tableReference, 0, limit)
	seen := make(map[string]bool)
	for _, row := range rows {
		table := tableReference{DatabaseID: text(row["database_id"]), TableID: text(row["table_id"])}
		if table.DatabaseID == "" || table.TableID == "" || seen[table.key()] {
			continue
		}
		seen[table.key()] = true
		tables = append(tables, table)
		if uint64(len(tables)) == limit {
			break
		}
	}
	return tables
}

func text(value any) string {
	text, _ := value.(string)
	return text
}

func cloneProtocolRows(rows []protocolmsql.Row) []protocolmsql.Row {
	cloned := make([]protocolmsql.Row, len(rows))
	for index, source := range rows {
		row := make(protocolmsql.Row, len(source))
		for key, value := range source {
			row[key] = value
		}
		cloned[index] = row
	}
	return cloned
}

func boundedReason(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 240 {
		runes = runes[:240]
	}
	return string(runes)
}

func makeInitialBootstrapRequest(request BootstrapRequest) protocolmsql.Request {
	budget := request.Budget
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source: "SHOW CATALOG ATLAS LIMIT :atlas_limit BYTES :atlas_bytes COMPACT; " +
			"SHOW LEXICAL LOCATIONS FROM ALL TABLES USING :query LIMIT :location_limit BYTES :lexical_bytes",
		Statements: []protocolmsql.StatementInput{
			{
				Parameters: protocolmsql.Parameters{Named: map[string]any{
					"atlas_limit": int64(budget.AtlasEntryLimit), "atlas_bytes": int64(budget.AtlasUTF8Bytes),
				}},
				Authorization: request.Authorization,
			},
			{
				Parameters: protocolmsql.Parameters{Named: map[string]any{
					"query": request.Query, "location_limit": int64(budget.LexicalLocationLimit),
					"lexical_bytes": int64(budget.LexicalUTF8Bytes),
				}},
				Authorization: request.Authorization,
			},
		},
	}
}

func makeAtlasContinuationRequest(cursor string, request BootstrapRequest) protocolmsql.Request {
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SHOW CATALOG ATLAS CURSOR :cursor LIMIT :atlas_limit BYTES :atlas_bytes COMPACT",
		Statements: []protocolmsql.StatementInput{{
			Parameters: protocolmsql.Parameters{Named: map[string]any{
				"cursor": cursor, "atlas_limit": int64(request.Budget.AtlasEntryLimit),
				"atlas_bytes": int64(request.Budget.AtlasUTF8Bytes),
			}},
			Authorization: request.Authorization,
		}},
	}
}

func makeRootRequest(tables []tableReference, request BootstrapRequest) protocolmsql.Request {
	sources := make([]string, len(tables))
	statements := make([]protocolmsql.StatementInput, len(tables))
	for index, table := range tables {
		sources[index] = "SHOW ROUTES FROM TABLE " + quoteMSQLIdentifier(table.Database) + "." +
			quoteMSQLIdentifier(table.Table) + " AT ROOT LIMIT :root_limit"
		statements[index] = protocolmsql.StatementInput{
			Parameters: protocolmsql.Parameters{Named: map[string]any{
				"root_limit": int64(request.Budget.RootRouteLimit),
			}},
			Authorization: request.Authorization,
		}
	}
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: strings.Join(sources, "; "), Statements: statements,
	}
}

func quoteMSQLIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}
