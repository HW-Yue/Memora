package mcpadapter

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

const (
	ModernVersion      = "2026-07-28"
	LegacyVersion      = "2025-11-25"
	LegacyVersionOld   = "2025-06-18"
	ToolName           = "memora_execute"
	DefaultMaxLineSize = 1 << 20
)

var ErrMessageTooLarge = errors.New("MCP message exceeds the maximum line size")

type ExecuteFunc func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error)

type Config struct {
	DataDir     string
	Version     string
	Execute     ExecuteFunc
	MaxLineSize int
}

type Server struct{ config Config }

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type session struct {
	legacyPending     bool
	legacyInitialized bool
}

func New(config Config) *Server {
	if config.Execute == nil {
		config.Execute = daemon.Execute
	}
	if config.Version == "" {
		config.Version = "dev"
	}
	if config.MaxLineSize <= 0 {
		config.MaxLineSize = DefaultMaxLineSize
	}
	return &Server{config: config}
}

func (server *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, min(64<<10, server.config.MaxLineSize)), server.config.MaxLineSize)
	encoder := json.NewEncoder(output)
	state := session{}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		line := scanner.Bytes()
		message, emit := server.handle(ctx, line, &state)
		if emit {
			if err := encoder.Encode(message); err != nil {
				return fmt.Errorf("write MCP response: %w", err)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		_ = encoder.Encode(errorResponse(nil, -32700, ErrMessageTooLarge.Error(), nil))
		return ErrMessageTooLarge
	}
	return nil
}

func (server *Server) handle(ctx context.Context, encoded []byte, state *session) (response, bool) {
	var call request
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&call); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return errorResponse(nil, -32700, "parse error", nil), true
	}
	if call.JSONRPC != "2.0" || call.Method == "" || !validID(call.ID) {
		return errorResponse(call.ID, -32600, "invalid request", nil), true
	}
	notification := len(call.ID) == 0
	if notification {
		switch call.Method {
		case "notifications/initialized":
			if state.legacyPending {
				state.legacyInitialized = true
				state.legacyPending = false
			}
		case "notifications/cancelled":
		}
		return response{}, false
	}

	modern, version, metaErr := requestEra(call.Method, call.Params)
	if metaErr != nil {
		return errorResponse(call.ID, metaErr.Code, metaErr.Message, metaErr.Data), true
	}
	if modern && version != ModernVersion {
		return unsupportedVersion(call.ID, version), true
	}
	if !modern && call.Method != "initialize" && !state.legacyInitialized {
		return errorResponse(call.ID, -32600, "legacy MCP client is not initialized", nil), true
	}

	resultValue, requestErr := server.dispatch(ctx, call, modern, state)
	if requestErr != nil {
		return errorResponse(call.ID, requestErr.Code, requestErr.Message, requestErr.Data), true
	}
	return response{JSONRPC: "2.0", ID: call.ID, Result: resultValue}, true
}

func (server *Server) dispatch(ctx context.Context, call request, modern bool, state *session) (any, *rpcError) {
	switch call.Method {
	case "server/discover":
		if !modern {
			return nil, &rpcError{Code: -32602, Message: "server/discover requires modern MCP metadata"}
		}
		return server.modernResult(map[string]any{
			"supportedVersions": []string{ModernVersion, LegacyVersion, LegacyVersionOld},
			"capabilities":      map[string]any{"tools": map[string]any{}},
			"instructions":      "Use memora_execute for all logical database operations. Every statement requires explicit Memora Authorization v2.",
		}), nil
	case "initialize":
		if modern {
			return nil, &rpcError{Code: -32601, Message: "initialize is not used by modern MCP"}
		}
		var params struct {
			ProtocolVersion string         `json:"protocolVersion"`
			Capabilities    map[string]any `json:"capabilities"`
			ClientInfo      map[string]any `json:"clientInfo"`
		}
		if !strictDecode(call.Params, &params) || params.ProtocolVersion == "" || params.Capabilities == nil || params.ClientInfo == nil {
			return nil, &rpcError{Code: -32602, Message: "initialize params are invalid"}
		}
		negotiated := params.ProtocolVersion
		if negotiated != LegacyVersion && negotiated != LegacyVersionOld {
			negotiated = LegacyVersion
		}
		state.legacyPending = true
		state.legacyInitialized = false
		return map[string]any{
			"protocolVersion": negotiated,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      server.serverInfo(),
			"instructions":    "Use memora_execute with explicit Memora Authorization v2 for each statement.",
		}, nil
	case "ping":
		return server.resultForEra(modern, map[string]any{}), nil
	case "tools/list":
		var params struct {
			Cursor string          `json:"cursor,omitempty"`
			Meta   json.RawMessage `json:"_meta,omitempty"`
		}
		if len(call.Params) > 0 && !strictDecode(call.Params, &params) {
			return nil, &rpcError{Code: -32602, Message: "tools/list params are invalid"}
		}
		if params.Cursor != "" {
			return nil, &rpcError{Code: -32602, Message: "tools/list cursor is invalid"}
		}
		return server.resultForEra(modern, map[string]any{"tools": []any{toolDefinition()}}), nil
	case "tools/call":
		return server.callTool(ctx, call.Params, modern)
	default:
		return nil, &rpcError{Code: -32601, Message: "method not found"}
	}
}

func (server *Server) callTool(ctx context.Context, encoded json.RawMessage, modern bool) (any, *rpcError) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
		Meta      json.RawMessage `json:"_meta,omitempty"`
	}
	if !strictDecode(encoded, &params) || params.Name == "" || len(params.Arguments) == 0 {
		return nil, &rpcError{Code: -32602, Message: "tools/call params are invalid"}
	}
	if params.Name != ToolName {
		return nil, &rpcError{Code: -32602, Message: "unknown tool: " + params.Name}
	}
	var arguments struct {
		Source     string                    `json:"source"`
		Statements []executor.StatementInput `json:"statements"`
	}
	if !strictDecode(params.Arguments, &arguments) || strings.TrimSpace(arguments.Source) == "" ||
		len(arguments.Source) > DefaultMaxLineSize || len(arguments.Statements) == 0 || len(arguments.Statements) > 128 {
		return server.toolError(modern, "memora_execute arguments are invalid"), nil
	}
	for _, statement := range arguments.Statements {
		if err := statement.Authorization.Validate(); err != nil {
			return server.toolError(modern, err.Error()), nil
		}
	}
	envelope, err := server.config.Execute(ctx, server.config.DataDir, arguments.Source, arguments.Statements)
	if err != nil {
		return server.toolError(modern, "Memora daemon call failed: "+err.Error()), nil
	}
	text, err := json.Marshal(envelope)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: "encode Memora result"}
	}
	value := map[string]any{
		"content":           []any{map[string]any{"type": "text", "text": string(text)}},
		"structuredContent": envelope,
		"isError":           !envelope.OK,
	}
	return server.resultForEra(modern, value), nil
}

func (server *Server) toolError(modern bool, message string) any {
	return server.resultForEra(modern, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": message}},
		"isError": true,
	})
}

func (server *Server) resultForEra(modern bool, value map[string]any) map[string]any {
	if modern {
		return server.modernResult(value)
	}
	return value
}

func (server *Server) modernResult(value map[string]any) map[string]any {
	value["resultType"] = "complete"
	value["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": server.serverInfo()}
	return value
}

func (server *Server) serverInfo() map[string]any {
	return map[string]any{"name": "memora", "title": "Memora", "version": server.config.Version}
}

func requestEra(method string, encoded json.RawMessage) (bool, string, *rpcError) {
	if method == "initialize" {
		return false, "", nil
	}
	var params struct {
		Meta struct {
			ProtocolVersion    string          `json:"io.modelcontextprotocol/protocolVersion"`
			ClientCapabilities json.RawMessage `json:"io.modelcontextprotocol/clientCapabilities"`
		} `json:"_meta"`
	}
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &params); err != nil {
			return false, "", &rpcError{Code: -32602, Message: "request params are invalid"}
		}
	}
	if params.Meta.ProtocolVersion == "" {
		if method == "server/discover" {
			return false, "", &rpcError{Code: -32602, Message: "modern MCP metadata is required"}
		}
		return false, "", nil
	}
	var capabilities map[string]any
	if len(params.Meta.ClientCapabilities) == 0 || json.Unmarshal(params.Meta.ClientCapabilities, &capabilities) != nil || capabilities == nil {
		return false, "", &rpcError{Code: -32602, Message: "client capabilities metadata is required"}
	}
	return true, params.Meta.ProtocolVersion, nil
}

func toolDefinition() map[string]any {
	authorization := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"version":              map[string]any{"const": "memora.authorization/v2"},
			"actor":                map[string]any{"type": "string"},
			"authorized_databases": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 1, "maxItems": 32},
			"default_level":        map[string]any{"type": "string", "enum": []string{"L0", "L1", "L2", "L3"}},
			"database_levels":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string", "enum": []string{"L0", "L1", "L2", "L3"}}},
			"approval":             map[string]any{"type": "object"},
		},
		"required":             []string{"version", "actor", "authorized_databases"},
		"additionalProperties": false,
	}
	statement := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"parameters":    map[string]any{"type": "object"},
			"mutation":      map[string]any{"type": "object"},
			"authorization": authorization,
		},
		"required":             []string{"authorization"},
		"additionalProperties": false,
	}
	return map[string]any{
		"name":        ToolName,
		"title":       "Execute Memora MSQL",
		"description": "Execute one MSQL batch through the local Memora daemon. Use SHOW/OPEN ROUTE/SELECT for discovery and PLAN/INSERT/UPDATE/DELETE/DDL for approved changes. Supply one StatementInput per MSQL statement with explicit Authorization v2; the engine enforces L0-L3 and database scope.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"source":     map[string]any{"type": "string", "minLength": 1, "description": "MSQL source; may contain a bounded batch."},
				"statements": map[string]any{"type": "array", "items": statement, "minItems": 1, "maxItems": 128},
			},
			"required":             []string{"source", "statements"},
			"additionalProperties": false,
		},
	}
}

func strictDecode(encoded []byte, target any) bool {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func validID(id json.RawMessage) bool {
	if len(id) == 0 {
		return true
	}
	if bytes.Equal(id, []byte("null")) {
		return false
	}
	var text string
	if json.Unmarshal(id, &text) == nil {
		return true
	}
	var number json.Number
	decoder := json.NewDecoder(bytes.NewReader(id))
	decoder.UseNumber()
	return decoder.Decode(&number) == nil
}

func errorResponse(id json.RawMessage, code int, message string, data any) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message, Data: data}}
}

func unsupportedVersion(id json.RawMessage, requested string) response {
	return errorResponse(id, -32022, "unsupported protocol version", map[string]any{
		"supported": []string{ModernVersion, LegacyVersion, LegacyVersionOld}, "requested": requested,
	})
}
