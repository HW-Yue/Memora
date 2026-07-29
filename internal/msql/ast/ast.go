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
	Kind        string                `json:"kind"`
	Span        lexer.Span            `json:"-"`
	Show        *ShowStatement        `json:"show,omitempty"`
	Describe    *DescribeStatement    `json:"describe,omitempty"`
	Create      *CreateStatement      `json:"create,omitempty"`
	Alter       *AlterStatement       `json:"alter,omitempty"`
	Select      *SelectStatement      `json:"select,omitempty"`
	Insert      *InsertStatement      `json:"insert,omitempty"`
	Update      *UpdateStatement      `json:"update,omitempty"`
	Delete      *DeleteStatement      `json:"delete,omitempty"`
	Restore     *RestoreStatement     `json:"restore,omitempty"`
	Relate      *RelateStatement      `json:"relate,omitempty"`
	Unrelate    *UnrelateStatement    `json:"unrelate,omitempty"`
	Transaction *TransactionStatement `json:"transaction,omitempty"`
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
}

type DescribeStatement struct {
	Object  string `json:"object"`
	Name    Name   `json:"name"`
	Compact bool   `json:"compact,omitempty"`
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
	Name    Identifier `json:"name"`
	Type    TypeRef    `json:"type"`
	NotNull bool       `json:"not_null,omitempty"`
	Purpose string     `json:"purpose,omitempty"`
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
	case statement.Show != nil && statement.Show.Object == "HISTORY":
		appendExpression(statement.Show.Row)
		appendExpression(statement.Show.Limit)
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
	case statement.Relate != nil:
		appendExpression(statement.Relate.SourceRow)
		appendExpression(statement.Relate.TargetRow)
		appendExpression(statement.Relate.Type)
		appendExpression(statement.Relate.Description)
	case statement.Unrelate != nil:
		appendExpression(statement.Unrelate.Relation)
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
