package pagestoremigration

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestLexicalRebuildReportsCanonicalSnapshotParity(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "All durable notes", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "storage", Kind: router.KindLeaf, Purpose: "Storage decisions",
	})
	if err != nil {
		t.Fatal(err)
	}
	beforeGeneration := authority.marker.Generation
	receipt, err := authority.ReplaceGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PreviousGeneration != beforeGeneration || receipt.Generation == beforeGeneration ||
		receipt.SourceFingerprint == "" || receipt.PreviousSnapshotSHA256 == "" ||
		receipt.SnapshotSHA256 == "" || receipt.PreviousSnapshotSHA256 != receipt.SnapshotSHA256 ||
		!receipt.Parity || !receipt.Verified {
		t.Fatalf("rebuild receipt = %#v", receipt)
	}
	assertRoutePosting(t, authority, "storage", leaf.ID, "name", leaf.Revision)

	second, err := authority.ReplaceGeneration(ctx)
	if err != nil || second.SnapshotSHA256 != receipt.SnapshotSHA256 || !second.Parity || !second.Verified {
		t.Fatalf("second rebuild receipt = %#v, %v", second, err)
	}
}

func TestLexicalRebuildRepairsNativeTruthAheadOfOnlinePosting(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	legacyDictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	legacyRows := nativerow.NewService(nativerow.New(file), legacyDictionary, nativerow.ServiceOptions{})
	updated, err := legacyRows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
		"title": "rebuild repaired",
	}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision})
	if err != nil {
		t.Fatal(err)
	}
	assertNoRowPosting(t, authority, "repaired")
	receipt, err := authority.ReplaceGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PreviousSnapshotSHA256 == "" || receipt.SnapshotSHA256 == "" ||
		receipt.PreviousSnapshotSHA256 == receipt.SnapshotSHA256 || receipt.Parity || !receipt.Verified {
		t.Fatalf("repair receipt = %#v", receipt)
	}
	assertNoRowPosting(t, authority, "indexed")
	assertRowPosting(t, authority, "rebuild", updated.ID, updated.Revision)
	assertRowPosting(t, authority, "repaired", updated.ID, updated.Revision)
	got, err := rows.Get(ctx, "work", "notes", updated.ID)
	if err != nil || got.Revision != updated.Revision || got.Values["title"] != "rebuild repaired" {
		t.Fatalf("Row after rebuild = %#v, %v", got, err)
	}
}
