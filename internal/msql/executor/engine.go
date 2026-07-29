package executor

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/binder"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/search"
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
	Match(context.Context, string, string, string, []string, int) (search.Result, error)
	CreateRouterRoot(context.Context, string, string) (router.Node, error)
	CreateRouterNode(context.Context, string, router.NodeDefinition) (router.Node, error)
	RenameRouterNode(context.Context, string, string, uint64) (router.Node, error)
	DeleteRouterNode(context.Context, string, uint64) (uint64, error)
	GetRouterNode(context.Context, string) (router.Node, error)
	ResolveRouterPath(context.Context, string, string) (router.Node, error)
	ListRouterChildren(context.Context, string, string, int) ([]router.Node, string, error)
	ListRouterLeaf(context.Context, string, int) ([]router.Locator, bool, error)
}

type Engine struct {
	catalog       Catalog
	catalogBinder *binder.Catalog
	rows          Rows
}

type Parameters struct {
	Named      map[string]any `json:"named,omitempty"`
	Positional []any          `json:"positional,omitempty"`
}

type MutationOptions struct {
	ExpectedSchemaVersion uint64   `json:"expected_schema_version,omitempty"`
	ExpectedRevision      uint64   `json:"expected_revision,omitempty"`
	MaxAffectedRows       uint64   `json:"max_affected_rows,omitempty"`
	Actor                 string   `json:"actor,omitempty"`
	Source                string   `json:"source,omitempty"`
	Reason                string   `json:"reason,omitempty"`
	IndexTerms            []string `json:"index_terms,omitempty"`
	RouteLeafIDs          []string `json:"route_leaf_ids,omitempty"`
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

func executeError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func unsupported(statement ast.Statement) error {
	return executeError(result.CodeUnsupported, fmt.Sprintf("%s is not supported by this executor path", statement.Kind))
}
