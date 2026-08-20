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
		if parser.matchWord("CONFIGURATION") {
			statement, err = parser.parseRestoreConfiguration()
		} else {
			statement, err = parser.parseRestore()
		}
	case parser.matchWord("ARCHIVE"):
		statement, err = parser.parseArchive(false)
	case parser.matchWord("UNARCHIVE"):
		statement, err = parser.parseArchive(true)
	case parser.matchWord("SPLIT"):
		statement, err = parser.parseSplit()
	case parser.matchWord("MERGE"):
		statement, err = parser.parseMerge()
	case parser.matchWord("PLAN"):
		if parser.checkWord("SCHEMA") {
			statement, err = parser.parsePlanSchemaChange()
		} else {
			statement, err = parser.parsePlanRouteMutation()
		}
	case parser.matchWord("APPLY"):
		if parser.checkWord("SCHEMA") {
			statement, err = parser.parseApplySchemaChange()
		} else {
			statement, err = parser.parseApplyRouteMutation()
		}
	case parser.matchWord("RELATE"):
		statement, err = parser.parseRelate()
	case parser.matchWord("UNRELATE"):
		statement, err = parser.parseUnrelate()
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
	case parser.matchWord("REBUILD"):
		statement, err = parser.parseRebuildLexicalIndex()
	case parser.matchWord("REVIEW"):
		statement, err = parser.parseReviewAssimilation()
	case parser.matchWord("SUBMIT"):
		statement, err = parser.parseSubmitAssimilation()
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

func (parser *parser) parseReviewAssimilation() (ast.Statement, error) {
	for _, word := range []string{"ASSIMILATION", "FOR", "DATABASE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	database, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("USING"); err != nil {
		return ast.Statement{}, err
	}
	value, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "REVIEW_ASSIMILATION", Assimilation: &ast.AssimilationStatement{
		Action: "REVIEW", Database: database, Value: &value,
	}}, nil
}

func (parser *parser) parseSubmitAssimilation() (ast.Statement, error) {
	for _, word := range []string{"ASSIMILATION", "PLAN"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	value, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	for _, word := range []string{"FOR", "DATABASE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	database, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "SUBMIT_ASSIMILATION", Assimilation: &ast.AssimilationStatement{
		Action: "SUBMIT", Database: database, Value: &value,
	}}, nil
}

func (parser *parser) parseRebuildLexicalIndex() (ast.Statement, error) {
	for _, word := range []string{"LEXICAL", "INDEX"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	return ast.Statement{
		Kind: "REBUILD_LEXICAL_INDEX", Rebuild: &ast.RebuildStatement{Object: "LEXICAL_INDEX"},
	}, nil
}

func transactionStatement(action string) ast.Statement {
	return ast.Statement{Kind: action, Transaction: &ast.TransactionStatement{Action: action}}
}

func (parser *parser) parsePlanRouteMutation() (ast.Statement, error) {
	for _, word := range []string{"ROUTE", "MUTATION", "FOR", "TABLE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("USING"); err != nil {
		return ast.Statement{}, err
	}
	proposal, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "PLAN_ROUTE_MUTATION", PlanRoute: &ast.PlanRouteMutationStatement{
		Table: table, Proposal: &proposal,
	}}, nil
}

func (parser *parser) parseApplyRouteMutation() (ast.Statement, error) {
	for _, word := range []string{"ROUTE", "MUTATION", "PLAN"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	plan, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	for _, word := range []string{"FOR", "TABLE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "APPLY_ROUTE_MUTATION", ApplyRoute: &ast.ApplyRouteMutationStatement{
		Table: table, Plan: &plan,
	}}, nil
}

func (parser *parser) parseApplySchemaChange() (ast.Statement, error) {
	for _, word := range []string{"SCHEMA", "CHANGE", "PLAN"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	plan, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	for _, word := range []string{"FOR", "TABLE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "APPLY_SCHEMA_CHANGE", ApplySchema: &ast.ApplySchemaChangeStatement{
		Table: table, Plan: &plan,
	}}, nil
}

func (parser *parser) parsePlanSchemaChange() (ast.Statement, error) {
	for _, word := range []string{"SCHEMA", "CHANGE", "FOR", "TABLE"} {
		if _, err := parser.expectWord(word); err != nil {
			return ast.Statement{}, err
		}
	}
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("USING"); err != nil {
		return ast.Statement{}, err
	}
	proposal, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "PLAN_SCHEMA_CHANGE", PlanSchema: &ast.PlanSchemaChangeStatement{
		Table: table, Proposal: &proposal,
	}}, nil
}

func (parser *parser) parseShow() (ast.Statement, error) {
	show := &ast.ShowStatement{}
	switch {
	case parser.matchWord("INSTANCE"):
		show.Object = "INSTANCE"
	case parser.matchWord("CONFIGURATION"):
		show.Object = "CONFIGURATION"
		show.Key = "QUERY_BUDGETS"
		if parser.matchWord("ROUTE_POLICY") {
			show.Key = "ROUTE_POLICY"
		} else {
			_ = parser.matchWord("QUERY_BUDGETS")
		}
		if parser.matchWord("HISTORY") {
			show.Direction = "HISTORY"
			if _, err := parser.expectWord("LIMIT"); err != nil {
				return ast.Statement{}, err
			}
			limit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Limit = &limit
		}
	case parser.matchWord("DATABASES"):
		show.Object = "DATABASES"
	case parser.matchWord("CATALOG"):
		if _, err := parser.expectWord("ATLAS"); err != nil {
			return ast.Statement{}, err
		}
		show.Object = "CATALOG_ATLAS"
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
	case parser.matchWord("ASSIMILATION"):
		if _, err := parser.expectWord("RECEIPT"); err != nil {
			return ast.Statement{}, err
		}
		value, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		for _, word := range []string{"IN", "DATABASE"} {
			if _, err := parser.expectWord(word); err != nil {
				return ast.Statement{}, err
			}
		}
		database, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		return ast.Statement{Kind: "SHOW_ASSIMILATION_RECEIPT", Assimilation: &ast.AssimilationStatement{
			Action: "RECEIPT", Database: database, Value: &value,
		}}, nil
	case parser.matchWord("LEXICAL"):
		if _, err := parser.expectWord("LOCATIONS"); err != nil {
			return ast.Statement{}, err
		}
		show.Object = "LEXICAL_LOCATIONS"
		for _, word := range []string{"FROM", "ALL", "TABLES", "USING"} {
			if _, err := parser.expectWord(word); err != nil {
				return ast.Statement{}, err
			}
		}
		query, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Query = &query
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
		if _, err := parser.expectWord("BYTES"); err != nil {
			return ast.Statement{}, err
		}
		byteLimit, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.ByteLimit = &byteLimit
	case parser.matchWord("CHANGES"):
		show.Object = "CHANGES"
		if parser.matchWord("IN") {
			if _, err := parser.expectWord("DATABASE"); err != nil {
				return ast.Statement{}, err
			}
			name, err := parser.parseName()
			if err != nil {
				return ast.Statement{}, err
			}
			show.Database = &name
		}
		if parser.matchWord("AFTER") {
			if _, err := parser.expectWord("COMMIT_SEQUENCE"); err != nil {
				return ast.Statement{}, err
			}
			after, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.After = &after
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
	case parser.matchWord("CHANGE"):
		show.Object = "CHANGE"
		changeID, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		show.Change = &changeID
		if parser.matchWord("IN") {
			if _, err := parser.expectWord("DATABASE"); err != nil {
				return ast.Statement{}, err
			}
			name, err := parser.parseName()
			if err != nil {
				return ast.Statement{}, err
			}
			show.Database = &name
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
	case parser.matchWord("ROUTE"):
		switch {
		case parser.matchWord("CANDIDATES"):
			show.Object = "ROUTE_CANDIDATES"
			if _, err := parser.expectWord("FROM"); err != nil {
				return ast.Statement{}, err
			}
			if _, err := parser.expectWord("ALL"); err != nil {
				return ast.Statement{}, err
			}
			if _, err := parser.expectWord("TABLES"); err != nil {
				return ast.Statement{}, err
			}
			if _, err := parser.expectWord("USING"); err != nil {
				return ast.Statement{}, err
			}
			switch {
			case parser.matchWord("LEXICAL"):
				show.Predictor = "LEXICAL"
			case parser.matchWord("VECTOR"):
				show.Predictor = "VECTOR"
			default:
				return ast.Statement{}, parser.unexpected("LEXICAL or VECTOR")
			}
			query, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Query = &query
			if show.Predictor == "VECTOR" {
				if _, err := parser.expectWord("SPACE"); err != nil {
					return ast.Statement{}, err
				}
				space, err := parser.parseExpression(1)
				if err != nil {
					return ast.Statement{}, err
				}
				show.Space = &space
			}
			if _, err := parser.expectWord("LIMIT"); err != nil {
				return ast.Statement{}, err
			}
			limit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Limit = &limit
			if _, err := parser.expectWord("BYTES"); err != nil {
				return ast.Statement{}, err
			}
			byteLimit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.ByteLimit = &byteLimit
		case parser.matchWord("TRACES"):
			show.Object = "ROUTE_TRACES"
			if parser.matchWord("IN") {
				if _, err := parser.expectWord("DATABASE"); err != nil {
					return ast.Statement{}, err
				}
				name, err := parser.parseName()
				if err != nil {
					return ast.Statement{}, err
				}
				show.Database = &name
			}
			if parser.matchWord("AFTER") {
				if _, err := parser.expectWord("TRACE_SEQUENCE"); err != nil {
					return ast.Statement{}, err
				}
				after, err := parser.parseExpression(1)
				if err != nil {
					return ast.Statement{}, err
				}
				show.After = &after
			}
		case parser.matchWord("TRACE"):
			show.Object = "ROUTE_TRACE"
			traceID, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Trace = &traceID
			if parser.matchWord("IN") {
				if _, err := parser.expectWord("DATABASE"); err != nil {
					return ast.Statement{}, err
				}
				name, err := parser.parseName()
				if err != nil {
					return ast.Statement{}, err
				}
				show.Database = &name
			}
		default:
			return ast.Statement{}, parser.unexpected("CANDIDATES, TRACE, or TRACES")
		}
		if show.Object != "ROUTE_CANDIDATES" && parser.matchWord("CURSOR") {
			cursor, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Cursor = &cursor
		}
		if show.Object != "ROUTE_CANDIDATES" {
			if _, err := parser.expectWord("LIMIT"); err != nil {
				return ast.Statement{}, err
			}
			limit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Limit = &limit
		}
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
			show.RouteMode = "TABLE_ROOT"
			if _, err := parser.expectWord("TABLE"); err != nil {
				return ast.Statement{}, err
			}
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
		default:
			return ast.Statement{}, parser.unexpected("UNDER or FROM TABLE")
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
		return ast.Statement{}, parser.unexpected("INSTANCE, CONFIGURATION, DATABASES, CATALOG ATLAS, TABLES, COLUMNS, ASSIMILATION RECEIPT, CHANGES, CHANGE, ROUTE CANDIDATES/TRACE, HISTORY, RELATIONS, or ROUTES")
	}
	if show.Object == "DATABASES" || show.Object == "TABLES" || show.Object == "COLUMNS" || show.Object == "CATALOG_ATLAS" {
		if parser.matchWord("CURSOR") {
			cursor, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Cursor = &cursor
		}
		if parser.matchWord("LIMIT") {
			limit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.Limit = &limit
		}
		if show.Object == "CATALOG_ATLAS" && parser.matchWord("BYTES") {
			byteLimit, err := parser.parseExpression(1)
			if err != nil {
				return ast.Statement{}, err
			}
			show.ByteLimit = &byteLimit
		}
	}
	show.Compact = parser.matchWord("COMPACT")
	if show.Object == "CATALOG_ATLAS" && !show.Compact {
		return ast.Statement{}, parser.unexpected("COMPACT")
	}
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
	case parser.matchWord("ROUTE"):
		describe.Object = "ROUTE"
		route, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		describe.Route = &route
		return ast.Statement{Kind: "DESCRIBE_ROUTE", Describe: describe}, nil
	default:
		return ast.Statement{}, parser.unexpected("DATABASE, TABLE, COLUMN, or ROUTE")
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
	if parser.matchWord("ROLE") {
		role, err := parser.parseIdentifier()
		if err != nil {
			return ast.ColumnDefinition{}, err
		}
		column.SemanticRole = strings.ToLower(role.Value)
	}
	return column, nil
}

func (parser *parser) parseAlter() (ast.Statement, error) {
	if parser.matchWord("ROUTE") {
		return parser.parseAlterRoute()
	}
	if parser.matchWord("CONFIGURATION") {
		return parser.parseAlterConfiguration()
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

func (parser *parser) parseAlterConfiguration() (ast.Statement, error) {
	key := "QUERY_BUDGETS"
	if parser.matchWord("ROUTE_POLICY") {
		key = "ROUTE_POLICY"
	} else if _, err := parser.expectWord("QUERY_BUDGETS"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("SET"); err != nil {
		return ast.Statement{}, err
	}
	value := &ast.ConfigurationStatement{Action: "ALTER", Key: key}
	fields := []struct {
		name string
		set  func(*ast.Expression)
	}{
		{"ROUTE_CHILDREN", func(expression *ast.Expression) { value.RouteChildren = expression }},
		{"OPEN_LOCATORS", func(expression *ast.Expression) { value.OpenLocators = expression }},
		{"SELECT_SCAN", func(expression *ast.Expression) { value.SelectScan = expression }},
		{"SELECT_ROWS", func(expression *ast.Expression) { value.SelectRows = expression }},
		{"ROUTE_FRAME_NODES", func(expression *ast.Expression) { value.RouteFrameNodes = expression }},
	}
	if key == "ROUTE_POLICY" {
		fields = []struct {
			name string
			set  func(*ast.Expression)
		}{
			{"BRANCH_FANOUT", func(expression *ast.Expression) { value.BranchFanout = expression }},
		}
	}
	for index, field := range fields {
		if index > 0 {
			if _, err := parser.expectKind(lexer.KindComma, ","); err != nil {
				return ast.Statement{}, err
			}
		}
		if _, err := parser.expectWord(field.name); err != nil {
			return ast.Statement{}, err
		}
		expression, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		field.set(&expression)
	}
	return ast.Statement{Kind: "ALTER_CONFIGURATION", Configuration: value}, nil
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

// parseArchive parses ARCHIVE and UNARCHIVE. Archiving is Memora's only delete
// semantics; each object kind resolves its target differently, so the grammar
// branches on the kind keyword rather than sharing one target rule.
func (parser *parser) parseArchive(restore bool) (ast.Statement, error) {
	statement := &ast.ArchiveStatement{Restore: restore}
	switch {
	case parser.matchWord("ROUTE"):
		statement.Object = "ROUTE"
		target, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Target = &target
	case parser.matchWord("DATABASE"):
		statement.Object = "DATABASE"
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Name = name
	case parser.matchWord("TABLE"):
		statement.Object = "TABLE"
		name, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Name = name
	default:
		return ast.Statement{}, parser.unexpected("DATABASE, TABLE or ROUTE")
	}
	if parser.matchWord("REASON") {
		reason, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Reason = &reason
	}
	if !restore && statement.Reason == nil {
		return ast.Statement{}, parser.unexpected("REASON")
	}
	return ast.Statement{Kind: archiveStatementKind(restore), Archive: statement}, nil
}

func archiveStatementKind(restore bool) string {
	if restore {
		return "UNARCHIVE"
	}
	return "ARCHIVE"
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

func (parser *parser) parseRestoreConfiguration() (ast.Statement, error) {
	key := "QUERY_BUDGETS"
	if parser.matchWord("ROUTE_POLICY") {
		key = "ROUTE_POLICY"
	} else if _, err := parser.expectWord("QUERY_BUDGETS"); err != nil {
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
	return ast.Statement{
		Kind: "RESTORE_CONFIGURATION",
		Configuration: &ast.ConfigurationStatement{
			Action: "RESTORE", Key: key, TargetRevision: &revision,
		},
	}, nil
}

func (parser *parser) parseSplit() (ast.Statement, error) {
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ROW"); err != nil {
		return ast.Statement{}, err
	}
	source, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	reshape := &ast.ReshapeStatement{Mode: "SPLIT", Table: table, Sources: []ast.Expression{source}}
	if err := parser.parseReshapeTargets(reshape); err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "SPLIT", Reshape: reshape}, nil
}

func (parser *parser) parseMerge() (ast.Statement, error) {
	table, err := parser.parseName()
	if err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectWord("ROWS"); err != nil {
		return ast.Statement{}, err
	}
	if _, err := parser.expectKind(lexer.KindLeftParen, "("); err != nil {
		return ast.Statement{}, err
	}
	reshape := &ast.ReshapeStatement{Mode: "MERGE", Table: table}
	for {
		source, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		reshape.Sources = append(reshape.Sources, source)
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
		return ast.Statement{}, err
	}
	if err := parser.parseReshapeTargets(reshape); err != nil {
		return ast.Statement{}, err
	}
	return ast.Statement{Kind: "MERGE", Reshape: reshape}, nil
}

func (parser *parser) parseReshapeTargets(reshape *ast.ReshapeStatement) error {
	if _, err := parser.expectWord("INTO"); err != nil {
		return err
	}
	if _, err := parser.expectKind(lexer.KindLeftParen, "("); err != nil {
		return err
	}
	for {
		column, err := parser.parseIdentifier()
		if err != nil {
			return err
		}
		reshape.Columns = append(reshape.Columns, column)
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
		return err
	}
	if _, err := parser.expectWord("VALUES"); err != nil {
		return err
	}
	for {
		if _, err := parser.expectKind(lexer.KindLeftParen, "("); err != nil {
			return err
		}
		var values []ast.Expression
		for {
			value, err := parser.parseExpression(1)
			if err != nil {
				return err
			}
			values = append(values, value)
			if !parser.matchKind(lexer.KindComma) {
				break
			}
		}
		if _, err := parser.expectKind(lexer.KindRightParen, ")"); err != nil {
			return err
		}
		reshape.Values = append(reshape.Values, values)
		if !parser.matchKind(lexer.KindComma) {
			break
		}
	}
	return nil
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

func (parser *parser) parseCreateRoute() (ast.Statement, error) {
	statement := &ast.CreateRouteStatement{}
	switch {
	case parser.matchWord("ROOT"):
		if _, err := parser.expectWord("FOR"); err != nil {
			return ast.Statement{}, err
		}
		statement.Mode = "TABLE_ROOT"
		if _, err := parser.expectWord("TABLE"); err != nil {
			return ast.Statement{}, err
		}
		table, err := parser.parseName()
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Table = &table
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
	if parser.matchWord("SYNOPSIS") {
		synopsis, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Synopsis = &synopsis
	}
	return ast.Statement{Kind: "CREATE_ROUTE", CreateRoute: statement}, nil
}

func (parser *parser) parseAlterRoute() (ast.Statement, error) {
	route, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	if parser.matchWord("RENAME") {
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
	if _, err := parser.expectWord("SET"); err != nil {
		return ast.Statement{}, err
	}
	statement := &ast.UpdateRouteStatement{Route: &route}
	switch {
	case parser.matchWord("SYNOPSIS"):
		synopsis, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Synopsis = &synopsis
	case parser.matchWord("ALIASES"):
		aliases, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Aliases = &aliases
	default:
		return ast.Statement{}, parser.unexpected("SYNOPSIS or ALIASES")
	}
	return ast.Statement{Kind: "UPDATE_ROUTE", UpdateRoute: statement}, nil
}

func (parser *parser) parseOpenRoute() (ast.Statement, error) {
	if _, err := parser.expectWord("ROUTE"); err != nil {
		return ast.Statement{}, err
	}
	statement := &ast.OpenRouteStatement{Mode: "ID"}
	route, err := parser.parseExpression(1)
	if err != nil {
		return ast.Statement{}, err
	}
	statement.Route = &route
	if parser.matchWord("CURSOR") {
		cursor, err := parser.parseExpression(1)
		if err != nil {
			return ast.Statement{}, err
		}
		statement.Cursor = &cursor
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
