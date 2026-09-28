package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/embedding"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

// The Skill tells the agent to send its writes through `memora mutate` when it
// can, so that path owes the host's half of the vector path exactly as `exec`
// does: without a drain the recommended route leaves every unit without a
// vector while the receipt says the write succeeded.
//
// The command runs for real — a daemon in this process for the handshake, the
// real environment lookup and the real HTTP provider client — and only the
// daemon round trip is a stub, so what is asserted is the CLI's own behaviour.
func TestMutateDrainsPendingVectorsLikeExec(t *testing.T) {
	dataDir := t.TempDir()
	serveInstance(t, dataDir, ipc.EngineProtocol)

	embedded := []string{}
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("provider request: %v", err)
		}
		embedded = append(embedded, body.Input...)
		data := make([]map[string]any, 0, len(body.Input))
		for index := range body.Input {
			vector := make([]float32, 1024)
			vector[0] = 1
			data = append(data, map[string]any{"index": index, "embedding": vector})
		}
		_ = json.NewEncoder(writer).Encode(map[string]any{"data": data})
	}))
	defer provider.Close()

	const (
		preflight = "SELECT row_id FROM work.notes LIMIT 1"
		step      = "INSERT INTO work.notes (title) VALUES ('x')"
		verify    = "SELECT title FROM work.notes LIMIT 1"
	)
	plan := fmt.Sprintf(`{
	  "version": "memora.mutation-plan/v1",
	  "id": "plan-1",
	  "decision": "INSERT",
	  "database": "work",
	  "table": "notes",
	  "actor": "agent:host",
	  "source_event_id": "conversation:event-1",
	  "reason": "record the decision",
	  "authorized_databases": ["work"],
	  "preflight": [{"id": "dedupe", "msql": %q, "expect_rows": 0}],
	  "steps": [{"id": "insert", "kind": "INSERT", "target": "work.notes", "msql": %q,
	             "input": {"mutation": {"expected_schema_version": 1, "max_affected_rows": 1,
	                        "actor": "agent:host", "source": "conversation:event-1",
	                        "reason": "record the decision", "route_leaf_ids": ["leaf-1"]}}}],
	  "verify": [{"id": "read-back", "msql": %q, "expect_rows": 1}]
	}`, preflight, step, verify)

	type call struct {
		source     string
		readOnly   bool
		statements []executor.StatementInput
	}
	calls := []call{}
	pending := true
	execute := func(_ context.Context, _, source string, statements []executor.StatementInput, readOnly bool) (result.Envelope, error) {
		calls = append(calls, call{source: source, readOnly: readOnly, statements: statements})
		switch {
		case strings.HasPrefix(source, "SHOW PENDING VECTORS"):
			statement := result.NewStatement(0, "SHOW", source)
			if pending {
				pending = false
				statement.Rows = []result.Row{{
					"unit_no": int64(7), "table": "notes",
					"content_hash": "sha256:one", "payload": "storage engine",
				}}
			}
			return result.NewEnvelope("test", statement), nil
		case strings.Contains(source, "ACCEPT VECTOR"):
			return result.NewEnvelope("test", result.NewStatement(0, "ACCEPT", source)), nil
		case source == preflight:
			return result.NewEnvelope("test", result.NewStatement(0, "SELECT", source)), nil
		case source == verify:
			statement := result.NewStatement(0, "SELECT", source)
			statement.Rows = []result.Row{{"title": "x"}}
			return result.NewEnvelope("test", statement), nil
		case source == step:
			statement := result.NewStatement(0, "INSERT", source)
			statement.AffectedRows = 1
			return result.NewEnvelope("test", statement), nil
		default:
			t.Errorf("unexpected request %q", source)
			return result.NewEnvelope("test", result.NewStatement(0, "UNKNOWN", source)), nil
		}
	}

	providerEnv := map[string]string{
		embedding.EnvBaseURL:    provider.URL,
		embedding.EnvModel:      "text-embedding-v4",
		embedding.EnvDimensions: "1024",
		embedding.EnvAPIKey:     "test-key",
	}
	dependencies := Dependencies{
		ExecuteMSQL: execute,
		LookupEnv:   func(name string) (string, bool) { value, ok := providerEnv[name]; return value, ok },
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := RunWithDependencies(
		[]string{"mutate", "--data-dir", dataDir, "--plan", plan},
		stdout, stderr, BuildInfo{}, dependencies)

	if code != ExitOK {
		t.Fatalf("mutate exit = %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout.String(), `"status":"committed"`) {
		t.Fatalf("the receipt must say the plan committed: %s", stdout)
	}

	asked, offered := false, false
	for _, one := range calls {
		if strings.HasPrefix(one.source, "SHOW PENDING VECTORS IN DATABASE work") {
			asked = true
			if !one.readOnly {
				t.Errorf("the work list is a read: %q asked with readOnly=false", one.source)
			}
		}
		if strings.Contains(one.source, "ACCEPT VECTOR") {
			offered = true
			if len(one.statements) != 1 {
				t.Fatalf("one unit, one offer: %+v", one.statements)
			}
			authorization := one.statements[0].Authorization
			if authorization.Actor != "agent:host" ||
				len(authorization.AuthorizedDatabases) != 1 || authorization.AuthorizedDatabases[0] != "work" {
				t.Errorf("the drain must travel under the plan's own scope: %+v", authorization)
			}
			if named := one.statements[0].Parameters.Named; named["unit"] != int64(7) ||
				named["model"] != "text-embedding-v4" || named["hash"] != "sha256:one" {
				t.Errorf("the offer must carry the unit, model and text it embedded: %v", named)
			}
		}
	}
	if !asked || !offered {
		t.Fatalf("mutate did not drain (asked=%v offered=%v): %+v", asked, offered, calls)
	}
	if len(embedded) != 1 || embedded[0] != "storage engine" {
		t.Fatalf("the host must embed the payload the engine handed over: %v", embedded)
	}
	if !strings.Contains(stderr.String(), "attached 1") {
		t.Fatalf("the drain must report what it did: %q", stderr.String())
	}
}

// A host with no provider must not be asked to do anything: the plan commits,
// nothing is drained, and no work list is even read.
func TestMutateWithoutAProviderDoesNotDrain(t *testing.T) {
	dataDir := t.TempDir()
	serveInstance(t, dataDir, ipc.EngineProtocol)

	const (
		preflight = "SELECT row_id FROM work.notes LIMIT 1"
		step      = "INSERT INTO work.notes (title) VALUES ('x')"
		verify    = "SELECT title FROM work.notes LIMIT 1"
	)
	plan := fmt.Sprintf(`{
	  "version": "memora.mutation-plan/v1",
	  "id": "plan-2", "decision": "INSERT", "database": "work", "table": "notes",
	  "actor": "agent:host", "source_event_id": "conversation:event-2",
	  "reason": "record the decision", "authorized_databases": ["work"],
	  "preflight": [{"id": "dedupe", "msql": %q, "expect_rows": 0}],
	  "steps": [{"id": "insert", "kind": "INSERT", "target": "work.notes", "msql": %q,
	             "input": {"mutation": {"expected_schema_version": 1, "max_affected_rows": 1,
	                        "actor": "agent:host", "source": "conversation:event-2",
	                        "reason": "record the decision", "route_leaf_ids": ["leaf-1"]}}}],
	  "verify": [{"id": "read-back", "msql": %q, "expect_rows": 1}]
	}`, preflight, step, verify)

	sources := []string{}
	execute := func(_ context.Context, _, source string, _ []executor.StatementInput, _ bool) (result.Envelope, error) {
		sources = append(sources, source)
		statement := result.NewStatement(0, "MSQL", source)
		if source == step {
			statement.AffectedRows = 1
		}
		if source == verify {
			statement.Rows = []result.Row{{"title": "x"}}
		}
		return result.NewEnvelope("test", statement), nil
	}
	dependencies := Dependencies{
		ExecuteMSQL: execute,
		LookupEnv:   func(string) (string, bool) { return "", false },
	}
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	code := RunWithDependencies(
		[]string{"mutate", "--data-dir", dataDir, "--plan", plan},
		stdout, stderr, BuildInfo{}, dependencies)

	if code != ExitOK {
		t.Fatalf("mutate exit = %d\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	for _, source := range sources {
		if strings.Contains(source, "PENDING VECTORS") || strings.Contains(source, "ACCEPT VECTOR") {
			t.Fatalf("a host without a provider has no half of the vector path to run: %q", source)
		}
	}
}
