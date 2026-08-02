package executor_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRebuildLexicalIndexReturnsVerifiedReceipt(t *testing.T) {
	maintenance := &fakeLexicalMaintenance{receipt: executor.LexicalRebuildReceipt{
		PreviousGeneration: "page-index-v1", Generation: "page-index-v1.g1", Epoch: 1,
		PlanDigest: "plan", SourceFingerprint: "source", PreviousSnapshotSHA256: "sha256:old",
		SnapshotSHA256: "sha256:new", Parity: false, Verified: true, Reused: false,
	}}
	statement := ast.Statement{Kind: "REBUILD_LEXICAL_INDEX", Rebuild: &ast.RebuildStatement{Object: "LEXICAL_INDEX"}}
	output, err := executor.NewWithLexicalIndexMaintenance(nil, nil, maintenance).Execute(
		context.Background(), statement, executor.Parameters{}, executor.MutationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if maintenance.calls != 1 || output.AffectedRows != 1 || len(output.Rows) != 1 ||
		output.Rows[0]["generation"] != "page-index-v1.g1" || output.Rows[0]["verified"] != true ||
		output.Rows[0]["parity"] != false {
		t.Fatalf("REBUILD output = %#v calls=%d", output, maintenance.calls)
	}
}

func TestRebuildLexicalIndexRequiresPortAndStructuralAuthorization(t *testing.T) {
	statement := ast.Statement{Kind: "REBUILD_LEXICAL_INDEX", Rebuild: &ast.RebuildStatement{Object: "LEXICAL_INDEX"}}
	if _, err := executor.New(nil, nil).Execute(
		context.Background(), statement, executor.Parameters{}, executor.MutationOptions{},
	); code(err) != string(result.CodeUnsupported) {
		t.Fatalf("missing maintenance error = %v", err)
	}
	readOnly := security.WithAuthorization(context.Background(), security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test", AuthorizedDatabases: []string{"work"},
		DefaultLevel: security.LevelRead,
	})
	if _, err := executor.NewWithLexicalIndexMaintenance(nil, nil, &fakeLexicalMaintenance{}).Execute(
		readOnly, statement, executor.Parameters{}, executor.MutationOptions{},
	); code(err) != string(result.CodePermissionDenied) {
		t.Fatalf("L0 maintenance error = %v", err)
	}
}

func TestRebuildLexicalIndexIsAutocommitOnly(t *testing.T) {
	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dictionary := catalog.New(database, catalog.Options{})
	rows := row.New(database, dictionary, row.Options{})
	maintenance := &fakeLexicalMaintenance{}
	session := executor.NewBatchSessionWithLexicalIndexMaintenance(ctx, dictionary, rows, maintenance)
	defer session.Close()
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "transactional-rebuild", Source: "BEGIN; REBUILD LEXICAL INDEX; ROLLBACK",
	})
	if len(envelope.Results) != 3 || envelope.Results[1].Error == nil ||
		envelope.Results[1].Error.Code != result.CodeUnsupported || maintenance.calls != 0 {
		t.Fatalf("transactional REBUILD = %#v calls=%d", envelope, maintenance.calls)
	}
}

type fakeLexicalMaintenance struct {
	receipt executor.LexicalRebuildReceipt
	calls   int
}

func (maintenance *fakeLexicalMaintenance) RebuildLexicalIndex(context.Context) (executor.LexicalRebuildReceipt, error) {
	maintenance.calls++
	return maintenance.receipt, nil
}
