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
	stderr io.Writer,
) int {
	attached := 0
	for _, database := range caller.Authorization.AuthorizedDatabases {
		attached += drainDatabase(ctx, dataDir, caller, database, execute, embedder, stderr)
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
		vectors, err := embedder.Embed(ctx, texts)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): %v; the rest stay not-ready\n", attached, err)
			return attached
		}
		if len(vectors) != len(rows) {
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): provider returned %d vectors for %d units\n",
				attached, len(vectors), len(rows))
			return attached
		}
		if err := attachVectors(ctx, dataDir, caller, database, rows, vectors, embedder.Model(), execute); err != nil {
			_, _ = fmt.Fprintf(stderr, "embeddings: stopped after %d vector(s): %v; the rest stay not-ready\n", attached, err)
			return attached
		}
		attached += len(rows)
		if len(rows) < drainBatch {
			return attached
		}
	}
	return attached
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
