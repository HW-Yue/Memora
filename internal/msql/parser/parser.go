package parser

import (
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/lexer"
)

type parser struct {
	tokens           []lexer.Token
	current          int
	parameterOrdinal int
}

func Parse(source string) (*ast.Document, error) {
	tokens, err := lexer.Lex(source)
	if err != nil {
		return nil, err
	}
	parser := parser{tokens: tokens}
	statement, err := parser.parseStatement()
	if err != nil {
		return nil, err
	}
	if parser.matchKind(lexer.KindSemicolon) {
		// F12 will turn the remaining token stream into a statement list.
	}
	if !parser.checkKind(lexer.KindEOF) {
		return nil, parser.unexpected("end of statement")
	}
	return &ast.Document{Version: ast.Version, Statement: statement}, nil
}

func ParseBatch(source string) (*ast.Batch, error) {
	tokens, err := lexer.Lex(source)
	if err != nil {
		return nil, err
	}
	parser := parser{tokens: tokens}
	batch := &ast.Batch{Version: ast.Version}
	for {
		for parser.matchKind(lexer.KindSemicolon) {
		}
		if parser.checkKind(lexer.KindEOF) {
			break
		}
		statementIndex := len(batch.Statements)
		statement, err := parser.parseStatement()
		if err != nil {
			return nil, withStatementIndex(err, statementIndex)
		}
		batch.Statements = append(batch.Statements, statement)
		if parser.checkKind(lexer.KindEOF) {
			break
		}
		if !parser.matchKind(lexer.KindSemicolon) {
			return nil, withStatementIndex(parser.unexpected("; or end of request"), statementIndex)
		}
	}
	if len(batch.Statements) == 0 {
		token := parser.peek()
		return nil, &Error{Code: ErrorEmptyBatch, Span: token.Span, Expected: "at least one statement", Found: "EOF", StatementIndex: -1}
	}
	return batch, nil
}

func (parser *parser) parseStatement() (ast.Statement, error) {
	start := parser.peek().Span.Start
	var statement ast.Statement
	var err error
	switch {
	case parser.matchWord("SHOW"):
		statement, err = parser.parseShow()
	case parser.matchWord("DESCRIBE"):
		statement, err = parser.parseDescribe()
	case parser.matchWord("CREATE"):
		statement, err = parser.parseCreate()
	case parser.matchWord("ALTER"):
		statement, err = parser.parseAlter()
	case parser.matchWord("SELECT"):
		statement, err = parser.parseSelect()
	case parser.matchWord("INSERT"):
		statement, err = parser.parseInsert()
	case parser.matchWord("UPDATE"):
		statement, err = parser.parseUpdate()
	case parser.matchWord("DELETE"):
		statement, err = parser.parseDelete()
	case parser.matchWord("RESTORE"):
		statement, err = parser.parseRestore()
	case parser.matchWord("RELATE"):
		statement, err = parser.parseRelate()
	case parser.matchWord("UNRELATE"):
		statement, err = parser.parseUnrelate()
	case parser.matchWord("MATCH"):
		statement, err = parser.parseMatch()
	case parser.matchWord("PACK"):
		statement, err = parser.parsePackDatabase()
	case parser.matchWord("EXPORT"):
		statement, err = parser.parseExportWiki()
	case parser.matchWord("OPEN"):
		if parser.checkWord("PACKAGE") {
			statement, err = parser.parseOpenPackage()
		} else {
			statement, err = parser.parseOpenRoute()
		}
	case parser.matchWord("INSTALL"):
		statement, err = parser.parseInstallPackage()
	case parser.matchWord("BEGIN"):
		statement = transactionStatement("BEGIN")
	case parser.matchWord("START"):
		if _, err = parser.expectWord("TRANSACTION"); err == nil {
			statement = transactionStatement("BEGIN")
		}
	case parser.matchWord("COMMIT"):
		statement = transactionStatement("COMMIT")
	case parser.matchWord("ROLLBACK"):
		statement = transactionStatement("ROLLBACK")
	case parser.checkKind(lexer.KindEOF):
		return ast.Statement{}, parser.unexpected("statement")
	default:
		token := parser.peek()
		return ast.Statement{}, &Error{Code: ErrorUnsupportedStatement, Span: token.Span, Found: tokenDescription(token), StatementIndex: -1}
	}
	if err != nil {
		return ast.Statement{}, err
	}
	statement.Span = lexer.Span{Start: start, End: parser.previous().Span.End}
	return statement, nil
}

func transactionStatement(action string) ast.Statement {
	return ast.Statement{Kind: action, Transaction: &ast.TransactionStatement{Action: action}}
}

func (parser *parser) parseShow() (ast.Statement, error) {
	show := &ast.ShowStatement{}
	switch {
	case parser.matchWord("INSTANCE"):
		show.Object = "INSTANCE"
	case parser.matchWord("DATABASES"):
		show.Object = "DATABASES"
	case parser.matchWord("TABLES"):
		show.Object = "TABLES"
		if parser.matchWord("FROM") {
			name, err := parser.parseName()
			if err != nil {
				return ast.Statement{}, err
			}
			show.Database = &name
		}
	case parser.matchWord("COLUMNS"):
		show.Object = "COLUMNS"
		if _, err := parser.expectWord("FROM"); err != nil {
			return ast.Statement{}, err
		}
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		show.Table = &name
	case parser.matchWord("HISTORY"):
		show.Object = "HISTORY"
		if _, err := parser.expectWord("FROM"); err != nil {
			return ast.Statement{}, err
		}
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		show.Table = &name
		if _, err := parser.expectWord("FOR"); err != nil {
			return ast.Statement{}, err
		}
		if _, err := parser.expectWord("ROW"); err != nil {
			return ast.Statement{}, err
		}
		row, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Row = &row
		if _, err := parser.expectWord("LIMIT"); err != nil {
			return ast.Statement{}, err
		}
		limit, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Limit = &limit
	case parser.matchWord("RELATIONS"):
		show.Object = "RELATIONS"
		if _, err := parser.expectWord("FROM"); err != nil {
			return ast.Statement{}, err
		}
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		show.Table = &name
		if _, err := parser.expectWord("FOR"); err != nil {
			return ast.Statement{}, err
		}
		if _, err := parser.expectWord("ROW"); err != nil {
			return ast.Statement{}, err
		}
		row, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Row = &row
		if _, err := parser.expectWord("DIRECTION"); err != nil {
			return ast.Statement{}, err
		}
		switch {
		case parser.matchWord("OUTGOING"):
			show.Direction = "OUTGOING"
		case parser.matchWord("INCOMING"):
			show.Direction = "INCOMING"
		default:
			return ast.Statement{}, parser.unexpected("OUTGOING or INCOMING")
		}
		if _, err := parser.expectWord("LIMIT"); err != nil {
			return ast.Statement{}, err
		}
		limit, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Limit = &limit
	case parser.matchWord("ROUTES"):
		show.Object = "ROUTES"
		switch {
		case parser.matchWord("UNDER"):
			show.RouteMode = "ID"
			route, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Route = &route
		case parser.matchWord("FROM"):
			if parser.matchWord("TABLE") {
				show.RouteMode = "TABLE_ROOT"
				table, err := parser.parseName()
				if err != nil {
					return ast.Statement{}, err
				}
				show.Table = &table
				if _, err := parser.expectWord("AT"); err != nil {
					return ast.Statement{}, err
				}
				if _, err := parser.expectWord("ROOT"); err != nil {
					return ast.Statement{}, err
				}
			} else {
				show.RouteMode = "PATH"
				if _, err := parser.expectWord("DATABASE"); err != nil {
					return ast.Statement{}, err
				}
				database, err := parser.parseExpression(1)
				if err != nil {
					return ast.Statement{}, err
				}
				show.RouteDB = &database
				if _, err := parser.expectWord("AT"); err != nil {
					return ast.Statement{}, err
				}
				path, err := parser.parseExpression(1)
				if err != nil {
					return ast.Statement{}, err
				}
				show.Route = &path
			}
		default:
			return ast.Statement{}, parser.unexpected("UNDER or FROM DATABASE")
		}
		if parser.matchWord("CURSOR") {
			cursor, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Cursor = &cursor
		}
		if _, err := parser.expectWord("LIMIT"); err != nil {
			return ast.Statement{}, err
		}
		limit, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Limit = &limit
	default:
		return ast.Statement{}, parser.unexpected("INSTANCE, DATABASES, TABLES, COLUMNS, HISTORY, RELATIONS, or ROUTES")
	}
	show.Compact = parser.matchWord("COMPACT")
	return ast.Statement{Kind: "SHOW", Show: show}, nil
}

func (parser *parser) parseDescribe() (ast.Statement, error) {
	describe := &ast.DescribeStatement{}
	switch {
	case parser.matchWord("DATABASE"):
		describe.Object = "DATABASE"
	case parser.matchWord("TABLE"):
		describe.Object = "TABLE"
	case parser.matchWord("COLUMN"):
		describe.Object = "COLUMN"
	default:
		return ast.Statement{}, parser.unexpected("DATABASE, TABLE, or COLUMN")
	}
	name, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	describe.Name = name
	describe.Compact = parser.matchWord("COMPACT")
	return ast.Statement{Kind: "DESCRIBE", Describe: describe}, nil
}

func (parser *parser) parseCreate() (ast.Statement, error) {
	if parser.matchWord("ROUTE") {
		return parser.parseCreateRoute()
	}
	create := &ast.CreateStatement{}
	switch {
	case parser.matchWord("DATABASE"):
		create.Object = "DATABASE"
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		create.Name = name
		metadata, err := parser.parseCatalogMetadata(false)
		if err != nil {
			return ast.Statement{}, err
		}
		applyMetadata(create, metadata)
	case parser.matchWord("TABLE"):
		create.Object = "TABLE"
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		create.Name = name
		metadata, err := parser.parseCatalogMetadata(true)
		if err != nil {
			return ast.Statement{}, err
		}
		applyMetadata(create, metadata)
		if _, err := parser.expectKind(lexer.KindLeftParen, "("); err != nil {
			return ast.Statement{}, err
		}
		for {
			column, err := parser.parseColumnDefinition()
			if err != nil {
				return ast.Statement{}, err
			}
			create.Columns = append(create.Columns, column)
			if !parser.matchKind(lexer.KindComma) {
				break
			}
		}
		if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
			return ast.Statement{}, err
		}
	default:
		return ast.Statement{}, parser.unexpected("DATABASE or TABLE")
	}
	return ast.Statement{Kind: "CREATE", Create: create}, nil
}

type catalogMetadata struct {
	purpose      string
	scope        string
	antiScope    string
	rowSemantics string
}

func (parser *parser) parseCatalogMetadata(table bool) (catalogMetadata, error) {
	var metadata catalogMetadata
	seen := make(map[string]bool)
	for {
		var option string
		switch {
		case parser.checkWord("PURPOSE"):
			option = "PURPOSE"
		case parser.checkWord("SCOPE"):
			option = "SCOPE"
		case parser.checkWord("ANTI"):
			option = "ANTI_SCOPE"
		case table && parser.checkWord("ROW"):
			option = "ROW_SEMANTICS"
		default:
			return metadata, nil
		}
		optionToken := parser.advance()
		if seen[option] {
			return catalogMetadata{}, parser.errorAt(optionToken, option+" only once")
		}
		seen[option] = true
		if option == "ANTI_SCOPE" {
			if _, err := parser.expectWord("SCOPE"); err != nil {
				return catalogMetadata{}, err
			}
		}
		if option == "ROW_SEMANTICS" {
			if _, err := parser.expectWord("SEMANTICS"); err != nil {
				return catalogMetadata{}, err
			}
		}
		value, err := parser.expectKind(lexer.KindString, "string literal")
		if err != nil {
			return catalogMetadata{}, err
		}
		switch option {
		case "PURPOSE":
			metadata.purpose = value.Value
		case "SCOPE":
			metadata.scope = value.Value
		case "ANTI_SCOPE":
			metadata.antiScope = value.Value
		case "ROW_SEMANTICS":
			metadata.rowSemantics = value.Value
		}
	}
}

func applyMetadata(create *ast.CreateStatement, metadata catalogMetadata) {
	create.Purpose = metadata.purpose
	create.Scope = metadata.scope
	create.AntiScope = metadata.antiScope
	create.RowSemantics = metadata.rowSemantics
}

func (parser *parser) parseColumnDefinition() (ast.ColumnDefinition, error) {
	name, err := parser.parseIdentifier()
	if err != nil {
		return ast.ColumnDefinition{}, err
	}
	typeName, err := parser.parseIdentifier()
	if err != nil {
		return ast.ColumnDefinition{}, err
	}
	column := ast.ColumnDefinition{Name: name, Type: ast.TypeRef{Name: typeName}}
	if parser.matchKind(lexer.KindLeftParen) {
		for {
			token, err := parser.expectKind(lexer.KindNumber, "integer type argument")
			if err != nil {
				return ast.ColumnDefinition{}, err
			}
			argument, err := strconv.ParseInt(token.Value, 10, 64)
			if err != nil || strings.ContainsAny(token.Value, ".eE") {
				return ast.ColumnDefinition{}, parser.errorAt(token, "integer type argument")
			}
			column.Type.Arguments = append(column.Type.Arguments, argument)
			if !parser.matchKind(lexer.KindComma) {
				break
			}
		}
		if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
			return ast.ColumnDefinition{}, err
		}
	}
	if parser.matchWord("NOT") {
		if _, err := parser.expectWord("NULL"); err != nil {
			return ast.ColumnDefinition{}, err
		}
		column.NotNull = true
	} else {
		parser.matchWord("NULL")
	}
	if parser.matchWord("PURPOSE") {
		purpose, err := parser.expectKind(lexer.KindString, "string literal")
		if err != nil {
			return ast.ColumnDefinition{}, err
		}
		column.Purpose = purpose.Value
	}
	return column, nil
}

func (parser *parser) parseAlter() (ast.Statement, error) {
	if parser.matchWord("ROUTE") {
		return parser.parseRenameRoute()
	}
	alter := &ast.AlterStatement{}
	switch {
	case parser.matchWord("DATABASE"):
		alter.Object = "DATABASE"
	case parser.matchWord("TABLE"):
		alter.Object = "TABLE"
	default:
		return ast.Statement{}, parser.unexpected("DATABASE or TABLE")
	}
	name, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	alter.Name = name

	switch {
	case parser.matchWord("ADD") && alter.Object == "TABLE":
		if _, err := parser.expectWord("COLUMN"); err != nil {
			return ast.Statement{}, err
		}
		column, err := parser.parseColumnDefinition()
		if err != nil {
			return ast.Statement{}, err
		}
		alter.Action = "ADD_COLUMN"
		alter.Column = &column
	case parser.matchWord("RENAME"):
		if alter.Object == "TABLE" && parser.matchWord("COLUMN") {
			columnName, err := parser.parseIdentifier()
			if err != nil {
				return ast.Statement{}, err
			}
			if _, err := parser.expectWord("TO"); err != nil {
				return ast.Statement{}, err
			}
			newName, err := parser.parseIdentifier()
			if err != nil {
				return ast.Statement{}, err
			}
			alter.Action = "RENAME_COLUMN"
			alter.ColumnName = &columnName
			alter.NewName = &newName
			break
		}
		if _, err := parser.expectWord("TO"); err != nil {
			return ast.Statement{}, err
		}
		newName, err := parser.parseIdentifier()
		if err != nil {
			return ast.Statement{}, err
		}
		alter.Action = "RENAME"
		alter.NewName = &newName
	default:
		return ast.Statement{}, parser.unexpected("ADD COLUMN or RENAME")
	}
	return ast.Statement{Kind: "ALTER", Alter: alter}, nil
}

func (parser *parser) parseSelect() (ast.Statement, error) {
	selectStatement := &ast.SelectStatement{}
	for {
		if parser.matchKind(lexer.KindStar) {
			token := parser.previous()
			selectStatement.Projections = append(selectStatement.Projections, ast.Expression{Kind: "star", Span: token.Span})
		} else {
			expression, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			selectStatement.Projections = append(selectStatement.Projections, expression)
		}
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	if _, err := parser.expectWord("FROM"); err != nil {
		return ast.Statement{}, err
	}
	from, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	selectStatement.From = from
	if parser.matchWord("AS") {
		if _, err := parser.expectWord("OF"); err != nil {
			return ast.Statement{}, err
		}
		asOf := &ast.AsOfClause{}
		switch {
		case parser.matchWord("REVISION"):
			asOf.Kind = "REVISION"
		case parser.matchWord("COMMIT_SEQUENCE"):
			asOf.Kind = "COMMIT_SEQUENCE"
		default:
			return ast.Statement{}, parser.unexpected("REVISION or COMMIT_SEQUENCE")
		}
		value, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		asOf.Value = value
		selectStatement.AsOf = asOf
	}
	if parser.matchWord("WHERE") {
		where, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		selectStatement.Where = &where
	}
	if parser.matchWord("LIMIT") {
		limit, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		selectStatement.Limit = &limit
	}
	return ast.Statement{Kind: "SELECT", Select: selectStatement}, nil
}

func (parser *parser) parseInsert() (ast.Statement, error) {
	if _, err := parser.expectWord("INTO"); err != nil {
		return ast.Statement{}, err
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	insert := &ast.InsertStatement{Table: table}
	if parser.matchKind(lexer.KindLeftParen) {
		for {
			column, err := parser.parseIdentifier()
			if err != nil {
				return ast.Statement{}, err
			}
			insert.Columns = append(insert.Columns, column)
			if !parser.matchKind(lexer.KindComma) {
				break
			}
		}
		if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
			return ast.Statement{}, err
		}
	}
	if _, err := parser.expectWord("VALUES"); err != nil {
		return ast.Statement{}, err
	}
	for {
		if _, err := parser.expectKind(lexer.KindLeftParen, "("); err != nil {
			return ast.Statement{}, err
		}
		var row []ast.Expression
		for {
			value, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			row = append(row, value)
			if !parser.matchKind(lexer.KindComma) {
				break
			}
		}
		if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
			return ast.Statement{}, err
		}
		insert.Values = append(insert.Values, row)
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	return ast.Statement{Kind: "INSERT", Insert: insert}, nil
}

func (parser *parser) parseUpdate() (ast.Statement, error) {
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("SET"); err != nil {
		return ast.Statement{}, err
	}
	update := &ast.UpdateStatement{Table: table}
	for {
		column, err := parser.parseIdentifier()
		if err != nil {
			return ast.Statement{}, err
		}
		if _, err := parser.expectOperator("="); err != nil {
			return ast.Statement{}, err
		}
		value, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		update.Assignments = append(update.Assignments, ast.Assignment{Column: column, Value: value})
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	if parser.matchWord("WHERE") {
		where, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		update.Where = &where
	}
	return ast.Statement{Kind: "UPDATE", Update: update}, nil
}

func (parser *parser) parseDelete() (ast.Statement, error) {
	if parser.matchWord("ROUTE") {
		route, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		return ast.Statement{Kind: "DELETE_ROUTE", DeleteRoute: &ast.DeleteRouteStatement{
			Route: &route,
		}}, nil
	}
	if _, err := parser.expectWord("FROM"); err != nil {
		return ast.Statement{}, err
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	deleteStatement := &ast.DeleteStatement{Table: table}
	if parser.matchWord("WHERE") {
		where, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		deleteStatement.Where = &where
	}
	return ast.Statement{Kind: "DELETE", Delete: deleteStatement}, nil
}

func (parser *parser) parseRestore() (ast.Statement, error) {
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ROW"); err != nil {
		return ast.Statement{}, err
	}
	rowExpression, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TO"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("REVISION"); err != nil {
		return ast.Statement{}, err
	}
	revision, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "RESTORE", Restore: &ast.RestoreStatement{
		Table: table, Row: &rowExpression, Revision: &revision,
	}}, nil
}

func (parser *parser) parseRelate() (ast.Statement, error) {
	sourceTable, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ROW"); err != nil {
		return ast.Statement{}, err
	}
	sourceRow, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TO"); err != nil {
		return ast.Statement{}, err
	}
	targetTable, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ROW"); err != nil {
		return ast.Statement{}, err
	}
	targetRow, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TYPE"); err != nil {
		return ast.Statement{}, err
	}
	relationType, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	statement := &ast.RelateStatement{
		SourceTable: sourceTable, SourceRow: &sourceRow,
		TargetTable: targetTable, TargetRow: &targetRow, Type: &relationType,
	}
	if parser.matchWord("DESCRIPTION") {
		description, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Description = &description
	}
	return ast.Statement{Kind: "RELATE", Relate: statement}, nil
}

func (parser *parser) parseUnrelate() (ast.Statement, error) {
	relationExpression, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "UNRELATE", Unrelate: &ast.UnrelateStatement{
		Relation: &relationExpression,
	}}, nil
}

func (parser *parser) parseMatch() (ast.Statement, error) {
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("QUERY"); err != nil {
		return ast.Statement{}, err
	}
	query, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TERMS"); err != nil {
		return ast.Statement{}, err
	}
	terms, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("LIMIT"); err != nil {
		return ast.Statement{}, err
	}
	limit, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "MATCH", Match: &ast.MatchStatement{
		Table: table, Query: &query, Terms: &terms, Limit: &limit,
	}}, nil
}

func (parser *parser) parseCreateRoute() (ast.Statement, error) {
	statement := &ast.CreateRouteStatement{}
	switch {
	case parser.matchWord("ROOT"):
		if _, err := parser.expectWord("FOR"); err != nil {
			return ast.Statement{}, err
		}
		if parser.matchWord("TABLE") {
			statement.Mode = "TABLE_ROOT"
			table, err := parser.parseName()
			if err != nil {
				return ast.Statement{}, err
			}
			statement.Table = &table
		} else {
			statement.Mode = "ROOT"
			if _, err := parser.expectWord("DATABASE"); err != nil {
				return ast.Statement{}, err
			}
			database, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			statement.Database = &database
		}
	case parser.matchWord("UNDER"):
		statement.Mode = "CHILD"
		parent, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Parent = &parent
		if _, err := parser.expectWord("NAME"); err != nil {
			return ast.Statement{}, err
		}
		name, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Name = &name
		if _, err := parser.expectWord("KIND"); err != nil {
			return ast.Statement{}, err
		}
		nodeKind, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.NodeKind = &nodeKind
	default:
		return ast.Statement{}, parser.unexpected("ROOT or UNDER")
	}
	if _, err := parser.expectWord("PURPOSE"); err != nil {
		return ast.Statement{}, err
	}
	purpose, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	statement.Purpose = &purpose
	return ast.Statement{Kind: "CREATE_ROUTE", CreateRoute: statement}, nil
}

func (parser *parser) parseRenameRoute() (ast.Statement, error) {
	route, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("RENAME"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TO"); err != nil {
		return ast.Statement{}, err
	}
	name, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "RENAME_ROUTE", RenameRoute: &ast.RenameRouteStatement{
		Route: &route, Name: &name,
	}}, nil
}

func (parser *parser) parseOpenRoute() (ast.Statement, error) {
	if _, err := parser.expectWord("ROUTE"); err != nil {
		return ast.Statement{}, err
	}
	statement := &ast.OpenRouteStatement{}
	if parser.matchWord("FROM") {
		statement.Mode = "PATH"
		if _, err := parser.expectWord("DATABASE"); err != nil {
			return ast.Statement{}, err
		}
		database, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Database = &database
		if _, err := parser.expectWord("AT"); err != nil {
			return ast.Statement{}, err
		}
		path, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Path = &path
	} else {
		statement.Mode = "ID"
		route, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Route = &route
	}
	if _, err := parser.expectWord("LIMIT"); err != nil {
		return ast.Statement{}, err
	}
	limit, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	statement.Limit = &limit
	return ast.Statement{Kind: "OPEN_ROUTE", OpenRoute: statement}, nil
}

func (parser *parser) parsePackDatabase() (ast.Statement, error) {
	if _, err := parser.expectWord("DATABASE"); err != nil {
		return ast.Statement{}, err
	}
	database, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("BY"); err != nil {
		return ast.Statement{}, err
	}
	author, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "PACK_DATABASE", Package: &ast.PackageStatement{
		Action: "PACK", Database: database, Author: &author,
	}}, nil
}

func (parser *parser) parseExportWiki() (ast.Statement, error) {
	if _, err := parser.expectWord("WIKI"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TO"); err != nil {
		return ast.Statement{}, err
	}
	path, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("PROFILE"); err != nil {
		return ast.Statement{}, err
	}
	profile, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "EXPORT_WIKI", Export: &ast.ExportStatement{
		Format: "WIKI", Path: &path, Profile: &profile,
	}}, nil
}

func (parser *parser) parseOpenPackage() (ast.Statement, error) {
	if _, err := parser.expectWord("PACKAGE"); err != nil {
		return ast.Statement{}, err
	}
	value, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("READ"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ONLY"); err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "OPEN_PACKAGE", Package: &ast.PackageStatement{
		Action: "OPEN", Value: &value, ReadOnly: true,
	}}, nil
}

func (parser *parser) parseInstallPackage() (ast.Statement, error) {
	if _, err := parser.expectWord("PACKAGE"); err != nil {
		return ast.Statement{}, err
	}
	value, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("TRUSTED"); err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "INSTALL_PACKAGE", Package: &ast.PackageStatement{
		Action: "INSTALL", Value: &value, Trusted: true,
	}}, nil
}

func (parser *parser) parseName() (ast.Name, error) {
	first, err := parser.parseIdentifier()
	if err != nil {
		return ast.Name{}, err
	}
	name := ast.Name{Parts: []ast.Identifier{first}, Span: first.Span}
	for parser.matchKind(lexer.KindDot) {
		part, err := parser.parseIdentifier()
		if err != nil {
			return ast.Name{}, err
		}
		name.Parts = append(name.Parts, part)
		name.Span.End = part.Span.End
	}
	return name, nil
}

func (parser *parser) parseIdentifier() (ast.Identifier, error) {
	token := parser.peek()
	if token.Kind == lexer.KindQuotedIdentifier {
		parser.advance()
		return ast.Identifier{Value: token.Value, Quoted: true, Span: token.Span}, nil
	}
	if token.Kind != lexer.KindWord || token.IsKeyword(token.Value) {
		return ast.Identifier{}, parser.unexpected("identifier")
	}
	parser.advance()
	return ast.Identifier{Value: token.Value, Span: token.Span}, nil
}

func (parser *parser) matchWord(word string) bool {
	if !parser.checkWord(word) {
		return false
	}
	parser.advance()
	return true
}

func (parser *parser) checkWord(word string) bool {
	token := parser.peek()
	return token.Kind == lexer.KindWord && strings.EqualFold(token.Value, word)
}

func (parser *parser) expectWord(word string) (lexer.Token, error) {
	if parser.matchWord(word) {
		return parser.previous(), nil
	}
	return lexer.Token{}, parser.unexpected(word)
}

func (parser *parser) expectOperator(operator string) (lexer.Token, error) {
	token := parser.peek()
	if token.Kind == lexer.KindOperator && token.Lexeme == operator {
		parser.advance()
		return token, nil
	}
	return lexer.Token{}, parser.unexpected(operator)
}

func (parser *parser) matchKind(kind lexer.Kind) bool {
	if !parser.checkKind(kind) {
		return false
	}
	parser.advance()
	return true
}

func (parser *parser) checkKind(kind lexer.Kind) bool {
	return parser.peek().Kind == kind
}

func (parser *parser) expectKind(kind lexer.Kind, expected string) (lexer.Token, error) {
	if parser.matchKind(kind) {
		return parser.previous(), nil
	}
	return lexer.Token{}, parser.unexpected(expected)
}

func (parser *parser) advance() lexer.Token {
	if !parser.checkKind(lexer.KindEOF) {
		parser.current++
	}
	return parser.previous()
}

func (parser *parser) peek() lexer.Token {
	return parser.tokens[parser.current]
}

func (parser *parser) previous() lexer.Token {
	if parser.current == 0 {
		return parser.tokens[0]
	}
	return parser.tokens[parser.current-1]
}

func (parser *parser) unexpected(expected string) *Error {
	token := parser.peek()
	code := ErrorUnexpectedToken
	if token.Kind == lexer.KindEOF {
		code = ErrorUnexpectedEOF
	}
	return &Error{Code: code, Span: token.Span, Expected: expected, Found: tokenDescription(token), StatementIndex: -1}
}

func (parser *parser) errorAt(token lexer.Token, expected string) *Error {
	return &Error{Code: ErrorUnexpectedToken, Span: token.Span, Expected: expected, Found: tokenDescription(token), StatementIndex: -1}
}

func withStatementIndex(err error, index int) error {
	if parseError, ok := err.(*Error); ok {
		parseError.StatementIndex = index
	}
	return err
}

func tokenDescription(token lexer.Token) string {
	if token.Kind == lexer.KindEOF {
		return "EOF"
	}
	return strconv.Quote(token.Lexeme)
}
