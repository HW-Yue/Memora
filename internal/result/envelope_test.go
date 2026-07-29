package result

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEnvelopeGolden(t *testing.T) {
	t.Parallel()

	revision := uint64(7)
	tests := map[string]Envelope{
		"success": NewEnvelope("req-success", StatementResult{
			Index: 0, Statement: "SELECT", Source: "SELECT row_id, title FROM notes LIMIT 1", Status: StatusSucceeded,
			Columns: []Column{{Name: "row_id", Type: "ID", Nullable: false}, {Name: "title", Type: "TEXT", Nullable: false}},
			Rows:    []Row{{"row_id": "row-1", "title": "IPC design"}}, Revision: &revision,
		}),
		"failure": NewEnvelope("req-failure", FailedStatement(0, "UPDATE", "UPDATE notes SET body = ?", CodeValueTooLong, "body exceeds its 1200 character limit", false)),
		"truncated": func() Envelope {
			statement := NewStatement(0, "SHOW_ROUTES", "SHOW ROUTES FROM DATABASE work AT '/'")
			statement.Rows = []Row{{"route": "/architecture"}}
			statement.Truncated = true
			statement.NextCursor = "cursor-2"
			statement.Warnings = []Notice{{Code: CodeOutputTruncated, Message: "statement output reached its row budget"}}
			envelope := NewEnvelope("req-truncated", statement)
			envelope.Truncated = true
			envelope.NextCursor = "cursor-2"
			return envelope
		}(),
		"batch": NewEnvelope("req-batch",
			NewStatement(0, "SELECT", "SELECT * FROM notes LIMIT 1"),
			FailedStatement(1, "SELECT", "SELECT * FROM missing", CodeNotFound, "table missing was not found", false),
			NewStatement(2, "SHOW_DATABASES", "SHOW DATABASES"),
		),
	}
	for name, envelope := range tests {
		name, envelope := name, envelope
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.MarshalIndent(envelope, "", "  ")
			if err != nil {
				t.Fatalf("MarshalIndent() error = %v", err)
			}
			encoded = append(encoded, '\n')
			golden, err := os.ReadFile(filepath.Join("testdata", name+".golden"))
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if string(encoded) != string(golden) {
				t.Fatalf("envelope mismatch\n--- got ---\n%s--- want ---\n%s", encoded, golden)
			}
		})
	}
}

func TestUnmarshalAllowsUnknownFields(t *testing.T) {
	t.Parallel()

	input := []byte(`{
		"version":"memora.result/v1","request_id":"req-1","ok":true,
		"results":[{"index":0,"statement":"SELECT","source":"SELECT 1","status":"succeeded","columns":[],"rows":[],"affected_rows":0,"truncated":false,"warnings":[],"future_result":true}],
		"truncated":false,"warnings":[],"future_top_level":{"enabled":true}
	}`)
	var envelope Envelope
	if err := json.Unmarshal(input, &envelope); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if envelope.RequestID != "req-1" || len(envelope.Results) != 1 {
		t.Fatalf("Unmarshal() = %#v", envelope)
	}
}

func TestMarshalRejectsUnregisteredErrorCode(t *testing.T) {
	t.Parallel()

	envelope := NewEnvelope("req-1", FailedStatement(0, "SELECT", "SELECT 1", Code("made_up"), "bad", false))
	if _, err := json.Marshal(envelope); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("Marshal() error = %v, want %v", err, ErrInvalidEnvelope)
	}
}

func TestRequestFailureHasStableCodeAndNoStatementResults(t *testing.T) {
	t.Parallel()

	envelope := FailedRequest("req-1", CodeInvalidRequest, "request payload is empty", false)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["ok"] != false || decoded["error"].(map[string]any)["code"] != string(CodeInvalidRequest) {
		t.Fatalf("request failure = %s", encoded)
	}
}
