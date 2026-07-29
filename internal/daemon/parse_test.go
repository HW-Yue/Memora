package daemon

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/HW-Yue/Memora/internal/ipc"
)

func TestParseHandlerReturnsVersionedASTAndPreciseErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		ok     bool
		code   string
	}{
		{"success", "SHOW DATABASES; SELECT * FROM notes", true, ""},
		{"failure", "SELECT * notes", false, "unexpected_token"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			payload, err := json.Marshal(map[string]string{"source": test.source})
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := handleRequest(context.Background(), ipc.Session{ID: "session-test"}, ipc.Request{Method: "msql.parse", Payload: payload})
			if err != nil {
				t.Fatalf("handleRequest() error = %v", err)
			}
			var response ParseResponse
			if err := json.Unmarshal(encoded, &response); err != nil {
				t.Fatalf("Unmarshal() error = %v", err)
			}
			if response.OK != test.ok {
				t.Fatalf("response = %#v", response)
			}
			if test.ok {
				if response.AST == nil || response.AST.Version == "" || len(response.AST.Statements) != 2 {
					t.Fatalf("response AST = %#v", response.AST)
				}
			} else if response.Error == nil || response.Error.Code != test.code || response.Error.StatementIndex == nil || *response.Error.StatementIndex != 0 {
				t.Fatalf("response error = %#v", response.Error)
			}
		})
	}
}

func TestParseHandlerRejectsMalformedPayload(t *testing.T) {
	t.Parallel()

	if _, err := handleRequest(context.Background(), ipc.Session{}, ipc.Request{Method: "msql.parse", Payload: json.RawMessage(`{`)}); err == nil {
		t.Fatal("handleRequest() succeeded for malformed payload")
	}
}
