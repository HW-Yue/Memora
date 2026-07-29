package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/lexer"
	"github.com/HW-Yue/Memora/internal/msql/parser"
)

type ParseResponse struct {
	OK    bool        `json:"ok"`
	AST   *ast.Batch  `json:"ast,omitempty"`
	Error *ParseIssue `json:"error,omitempty"`
}

type ParseIssue struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	StatementIndex *int   `json:"statement_index,omitempty"`
	Line           int    `json:"line"`
	Column         int    `json:"column"`
}

func Parse(ctx context.Context, dataDir, source string) (ParseResponse, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return ParseResponse{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return ParseResponse{}, err
	}
	defer func() { _ = client.Close() }()
	var response ParseResponse
	if err := client.Call(ctx, "msql.parse", map[string]string{"source": source}, &response); err != nil {
		return ParseResponse{}, err
	}
	return response, nil
}

func handleRequest(_ context.Context, _ ipc.Session, request ipc.Request) (json.RawMessage, error) {
	switch request.Method {
	case "ping":
		return json.RawMessage(`{"message":"pong"}`), nil
	case "msql.parse":
		var payload struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(request.Payload, &payload); err != nil {
			return nil, fmt.Errorf("decode MSQL parse request: %w", err)
		}
		batch, err := parser.ParseBatch(payload.Source)
		response := ParseResponse{OK: err == nil, AST: batch}
		if err != nil {
			response.Error = parseIssue(err)
		}
		return json.Marshal(response)
	default:
		return nil, fmt.Errorf("unknown IPC method %q", request.Method)
	}
}

func parseIssue(err error) *ParseIssue {
	var parseError *parser.Error
	if errors.As(err, &parseError) {
		issue := &ParseIssue{
			Code: string(parseError.Code), Message: parseError.Error(),
			Line: parseError.Span.Start.Line, Column: parseError.Span.Start.Column,
		}
		if parseError.StatementIndex >= 0 {
			index := parseError.StatementIndex
			issue.StatementIndex = &index
		}
		return issue
	}
	var lexError *lexer.Error
	if errors.As(err, &lexError) {
		return &ParseIssue{
			Code: string(lexError.Code), Message: lexError.Error(),
			Line: lexError.Span.Start.Line, Column: lexError.Span.Start.Column,
		}
	}
	return &ParseIssue{Code: "internal_error", Message: "MSQL parser failed", Line: 1, Column: 1}
}
