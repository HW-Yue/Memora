package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/HW-Yue/Memora/internal/archive"
	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/repair"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

const maxQueryScan = 1000

type Catalog interface {
	DescribeDatabase(context.Context, string) (catalog.Database, error)
	DescribeTable(context.Context, string, string) (catalog.Table, error)
}

type Rows interface {
	Get(context.Context, string, string, string) (row.Row, error)
	ListPage(context.Context, string, string, int) ([]row.Row, bool, error)
	Insert(context.Context, string, string, map[string]any, row.WriteOptions) (row.Row, error)
	Update(context.Context, string, string, string, map[string]any, row.WriteOptions) (row.Row, error)
	Delete(context.Context, string, string, string, row.WriteOptions) (row.Row, error)
	AsOfRevision(context.Context, string, string, string, uint64) (row.Row, error)
	AsOfCommit(context.Context, string, string, string, uint64) (row.Row, error)
	HistoryPage(context.Context, string, string, string, string, int) ([]history.Record, history.ReadPage, error)
	RecallKeywords(context.Context, string, string, string, int) ([]recall.Hit, error)
	RecallNearest(context.Context, string, string, []float32, int) ([]recall.Hit, error)
	VectorStatus(context.Context, string, string) (recall.VectorStatus, error)
	RepairLinks(context.Context, string, int) (repair.Receipt, error)
	RepairVectorIndex(context.Context, string, int) (repair.VectorReceipt, error)
	ArchivePage(context.Context, string, string, string, string, int) ([]archive.Summary, archive.Page, error)
	ArchiveRecord(context.Context, string) (archive.Record, error)
	Restore(context.Context, string, string, string, uint64, row.WriteOptions) (row.Row, error)
	CreateRouterNode(context.Context, string, router.NodeDefinition) (router.Node, error)
	RenameRouterNode(context.Context, string, string, uint64) (router.Node, error)
	GetRouterNode(context.Context, string) (router.Node, error)
	ListRouterChildrenPage(context.Context, string, string, int) ([]router.Node, router.ReadPage, error)
	ListRouterLeafPage(context.Context, string, string, int) ([]router.Locator, router.ReadPage, error)
}

// PointReads owns exact autocommit SELECT reads when configured. Implementations
// must not fall back to Catalog or Row scans after an indexed lookup fails.
type PointReads interface {
	Capture(context.Context) (uint64, error)
	DescribeTable(context.Context, string, string) (catalog.Table, error)
	Get(context.Context, catalog.Table, string, uint64) (row.Row, error)
	AsOfRevision(context.Context, catalog.Table, string, uint64, uint64) (row.Row, error)
	AsOfCommit(context.Context, catalog.Table, string, uint64, uint64) (row.Row, error)
}

type Reshaper interface {
	Split(context.Context, string, string, []string, []map[string]any, row.ReshapeOptions) ([]row.Row, error)
	Merge(context.Context, string, string, []string, []map[string]any, row.ReshapeOptions) ([]row.Row, error)
}

type Engine struct {
	catalog       Catalog
	catalogBinder *binder.Catalog
	rows          Rows
	points        PointReads
}

type Parameters struct {
	Named      map[string]any `json:"named,omitempty"`
	Positional []any          `json:"positional,omitempty"`
}

type MutationOptions struct {
	ExpectedSchemaVersion  uint64               `json:"expected_schema_version,omitempty"`
	ExpectedRevision       uint64               `json:"expected_revision,omitempty"`
	SourceRevisions        map[string]uint64    `json:"source_revisions,omitempty"`
	MaxAffectedRows        uint64               `json:"max_affected_rows,omitempty"`
	Actor                  string               `json:"actor,omitempty"`
	Source                 string               `json:"source,omitempty"`
	Reason                 string               `json:"reason,omitempty"`
	SourceKind             history.SourceKind   `json:"source_kind,omitempty"`
	SourceReceiptID        string               `json:"source_receipt_id,omitempty"`
	SourceLocator          string               `json:"source_locator,omitempty"`
	SourceContentHash      string               `json:"source_content_hash,omitempty"`
	RouteLeafIDs           []string             `json:"route_leaf_ids,omitempty"`
	RoutePath              []router.PathSegment `json:"route_path,omitempty"`
	Links                  []row.LinkRef        `json:"links,omitempty"`
	TargetRouteLeafIDs     [][]string           `json:"target_route_leaf_ids,omitempty"`
	RelationTargetOrdinals map[string]int       `json:"relation_target_ordinals,omitempty"`
	RouteUpdates           []row.RouteUpdate    `json:"route_updates,omitempty"`
}

type Authorization = security.Authorization

func (options MutationOptions) MarshalJSON() ([]byte, error) {
	type wireOptions struct {
		ExpectedSchemaVersion  uint64               `json:"expected_schema_version,omitempty"`
		ExpectedRevision       uint64               `json:"expected_revision,omitempty"`
		SourceRevisions        map[string]uint64    `json:"source_revisions,omitempty"`
		MaxAffectedRows        uint64               `json:"max_affected_rows,omitempty"`
		Actor                  string               `json:"actor,omitempty"`
		Source                 string               `json:"source,omitempty"`
		Reason                 string               `json:"reason,omitempty"`
		SourceKind             history.SourceKind   `json:"source_kind,omitempty"`
		SourceReceiptID        string               `json:"source_receipt_id,omitempty"`
		SourceLocator          string               `json:"source_locator,omitempty"`
		SourceContentHash      string               `json:"source_content_hash,omitempty"`
		RouteLeafIDs           *[]string            `json:"route_leaf_ids,omitempty"`
		RoutePath              []router.PathSegment `json:"route_path,omitempty"`
		Links                  *[]row.LinkRef       `json:"links,omitempty"`
		TargetRouteLeafIDs     *[][]string          `json:"target_route_leaf_ids,omitempty"`
		RelationTargetOrdinals map[string]int       `json:"relation_target_ordinals,omitempty"`
		RouteUpdates           []row.RouteUpdate    `json:"route_updates,omitempty"`
	}
	wire := wireOptions{
		ExpectedSchemaVersion:  options.ExpectedSchemaVersion,
		ExpectedRevision:       options.ExpectedRevision,
		SourceRevisions:        options.SourceRevisions,
		MaxAffectedRows:        options.MaxAffectedRows,
		Actor:                  options.Actor,
		Source:                 options.Source,
		Reason:                 options.Reason,
		SourceKind:             options.SourceKind,
		SourceReceiptID:        options.SourceReceiptID,
		SourceLocator:          options.SourceLocator,
		SourceContentHash:      options.SourceContentHash,
		RelationTargetOrdinals: options.RelationTargetOrdinals,
		RouteUpdates:           options.RouteUpdates,
		RoutePath:              options.RoutePath,
	}
	if options.RouteLeafIDs != nil {
		wire.RouteLeafIDs = &options.RouteLeafIDs
	}
	if options.TargetRouteLeafIDs != nil {
		wire.TargetRouteLeafIDs = &options.TargetRouteLeafIDs
	}
	if options.Links != nil {
		wire.Links = &options.Links
	}
	return json.Marshal(wire)
}

type Output struct {
	Columns        []result.Column
	Rows           []result.Row
	AffectedRows   uint64
	Revision       *uint64
	CommitSequence *uint64
	Truncated      bool
	NextCursor     string
	Page           *result.ListPage
	RowDetail      *result.RowDetail
	Discovery      *discovery.Frame
	// Warnings are structured notices about this statement's own result, not
	// errors: they explain what the answer could not cover. They never change
	// Rows — a caller that ignores them still reads a valid result.
	Warnings []result.Notice
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string {
	return fmt.Sprintf("MSQL executor %s: %s", err.Code, err.Message)
}

func (err *Error) StableCode() string {
	return string(err.Code)
}

func New(dictionary Catalog, rows Rows) *Engine {
	engine := &Engine{catalog: dictionary, rows: rows}
	if service, ok := dictionary.(binder.CatalogService); ok {
		engine.catalogBinder = binder.NewCatalog(service)
	}
	return engine
}

func NewWithPointReads(dictionary Catalog, rows Rows, points PointReads) *Engine {
	engine := New(dictionary, rows)
	engine.points = points
	return engine
}

func executeError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func unsupported(statement ast.Statement) error {
	return executeError(result.CodeUnsupported, fmt.Sprintf("%s is not supported by this executor path", statement.Kind))
}
