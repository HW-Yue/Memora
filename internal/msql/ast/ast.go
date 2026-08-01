package ast

import "github.com/HW-Yue/Memora/internal/msql/lexer"

const Version = "memora.msql.ast/v1"

type Document struct {
	Version   string    `json:"version"`
	Statement Statement `json:"statement"`
}

type Batch struct {
	Version    string      `json:"version"`
	Statements []Statement `json:"statements"`
}

type Statement struct {
	Kind          string                       `json:"kind"`
	Span          lexer.Span                   `json:"-"`
	Show          *ShowStatement               `json:"show,omitempty"`
	Describe      *DescribeStatement           `json:"describe,omitempty"`
	Create        *CreateStatement             `json:"create,omitempty"`
	Alter         *AlterStatement              `json:"alter,omitempty"`
	Select        *SelectStatement             `json:"select,omitempty"`
	Insert        *InsertStatement             `json:"insert,omitempty"`
	Update        *UpdateStatement             `json:"update,omitempty"`
	Delete        *DeleteStatement             `json:"delete,omitempty"`
	Restore       *RestoreStatement            `json:"restore,omitempty"`
	Reshape       *ReshapeStatement            `json:"reshape,omitempty"`
	Relate        *RelateStatement             `json:"relate,omitempty"`
	Unrelate      *UnrelateStatement           `json:"unrelate,omitempty"`
	CreateRoute   *CreateRouteStatement        `json:"create_route,omitempty"`
	RenameRoute   *RenameRouteStatement        `json:"rename_route,omitempty"`
	UpdateRoute   *UpdateRouteStatement        `json:"update_route,omitempty"`
	DeleteRoute   *DeleteRouteStatement        `json:"delete_route,omitempty"`
	OpenRoute     *OpenRouteStatement          `json:"open_route,omitempty"`
	PlanRoute     *PlanRouteMutationStatement  `json:"plan_route_mutation,omitempty"`
	PlanSchema    *PlanSchemaChangeStatement   `json:"plan_schema_change,omitempty"`
	ApplyRoute    *ApplyRouteMutationStatement `json:"apply_route_mutation,omitempty"`
	Configuration *ConfigurationStatement      `json:"configuration,omitempty"`
	Package       *PackageStatement            `json:"package,omitempty"`
	Export        *ExportStatement             `json:"export,omitempty"`
	Transaction   *TransactionStatement        `json:"transaction,omitempty"`
}

type Identifier struct {
	Value  string     `json:"value"`
	Quoted bool       `json:"quoted,omitempty"`
	Span   lexer.Span `json:"-"`
}

type Name struct {
	Parts []Identifier `json:"parts"`
	Span  lexer.Span   `json:"-"`
}

type ShowStatement struct {
	Object    string      `json:"object"`
	Database  *Name       `json:"database,omitempty"`
	Table     *Name       `json:"table,omitempty"`
	Row       *Expression `json:"row,omitempty"`
	Limit     *Expression `json:"limit,omitempty"`
	Direction string      `json:"direction,omitempty"`
	Compact   bool        `json:"compact,omitempty"`
	Route     *Expression `json:"route,omitempty"`
	Cursor    *Expression `json:"cursor,omitempty"`
	After     *Expression `json:"after,omitempty"`
	Change    *Expression `json:"change,omitempty"`
	Trace     *Expression `json:"trace,omitempty"`
	RouteMode string      `json:"route_mode,omitempty"`
	Predictor string      `json:"predictor,omitempty"`
	Query     *Expression `json:"query,omitempty"`
	Space     *Expression `json:"space,omitempty"`
	ByteLimit *Expression `json:"byte_limit,omitempty"`
}

type DescribeStatement struct {
	Object  string      `json:"object"`
	Name    Name        `json:"name"`
	Compact bool        `json:"compact,omitempty"`
	Route   *Expression `json:"route,omitempty"`
}

type CreateStatement struct {
	Object       string             `json:"object"`
	Name         Name               `json:"name"`
	Purpose      string             `json:"purpose,omitempty"`
	Scope        string             `json:"scope,omitempty"`
	AntiScope    string             `json:"anti_scope,omitempty"`
	RowSemantics string             `json:"row_semantics,omitempty"`
	Columns      []ColumnDefinition `json:"columns,omitempty"`
}

type ColumnDefinition struct {
	Name         Identifier `json:"name"`
	Type         TypeRef    `json:"type"`
	NotNull      bool       `json:"not_null,omitempty"`
	Purpose      string     `json:"purpose,omitempty"`
	SemanticRole string     `json:"semantic_role,omitempty"`
}

type AlterStatement struct {
	Object     string            `json:"object"`
	Name       Name              `json:"name"`
	Action     string            `json:"action"`
	ColumnName *Identifier       `json:"column_name,omitempty"`
	NewName    *Identifier       `json:"new_name,omitempty"`
	Column     *ColumnDefinition `json:"column,omitempty"`
}

type TypeRef struct {
	Name      Identifier `json:"name"`
	Arguments []int64    `json:"arguments,omitempty"`
}

type SelectStatement struct {
	Projections []Expression `json:"projections"`
	From        Name         `json:"from"`
	AsOf        *AsOfClause  `json:"as_of,omitempty"`
	Where       *Expression  `json:"where,omitempty"`
	Limit       *Expression  `json:"limit,omitempty"`
}

type AsOfClause struct {
	Kind  string     `json:"kind"`
	Value Expression `json:"value"`
}

type InsertStatement struct {
	Table   Name           `json:"table"`
	Columns []Identifier   `json:"columns,omitempty"`
	Values  [][]Expression `json:"values"`
}

type UpdateStatement struct {
	Table       Name         `json:"table"`
	Assignments []Assignment `json:"assignments"`
	Where       *Expression  `json:"where,omitempty"`
}

type Assignment struct {
	Column Identifier `json:"column"`
	Value  Expression `json:"value"`
}

type DeleteStatement struct {
	Table Name        `json:"table"`
	Where *Expression `json:"where,omitempty"`
}

type RestoreStatement struct {
	Table    Name        `json:"table"`
	Row      *Expression `json:"row"`
	Revision *Expression `json:"revision"`
}

type ReshapeStatement struct {
	Mode    string         `json:"mode"`
	Table   Name           `json:"table"`
	Sources []Expression   `json:"sources"`
	Columns []Identifier   `json:"columns"`
	Values  [][]Expression `json:"values"`
}

type RelateStatement struct {
	SourceTable Name        `json:"source_table"`
	SourceRow   *Expression `json:"source_row"`
	TargetTable Name        `json:"target_table"`
	TargetRow   *Expression `json:"target_row"`
	Type        *Expression `json:"relation_type"`
	Description *Expression `json:"description,omitempty"`
}

type UnrelateStatement struct {
	Relation *Expression `json:"relation"`
}

type CreateRouteStatement struct {
	Mode     string      `json:"mode"`
	Table    *Name       `json:"table,omitempty"`
	Parent   *Expression `json:"parent,omitempty"`
	Name     *Expression `json:"name,omitempty"`
	NodeKind *Expression `json:"node_kind,omitempty"`
	Purpose  *Expression `json:"purpose"`
	Synopsis *Expression `json:"synopsis,omitempty"`
}

type RenameRouteStatement struct {
	Route *Expression `json:"route"`
	Name  *Expression `json:"name"`
}

type UpdateRouteStatement struct {
	Route    *Expression `json:"route"`
	Synopsis *Expression `json:"synopsis"`
}

type DeleteRouteStatement struct {
	Route *Expression `json:"route"`
}

type OpenRouteStatement struct {
	Mode   string      `json:"mode"`
	Route  *Expression `json:"route"`
	Cursor *Expression `json:"cursor,omitempty"`
	Limit  *Expression `json:"limit"`
}

type PlanRouteMutationStatement struct {
	Table    Name        `json:"table"`
	Proposal *Expression `json:"proposal"`
}

type ApplyRouteMutationStatement struct {
	Table Name        `json:"table"`
	Plan  *Expression `json:"plan"`
}

type PlanSchemaChangeStatement struct {
	Table    Name        `json:"table"`
	Proposal *Expression `json:"proposal"`
}

type ConfigurationStatement struct {
	Action          string      `json:"action"`
	Key             string      `json:"key"`
	RouteChildren   *Expression `json:"route_children,omitempty"`
	OpenLocators    *Expression `json:"open_locators,omitempty"`
	SelectScan      *Expression `json:"select_scan,omitempty"`
	SelectRows      *Expression `json:"select_rows,omitempty"`
	RouteFrameNodes *Expression `json:"route_frame_nodes,omitempty"`
	TargetRevision  *Expression `json:"target_revision,omitempty"`
}

type PackageStatement struct {
	Action   string      `json:"action"`
	Database Name        `json:"database,omitempty"`
	Author   *Expression `json:"author,omitempty"`
	Value    *Expression `json:"value,omitempty"`
	ReadOnly bool        `json:"read_only,omitempty"`
	Trusted  bool        `json:"trusted,omitempty"`
}

type ExportStatement struct {
	Format  string      `json:"format"`
	Path    *Expression `json:"path"`
	Profile *Expression `json:"profile"`
}

type TransactionStatement struct {
	Action string `json:"action"`
}

type Expression struct {
	Kind      string      `json:"kind"`
	Span      lexer.Span  `json:"-"`
	Name      *Name       `json:"name,omitempty"`
	Literal   *Literal    `json:"literal,omitempty"`
	Parameter *Parameter  `json:"parameter,omitempty"`
	Operator  string      `json:"operator,omitempty"`
	Left      *Expression `json:"left,omitempty"`
	Right     *Expression `json:"right,omitempty"`
	Operand   *Expression `json:"operand,omitempty"`
}

type Literal struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
}

type Parameter struct {
	Style   string `json:"style"`
	Name    string `json:"name,omitempty"`
	Ordinal int    `json:"ordinal"`
}

func (document Document) Parameters() []Parameter {
	parameters := make([]Parameter, 0)
	appendExpression := parameterCollector(&parameters)
	statement := document.Statement
	switch {
	case statement.Select != nil:
		for index := range statement.Select.Projections {
			appendExpression(&statement.Select.Projections[index])
		}
		if statement.Select.AsOf != nil {
			appendExpression(&statement.Select.AsOf.Value)
		}
		appendExpression(statement.Select.Where)
		appendExpression(statement.Select.Limit)
	case statement.Describe != nil && statement.Describe.Object == "ROUTE":
		appendExpression(statement.Describe.Route)
	case statement.Show != nil && (statement.Show.Object == "DATABASES" ||
		statement.Show.Object == "TABLES" || statement.Show.Object == "COLUMNS"):
		appendExpression(statement.Show.Cursor)
		appendExpression(statement.Show.Limit)
	case statement.Show != nil && statement.Show.Object == "CONFIGURATION":
		appendExpression(statement.Show.Limit)
	case statement.Show != nil && statement.Show.Object == "HISTORY":
		appendExpression(statement.Show.Row)
		appendExpression(statement.Show.Cursor)
		appendExpression(statement.Show.Limit)
	case statement.Show != nil && (statement.Show.Object == "CHANGES" || statement.Show.Object == "CHANGE"):
		appendExpression(statement.Show.Change)
		appendExpression(statement.Show.After)
		appendExpression(statement.Show.Cursor)
		appendExpression(statement.Show.Limit)
	case statement.Show != nil && (statement.Show.Object == "ROUTE_TRACES" || statement.Show.Object == "ROUTE_TRACE"):
		appendExpression(statement.Show.Trace)
		appendExpression(statement.Show.After)
		appendExpression(statement.Show.Cursor)
		appendExpression(statement.Show.Limit)
	case statement.Show != nil && statement.Show.Object == "ROUTE_CANDIDATES":
		appendExpression(statement.Show.Query)
		appendExpression(statement.Show.Space)
		appendExpression(statement.Show.Limit)
		appendExpression(statement.Show.ByteLimit)
	case statement.Show != nil && statement.Show.Object == "RELATIONS":
		appendExpression(statement.Show.Row)
		appendExpression(statement.Show.Limit)
	case statement.Insert != nil:
		for row := range statement.Insert.Values {
			for column := range statement.Insert.Values[row] {
				appendExpression(&statement.Insert.Values[row][column])
			}
		}
	case statement.Update != nil:
		for index := range statement.Update.Assignments {
			appendExpression(&statement.Update.Assignments[index].Value)
		}
		appendExpression(statement.Update.Where)
	case statement.Delete != nil:
		appendExpression(statement.Delete.Where)
	case statement.Restore != nil:
		appendExpression(statement.Restore.Row)
		appendExpression(statement.Restore.Revision)
	case statement.Reshape != nil:
		for index := range statement.Reshape.Sources {
			appendExpression(&statement.Reshape.Sources[index])
		}
		for row := range statement.Reshape.Values {
			for column := range statement.Reshape.Values[row] {
				appendExpression(&statement.Reshape.Values[row][column])
			}
		}
	case statement.Relate != nil:
		appendExpression(statement.Relate.SourceRow)
		appendExpression(statement.Relate.TargetRow)
		appendExpression(statement.Relate.Type)
		appendExpression(statement.Relate.Description)
	case statement.Unrelate != nil:
		appendExpression(statement.Unrelate.Relation)
	case statement.CreateRoute != nil:
		appendExpression(statement.CreateRoute.Parent)
		appendExpression(statement.CreateRoute.Name)
		appendExpression(statement.CreateRoute.NodeKind)
		appendExpression(statement.CreateRoute.Purpose)
		appendExpression(statement.CreateRoute.Synopsis)
	case statement.RenameRoute != nil:
		appendExpression(statement.RenameRoute.Route)
		appendExpression(statement.RenameRoute.Name)
	case statement.UpdateRoute != nil:
		appendExpression(statement.UpdateRoute.Route)
		appendExpression(statement.UpdateRoute.Synopsis)
	case statement.DeleteRoute != nil:
		appendExpression(statement.DeleteRoute.Route)
	case statement.Show != nil && statement.Show.Object == "ROUTES":
		appendExpression(statement.Show.Route)
		appendExpression(statement.Show.Cursor)
		appendExpression(statement.Show.Limit)
	case statement.OpenRoute != nil:
		appendExpression(statement.OpenRoute.Route)
		appendExpression(statement.OpenRoute.Cursor)
		appendExpression(statement.OpenRoute.Limit)
	case statement.PlanRoute != nil:
		appendExpression(statement.PlanRoute.Proposal)
	case statement.PlanSchema != nil:
		appendExpression(statement.PlanSchema.Proposal)
	case statement.ApplyRoute != nil:
		appendExpression(statement.ApplyRoute.Plan)
	case statement.Configuration != nil:
		appendExpression(statement.Configuration.RouteChildren)
		appendExpression(statement.Configuration.OpenLocators)
		appendExpression(statement.Configuration.SelectScan)
		appendExpression(statement.Configuration.SelectRows)
		appendExpression(statement.Configuration.RouteFrameNodes)
		appendExpression(statement.Configuration.TargetRevision)
	case statement.Package != nil:
		appendExpression(statement.Package.Author)
		appendExpression(statement.Package.Value)
	case statement.Export != nil:
		appendExpression(statement.Export.Path)
		appendExpression(statement.Export.Profile)
	}
	return parameters
}

func parameterCollector(parameters *[]Parameter) func(*Expression) {
	var collect func(*Expression)
	collect = func(expression *Expression) {
		if expression == nil {
			return
		}
		if expression.Parameter != nil {
			*parameters = append(*parameters, *expression.Parameter)
		}
		collect(expression.Left)
		collect(expression.Right)
		collect(expression.Operand)
	}
	return collect
}
