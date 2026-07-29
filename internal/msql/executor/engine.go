package executor

import (
	"context"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

const maxQueryScan = 1000

type Catalog interface {
	DescribeTable(context.Context, string, string) (catalog.Table, error)
}

type Rows interface {
	ListPage(context.Context, string, string, int) ([]row.Row, bool, error)
}

type Engine struct {
	catalog Catalog
	rows    Rows
}

type Parameters struct {
	Named      map[string]any
	Positional []any
}

type Output struct {
	Columns      []result.Column
	Rows         []result.Row
	AffectedRows uint64
	Revision     *uint64
	Truncated    bool
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
	return &Engine{catalog: dictionary, rows: rows}
}

func executeError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func unsupported(statement ast.Statement) error {
	return executeError(result.CodeUnsupported, fmt.Sprintf("%s is not supported by this executor path", statement.Kind))
}
