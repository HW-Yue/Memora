package executor

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/wikiexport"
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
	HistoryPage(context.Context, string, string, string, int) ([]history.Record, bool, error)
	Restore(context.Context, string, string, string, uint64, row.WriteOptions) (row.Row, error)
	Relate(context.Context, row.RelationDefinition) (relation.Relation, error)
	GetRelation(context.Context, string) (relation.Relation, error)
	DeleteRelation(context.Context, string, uint64) (relation.Relation, error)
	ListOutgoingRelations(context.Context, row.RelationEndpoint) ([]relation.Relation, error)
	ListIncomingRelations(context.Context, row.RelationEndpoint) ([]relation.Relation, error)
	CreateRouterNode(context.Context, string, router.NodeDefinition) (router.Node, error)
	RenameRouterNode(context.Context, string, string, uint64) (router.Node, error)
	DeleteRouterNode(context.Context, string, uint64) (uint64, error)
	GetRouterNode(context.Context, string) (router.Node, error)
	ListRouterChildren(context.Context, string, string, int) ([]router.Node, string, error)
	ListRouterLeaf(context.Context, string, int) ([]router.Locator, bool, error)
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
	packages      PackageManager
	wiki          WikiExporter
}

type PackageManager interface {
	Pack(context.Context, string, string) ([]byte, dbpackage.Manifest, error)
	Open([]byte) (dbpackage.Opened, error)
	Install(context.Context, []byte, dbpackage.InstallOptions) (dbpackage.InstallReceipt, error)
}

type WikiExporter interface {
	Export(context.Context, string, wikiexport.Profile, []string) (wikiexport.Manifest, error)
}

type Parameters struct {
	Named      map[string]any `json:"named,omitempty"`
	Positional []any          `json:"positional,omitempty"`
}

type MutationOptions struct {
	ExpectedSchemaVersion  uint64             `json:"expected_schema_version,omitempty"`
	ExpectedRevision       uint64             `json:"expected_revision,omitempty"`
	SourceRevisions        map[string]uint64  `json:"source_revisions,omitempty"`
	MaxAffectedRows        uint64             `json:"max_affected_rows,omitempty"`
	Actor                  string             `json:"actor,omitempty"`
	Source                 string             `json:"source,omitempty"`
	Reason                 string             `json:"reason,omitempty"`
	SourceKind             history.SourceKind `json:"source_kind,omitempty"`
	SourceReceiptID        string             `json:"source_receipt_id,omitempty"`
	SourceLocator          string             `json:"source_locator,omitempty"`
	SourceContentHash      string             `json:"source_content_hash,omitempty"`
	RouteLeafIDs           []string           `json:"route_leaf_ids,omitempty"`
	TargetRouteLeafIDs     [][]string         `json:"target_route_leaf_ids,omitempty"`
	RelationTargetOrdinals map[string]int     `json:"relation_target_ordinals,omitempty"`
	RouteUpdates           []row.RouteUpdate  `json:"route_updates,omitempty"`
}

type Authorization = security.Authorization

func (options MutationOptions) MarshalJSON() ([]byte, error) {
	type wireOptions struct {
		ExpectedSchemaVersion  uint64             `json:"expected_schema_version,omitempty"`
		ExpectedRevision       uint64             `json:"expected_revision,omitempty"`
		SourceRevisions        map[string]uint64  `json:"source_revisions,omitempty"`
		MaxAffectedRows        uint64             `json:"max_affected_rows,omitempty"`
		Actor                  string             `json:"actor,omitempty"`
		Source                 string             `json:"source,omitempty"`
		Reason                 string             `json:"reason,omitempty"`
		SourceKind             history.SourceKind `json:"source_kind,omitempty"`
		SourceReceiptID        string             `json:"source_receipt_id,omitempty"`
		SourceLocator          string             `json:"source_locator,omitempty"`
		SourceContentHash      string             `json:"source_content_hash,omitempty"`
		RouteLeafIDs           *[]string          `json:"route_leaf_ids,omitempty"`
		TargetRouteLeafIDs     *[][]string        `json:"target_route_leaf_ids,omitempty"`
		RelationTargetOrdinals map[string]int     `json:"relation_target_ordinals,omitempty"`
		RouteUpdates           []row.RouteUpdate  `json:"route_updates,omitempty"`
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
	}
	if options.RouteLeafIDs != nil {
		wire.RouteLeafIDs = &options.RouteLeafIDs
	}
	if options.TargetRouteLeafIDs != nil {
		wire.TargetRouteLeafIDs = &options.TargetRouteLeafIDs
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

func NewWithPackages(dictionary Catalog, rows Rows, packages PackageManager) *Engine {
	return NewWithManagement(dictionary, rows, packages, nil)
}

func NewWithManagement(dictionary Catalog, rows Rows, packages PackageManager, wiki WikiExporter) *Engine {
	engine := New(dictionary, rows)
	engine.packages, engine.wiki = packages, wiki
	return engine
}

func executeError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func unsupported(statement ast.Statement) error {
	return executeError(result.CodeUnsupported, fmt.Sprintf("%s is not supported by this executor path", statement.Kind))
}
