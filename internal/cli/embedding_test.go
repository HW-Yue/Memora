package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/embedding"
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
	execute := func(_ context.Context, _, source string, statements []executor.StatementInput, readOnly bool) (result.Envelope, error) {
		// The work list is a read and must travel as one; the offers are writes.
		if strings.HasPrefix(source, "SHOW PENDING VECTORS") != readOnly {
			t.Errorf("read-only mode %v does not match %q", readOnly, source)
		}
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

	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute, embedder, 0, stderr)
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
	execute := func(_ context.Context, _, source string, _ []executor.StatementInput, _ bool) (result.Envelope, error) {
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
		&stubEmbedder{model: "m", dimensions: 2, fail: true}, 0, stderr)
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

// A provider with a batch ceiling is not a broken provider. Asking for a whole
// page at once and giving up when it says no leaves a backlog larger than the
// ceiling permanently unembedded — which is what happened to a 32-unit library
// against a provider that accepts ten at a time.
type ceilingEmbedder struct {
	stubEmbedder
	ceiling  int
	attempts []int
	accepted []int
}

func (stub *ceilingEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	stub.attempts = append(stub.attempts, len(texts))
	if len(texts) > stub.ceiling {
		return nil, fmt.Errorf("the embedding provider answered 400 Bad Request: %w",
			&embedding.StatusError{StatusCode: 400, Detail: "batch size is invalid, it should not be larger than 10."})
	}
	stub.accepted = append(stub.accepted, len(texts))
	return stub.stubEmbedder.Embed(ctx, texts)
}

func pendingRows(count int) []result.Row {
	rows := make([]result.Row, 0, count)
	for index := 1; index <= count; index++ {
		rows = append(rows, result.Row{
			"unit_no": int64(index), "table": "notes",
			"content_hash": "sha256:unit", "payload": "payload",
		})
	}
	return rows
}

// executePending serves one page and then an empty one, and counts the offers.
func executePending(t *testing.T, rows []result.Row, offers *[]executor.StatementInput) func(context.Context, string, string, []executor.StatementInput, bool) (result.Envelope, error) {
	served := false
	return func(_ context.Context, _, source string, statements []executor.StatementInput, _ bool) (result.Envelope, error) {
		if strings.HasPrefix(source, "SHOW PENDING VECTORS") {
			if served {
				return successfulEnvelope(pageResult(source, nil)), nil
			}
			served = true
			return successfulEnvelope(pageResult(source, rows)), nil
		}
		*offers = append(*offers, statements...)
		return successfulEnvelope(pageResult(source, nil)), nil
	}
}

func TestDrainSplitsABatchTheProviderRefuses(t *testing.T) {
	offers := []executor.StatementInput{}
	execute := executePending(t, pendingRows(24), &offers)
	embedder := &ceilingEmbedder{stubEmbedder: stubEmbedder{model: "m", dimensions: 2}, ceiling: 10}
	stderr := &bytes.Buffer{}

	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute, embedder, 0, stderr)
	if attached != 24 {
		t.Fatalf("attached = %d, want every unit in a refUsable batch: %q", attached, stderr.String())
	}
	if len(offers) != 24 {
		t.Fatalf("every unit must be offered once: %d", len(offers))
	}
	// The first request is the whole page and may be refused; what the provider
	// actually accepted must never exceed its ceiling, and the splitting must not
	// turn into a retry storm.
	for _, size := range embedder.accepted {
		if size > 10 {
			t.Fatalf("an accepted request exceeded the ceiling: %v", embedder.accepted)
		}
	}
	if len(embedder.attempts) > 4*16 {
		t.Fatalf("splitting must be logarithmic, not a retry storm: %d attempts", len(embedder.attempts))
	}
	if !strings.Contains(stderr.String(), "attached 24") {
		t.Fatalf("the summary must report the work done: %q", stderr.String())
	}
}

func TestDrainHonoursAConfiguredBatchSize(t *testing.T) {
	offers := []executor.StatementInput{}
	execute := executePending(t, pendingRows(24), &offers)
	embedder := &ceilingEmbedder{stubEmbedder: stubEmbedder{model: "m", dimensions: 2}, ceiling: 10}
	stderr := &bytes.Buffer{}

	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute, embedder, 10, stderr)
	if attached != 24 {
		t.Fatalf("attached = %d", attached)
	}
	for _, size := range embedder.attempts {
		if size > 10 {
			t.Fatalf("a configured batch size must be respected from the first request: %v", embedder.attempts)
		}
	}
}

// A single text the provider will not embed is not the whole backlog's problem:
// the rest must still be attached, and the one left out has to be named.
type poisonEmbedder struct {
	stubEmbedder
	poison string
}

func (stub *poisonEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	for _, text := range texts {
		if text == stub.poison {
			return nil, fmt.Errorf("the embedding provider answered 400 Bad Request: %w",
				&embedding.StatusError{StatusCode: 400, Detail: "input is too long"})
		}
	}
	return stub.stubEmbedder.Embed(context.Background(), texts)
}

func TestDrainLeavesOutOnlyTheTextTheProviderRefuses(t *testing.T) {
	rows := []result.Row{
		{"unit_no": int64(1), "table": "notes", "content_hash": "sha256:one", "payload": "fine"},
		{"unit_no": int64(2), "table": "notes", "content_hash": "sha256:two", "payload": "poison"},
		{"unit_no": int64(3), "table": "notes", "content_hash": "sha256:three", "payload": "also fine"},
	}
	offers := []executor.StatementInput{}
	execute := executePending(t, rows, &offers)
	embedder := &poisonEmbedder{stubEmbedder: stubEmbedder{model: "m", dimensions: 2}, poison: "poison"}
	stderr := &bytes.Buffer{}

	attached := drainEmbeddings(context.Background(), "/tmp/instance", callerInput(), execute, embedder, 0, stderr)
	if attached != 2 || len(offers) != 2 {
		t.Fatalf("attached = %d, offers = %d, want the two embeddable units", attached, len(offers))
	}
	for _, offer := range offers {
		if offer.Parameters.Named["unit"] == int64(2) {
			t.Fatal("a refused unit must not be offered")
		}
	}
	if !strings.Contains(stderr.String(), "unit 2 stays not-ready") {
		t.Fatalf("the refused unit must be named: %q", stderr.String())
	}
}
