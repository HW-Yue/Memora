package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRouteAliasesReplaceLivePostingAndSurviveReopenThroughMSQL(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "runtime", Kind: router.KindLeaf, Purpose: "Agent runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := executor.New(dictionary, rows)
	first := executeRouteAliases(t, ctx, engine, leaf.ID, []any{"legacy loop"}, 1)
	if first.Revision == nil || *first.Revision != 2 {
		t.Fatalf("first alias revision = %#v", first)
	}
	second := executeRouteAliases(t, ctx, engine, leaf.ID, []any{"agent loop", "运行时"}, 2)
	if !reflect.DeepEqual(second.Rows[0]["aliases"], []string{"agent loop", "运行时"}) {
		t.Fatalf("replacement aliases = %#v", second)
	}
	assertNoRoutePostingForObject(t, authority, "legacy", leaf.ID)
	assertRoutePosting(t, authority, "agent", leaf.ID, "aliases", 3)
	assertRoutePosting(t, authority, "运行", leaf.ID, "aliases", 3)

	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedCatalog := nativecatalog.NewService(
		nativecatalog.New(reopenedFile), nativecatalog.ServiceOptions{Authority: reopened},
	)
	reopenedRows := nativerow.NewService(
		nativerow.New(reopenedFile), reopenedCatalog, nativerow.ServiceOptions{Authority: reopened},
	)
	document, err := parser.Parse("DESCRIBE ROUTE :route")
	if err != nil {
		t.Fatal(err)
	}
	described, err := executor.New(reopenedCatalog, reopenedRows).Execute(
		ctx, document.Statement,
		executor.Parameters{Named: map[string]any{"route": leaf.ID}}, executor.MutationOptions{},
	)
	if err != nil || len(described.Rows) != 1 ||
		!reflect.DeepEqual(described.Rows[0]["aliases"], []string{"agent loop", "运行时"}) ||
		described.Rows[0]["revision"] != uint64(3) {
		t.Fatalf("reopened aliases = %#v, %v", described, err)
	}
	assertNoRoutePostingForObject(t, reopened, "legacy", leaf.ID)
	assertRoutePosting(t, reopened, "agent", leaf.ID, "aliases", 3)
}

func TestRouteAliasPublicationFaultPoisonsThenReopenConverges(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All notes", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "runtime", Kind: router.KindLeaf, Purpose: "Agent runtime",
	})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err = rows.UpdateRouterAliases(ctx, leaf.ID, []string{"legacy loop"}, leaf.Revision)
	if err != nil {
		t.Fatal(err)
	}
	authority.checkpoint = func(phase authorityPhase) error {
		if phase == phaseRouteBodyCommitted {
			return errors.New("injected alias publication fault")
		}
		return nil
	}
	_, err = rows.UpdateRouterAliases(ctx, leaf.ID, []string{"agent loop"}, leaf.Revision)
	if !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("faulted alias update error = %v", err)
	}
	if _, err := authority.Capture(ctx); !errors.Is(err, ErrAuthorityPoisoned) {
		t.Fatalf("faulted alias update did not poison Authority: %v", err)
	}
	authority.checkpoint = nil
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	current, err := nativerouter.New(reopenedFile).Get(leaf.ID)
	if err != nil || current.Revision != 3 || !reflect.DeepEqual(current.Aliases, []string{"agent loop"}) {
		t.Fatalf("recovered alias Route = %#v, %v", current, err)
	}
	assertNoRoutePostingForObject(t, reopened, "legacy", leaf.ID)
	assertRoutePosting(t, reopened, "agent", leaf.ID, "aliases", 3)
}

func executeRouteAliases(
	t *testing.T,
	ctx context.Context,
	engine *executor.Engine,
	routeID string,
	aliases []any,
	revision uint64,
) executor.Output {
	t.Helper()
	document, err := parser.Parse("ALTER ROUTE :route SET ALIASES :aliases")
	if err != nil {
		t.Fatal(err)
	}
	output, err := engine.Execute(ctx, document.Statement, executor.Parameters{Named: map[string]any{
		"route": routeID, "aliases": aliases,
	}}, executor.MutationOptions{ExpectedRevision: revision, MaxAffectedRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	return output
}
