package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/embedding"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/recall"
	"github.com/HW-Yue/Memora/internal/result"
)

// The host's half of the vector path, run after a write.
//
// The engine records that a unit has no vector and hands over the text; a host
// with a provider embeds it and offers the vector back. That split is what keeps
// the engine offline and the provider the host's business — and it means this
// file is the only place a CLI run could ever reach the network.
const (
	// drainBatch is one round trip's worth of units. It is the statement LIMIT,
	// so it stays inside the engine's own bound of 1000.
	drainBatch = 64
	// drainMaxPages bounds one run: a backlog larger than this keeps its place in
	// the work list for the next write to pick up, rather than holding the
	// caller's command open.
	drainMaxPages = 16
)

// drainEmbeddings attaches vectors for the units that are missing them in the
// databases this request was authorized for.
//
// It never fails the caller's write. The data is committed before this runs, and
// an embedding that did not arrive is a unit that stays not-ready — visible in
// RECALL's notice and in doctor — not a reason to unwind a fact the user asked
// to record. The error it returns is for reporting, not for the exit code.
func drainEmbeddings(
	ctx context.Context,
	dataDir string,
	caller executor.StatementInput,
	execute ExecuteMSQL,
	embedder embedding.Embedder,
	batch int,
	stderr io.Writer,
) int {
	attached := 0
	for _, database := range caller.Authorization.AuthorizedDatabases {
		attached += drainDatabase(ctx, dataDir, caller, database, execute, embedder, batch, stderr)
	}
	if attached > 0 {
		_, _ = fmt.Fprintf(stderr, "embeddings: attached %d vector(s)\n", attached)
	}
	return attached
}

func drainDatabase(
	ctx context.Context,
	dataDir string,
	caller executor.StatementInput,
	database string,
	execute ExecuteMSQL,
	embedder embedding.Embedder,
	batch int,
	stderr io.Writer,
) int {
	attached := 0
	for page := 0; page < drainMaxPages; page++ {
		rows, err := pendingPage(ctx, dataDir, caller, database, execute)
		if err != nil {
			// The units stay not-ready and the next run picks them up; saying so
			// is the whole point of not pretending the work was done.
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): %v; the rest stay not-ready\n", attached, err)
			return attached
		}
		if len(rows) == 0 {
			return attached
		}
		texts := make([]string, 0, len(rows))
		for _, row := range rows {
			texts = append(texts, rowText(row["payload"]))
		}
		vectors, refused, err := embedTexts(ctx, embedder, texts, batch)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): %v; the rest stay not-ready\n", attached, err)
			return attached
		}
		for _, index := range refused {
			_, _ = fmt.Fprintf(stderr,
				"embeddings: unit %v stays not-ready: the provider refuses that text at any size\n", rows[index]["unit_no"])
		}
		kept, keptVectors := withoutRefused(rows, vectors, refused)
		if len(kept) == 0 {
			// Nothing in this page can be embedded, and the page is at the head of
			// the work list: retrying would ask the same question forever.
			_, _ = fmt.Fprintf(stderr,
				"embeddings: nothing in this page could be embedded; %d unit(s) stay not-ready\n", len(rows))
			return attached
		}
		if err := attachVectors(ctx, dataDir, caller, database, kept, keptVectors, embedder.Model(), execute); err != nil {
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): %v; the rest stay not-ready\n", attached, err)
			return attached
		}
		attached += len(kept)
		if len(rows) < drainBatch {
			return attached
		}
	}
	return attached
}

// embedTexts embeds one page, finding a request size the provider accepts.
//
// A provider with a batch ceiling answers 4xx instead of truncating, so a host
// that gives up — or retries the same size — can never drain a backlog larger
// than that ceiling. Splitting the request in half turns one refused request
// into log2(n) accepted ones; a single text the provider still refuses is left
// out and reported, not retried forever.
//
// A failure that is not a refusal (the provider is unreachable, or answered 5xx)
// is returned as an error: splitting would only multiply that failure.
func embedTexts(
	ctx context.Context,
	embedder embedding.Embedder,
	texts []string,
	batch int,
) (vectors [][]float32, refused []int, err error) {
	vectors = make([][]float32, len(texts))
	if len(texts) == 0 {
		return vectors, nil, nil
	}
	if batch <= 0 || batch > len(texts) {
		batch = len(texts)
	}
	for start := 0; start < len(texts); start += batch {
		end := min(start+batch, len(texts))
		if err := embedInto(ctx, embedder, texts[start:end], vectors, start, &refused); err != nil {
			return nil, nil, err
		}
	}
	return vectors, refused, nil
}

func embedInto(
	ctx context.Context,
	embedder embedding.Embedder,
	texts []string,
	vectors [][]float32,
	base int,
	refused *[]int,
) error {
	found, err := embedder.Embed(ctx, texts)
	if err == nil {
		if len(found) != len(texts) {
			return fmt.Errorf("provider returned %d vectors for %d units", len(found), len(texts))
		}
		for index, vector := range found {
			vectors[base+index] = vector
		}
		return nil
	}
	var status *embedding.StatusError
	if !errors.As(err, &status) || status.StatusCode < 400 || status.StatusCode >= 500 {
		return err
	}
	if len(texts) == 1 {
		*refused = append(*refused, base)
		return nil
	}
	half := len(texts) / 2
	if err := embedInto(ctx, embedder, texts[:half], vectors, base, refused); err != nil {
		return err
	}
	return embedInto(ctx, embedder, texts[half:], vectors, base+half, refused)
}

// withoutRefused drops the units the provider refused, keeping rows and vectors
// aligned: a refused unit is neither offered nor counted as attached.
func withoutRefused(rows []result.Row, vectors [][]float32, refused []int) ([]result.Row, [][]float32) {
	if len(refused) == 0 {
		return rows, vectors
	}
	drop := map[int]bool{}
	for _, index := range refused {
		drop[index] = true
	}
	keptRows := make([]result.Row, 0, len(rows))
	keptVectors := make([][]float32, 0, len(rows))
	for index, row := range rows {
		if drop[index] {
			continue
		}
		keptRows = append(keptRows, row)
		keptVectors = append(keptVectors, vectors[index])
	}
	return keptRows, keptVectors
}

// ExecuteMSQL is the daemon round trip the CLI already depends on; the drain
// uses the same one, so a test can watch the whole exchange without a daemon.
// ExecuteMSQL is the daemon round trip. readOnly asks the daemon to hold the
// request to the read policy — the client does not decide that for itself.
type ExecuteMSQL func(context.Context, string, string, []executor.StatementInput, bool) (result.Envelope, error)

func pendingPage(
	ctx context.Context,
	dataDir string,
	caller executor.StatementInput,
	database string,
	execute ExecuteMSQL,
) ([]result.Row, error) {
	read := caller
	read.Mutation = executor.MutationOptions{}
	read.Parameters = executor.Parameters{Named: map[string]any{"limit": drainBatch}}
	envelope, err := execute(ctx, dataDir,
		"SHOW PENDING VECTORS IN DATABASE "+database+" LIMIT :limit",
		[]executor.StatementInput{read}, true)
	if err != nil {
		return nil, requestError(err)
	}
	if !envelope.OK || len(envelope.Results) == 0 {
		return nil, statementError(envelope)
	}
	return envelope.Results[0].Rows, nil
}

func attachVectors(
	ctx context.Context,
	dataDir string,
	caller executor.StatementInput,
	database string,
	rows []result.Row,
	vectors [][]float32,
	model string,
	execute ExecuteMSQL,
) error {
	sources := make([]string, 0, len(rows))
	inputs := make([]executor.StatementInput, 0, len(rows))
	for index, row := range rows {
		encoded, err := recall.EncodeVector(vectors[index])
		if err != nil {
			return err
		}
		sources = append(sources,
			"ACCEPT VECTOR :v FOR UNIT :unit IN DATABASE "+database+" MODEL :model HASH :hash")
		write := caller
		write.Parameters = executor.Parameters{Named: map[string]any{
			"v": encoded, "unit": row["unit_no"], "model": model,
			"hash": rowText(row["content_hash"]),
		}}
		write.Mutation.MaxAffectedRows = 1
		if write.Mutation.Source == "" {
			write.Mutation.Source = "embedding:drain"
		}
		write.Mutation.Reason = "attach the embedding a host computed for this unit"
		inputs = append(inputs, write)
	}
	// One request, one transaction: a batch of statements is how the language
	// carries several embeddings, since it has no array type.
	envelope, err := execute(ctx, dataDir, strings.Join(sources, "; "), inputs, false)
	if err != nil {
		return requestError(err)
	}
	if !envelope.OK {
		return statementError(envelope)
	}
	return nil
}

func requestError(err error) error {
	return fmt.Errorf("call the instance: %w", err)
}

// statementError surfaces the refusal by name, so a host can see what the engine
// would not take rather than being told only that something failed.
func statementError(envelope result.Envelope) error {
	if envelope.Error != nil {
		return fmt.Errorf("%s: %s", envelope.Error.Code, envelope.Error.Message)
	}
	for _, statement := range envelope.Results {
		if statement.Error != nil {
			return fmt.Errorf("%s: %s", statement.Error.Code, statement.Error.Message)
		}
	}
	return errors.New("the instance refused the request")
}

// rowText reads a value the engine sent, which is a string for every column the
// work list publishes.
func rowText(value any) string {
	text, _ := value.(string)
	return text
}
