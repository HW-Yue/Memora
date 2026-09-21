package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

// A host-side drain tested without a daemon and without a provider: the round
// trips are a stub and the embedder is deterministic, so this says exactly what
// the CLI asks for and what it does with the answers.
type stubEmbedder struct {
	model      string
	dimensions int
	fail       bool
	seen       []string
}

func (stub *stubEmbedder) Model() string   { return stub.model }
func (stub *stubEmbedder) Dimensions() int { return stub.dimensions }
func (stub *stubEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if stub.fail {
		return nil, errEmbedding
	}
	stub.seen = append(stub.seen, texts...)
	vectors := make([][]float32, 0, len(texts))
	for range texts {
		vector := make([]float32, stub.dimensions)
		vector[0] = 1
		vectors = append(vectors, vector)
	}
	return vectors, nil
}

var errEmbedding = &embeddingError{}

type embeddingError struct{}

func (*embeddingError) Error() string { return "provider is down" }

func callerInput() executor.StatementInput {
	return executor.StatementInput{
		Authorization: security.Authorization{
			Version: security.AuthorizationVersion, Actor: "agent:host",
			AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelWrite,
		},
		Mutation: executor.MutationOptions{Actor: "agent:host", Source: "conversation:event-9"},
	}
}

// The whole exchange: ask what is missing, embed the text the engine handed
// over, offer each vector back in one batch.
func TestDrainAttachesEveryPendingVector(t *testing.T) {
	pages := [][]result.Row{{
		{"unit_no": int64(7), "table": "notes", "content_hash": "sha256:one", "payload": "storage engine"},
		{"unit_no": int64(8), "table": "notes", "content_hash": "sha256:two", "payload": "write ahead log"},
	}}
	requests := []struct {
		source     string
		statements []executor.StatementInput
	}{}
	execute := func(_ context.Context, _, source string, statements []executor.StatementInput) (result.Envelope, error) {
		requests = append(requests, struct {
			source     string
			statements []executor.StatementInput
		}{source, statements})
		if strings.HasPrefix(source, "SHOW PENDING VECTORS") {
			if len(pages) == 0 {
				return successfulEnvelope(pageResult(source, nil)), nil
			}
			page := pages[0]
			pages = pages[1:]
			return successfulEnvelope(pageResult(source, page)), nil
		}
		return successfulEnvelope(pageResult(source, nil)), nil
	}
	embedder := &stubEmbedder{model: "text-embedding-v4", dimensions: 1024}
	stderr := &bytes.Buffer{}

	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute, embedder, stderr)
	if attached != 2 {
		t.Fatalf("attached = %d, want 2", attached)
	}
	if len(embedder.seen) != 2 || embedder.seen[0] != "storage engine" {
		t.Fatalf("the host must embed the payload the engine handed over: %v", embedder.seen)
	}
	// The first request asks for work, the second carries both vectors as a batch
	// of statements — the language has no array type.
	if len(requests) != 2 || !strings.Contains(requests[0].source, "SHOW PENDING VECTORS IN DATABASE work") {
		t.Fatalf("requests = %+v", requests)
	}
	batch := requests[1]
	if len(batch.statements) != 2 || strings.Count(batch.source, "ACCEPT VECTOR") != 2 {
		t.Fatalf("the batch = %q with %d statements", batch.source, len(batch.statements))
	}
	named := batch.statements[1].Parameters.Named
	if named["unit"] != int64(8) || named["model"] != "text-embedding-v4" || named["hash"] != "sha256:two" {
		t.Fatalf("the offer must carry the unit, the model and the text it embedded: %v", named)
	}
	if value, ok := named["v"].(string); !ok || value == "" {
		t.Fatalf("the offer must carry an encoded vector: %v", named)
	}
	if batch.statements[0].Mutation.MaxAffectedRows != 1 {
		t.Fatalf("one statement offers one vector: %+v", batch.statements[0].Mutation)
	}
	if !strings.Contains(stderr.String(), "attached 2") {
		t.Fatalf("the summary must be visible: %q", stderr.String())
	}
}

// A provider that is down must not stop the host from recording what the user
// asked for, and must not pretend the work was done either.
func TestDrainReportsAFailedProviderAndKeepsTheWorkPending(t *testing.T) {
	execute := func(_ context.Context, _, source string, _ []executor.StatementInput) (result.Envelope, error) {
		if strings.HasPrefix(source, "SHOW PENDING VECTORS") {
			return successfulEnvelope(pageResult(source, []result.Row{
				{"unit_no": int64(7), "table": "notes", "content_hash": "sha256:one", "payload": "storage engine"},
			})), nil
		}
		t.Fatal("nothing may be offered when the provider failed")
		return result.Envelope{}, nil
	}
	stderr := &bytes.Buffer{}
	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute,
		&stubEmbedder{model: "m", dimensions: 2, fail: true}, stderr)
	if attached != 0 {
		t.Fatalf("attached = %d", attached)
	}
	if !strings.Contains(stderr.String(), "stay not-ready") {
		t.Fatalf("the host must say the work is still pending: %q", stderr.String())
	}
}

func pageResult(source string, rows []result.Row) result.StatementResult {
	statement := result.NewStatement(0, "SHOW", source)
	statement.Rows = rows
	if statement.Rows == nil {
		statement.Rows = []result.Row{}
	}
	return statement
}

func successfulEnvelope(statements ...result.StatementResult) result.Envelope {
	if statements == nil {
		statements = []result.StatementResult{}
	}
	for index := range statements {
		statements[index].Status = result.StatusSucceeded
		if statements[index].Rows == nil {
			statements[index].Rows = []result.Row{}
		}
	}
	return result.Envelope{Version: result.Version, RequestID: "test", OK: true, Results: statements}
}
