//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestPhaseBExitTenThousandRowsRebuildRestartAndSnapshotHash(t *testing.T) {
	ctx := context.Background()
	encoded := phaseBSnapshot(t, 10_000)
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	databaseStore, err := sqlitestore.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.New(databaseStore).Import(ctx, encoded); err != nil {
		t.Fatal(err)
	}
	exported, err := snapshot.New(databaseStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sourceHash, err := snapshot.CanonicalHash(exported)
	if err != nil {
		t.Fatal(err)
	}
	targetStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "target.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.New(targetStore).Import(ctx, exported); err != nil {
		t.Fatal(err)
	}
	reexported, err := snapshot.New(targetStore).Export(ctx)
	if err != nil {
		t.Fatal(err)
	}
	targetHash, err := snapshot.CanonicalHash(reexported)
	if err != nil {
		t.Fatal(err)
	}
	if sourceHash != targetHash {
		t.Fatalf("snapshot round-trip hash = %s, want %s", targetHash, sourceHash)
	}
	if err := targetStore.Close(); err != nil {
		t.Fatal(err)
	}

	dictionary := catalog.New(databaseStore, catalog.Options{})
	service := row.New(databaseStore, dictionary, row.Options{
		IDs:    &phaseBIDs{values: []string{"seed", "after-import", "rolled-back"}},
		Router: router.Options{IDs: &phaseBIDs{values: []string{"root-1", "leaf-1", "root-2", "leaf-2"}}},
		Clock:  phaseBClock{},
	})
	page, truncated, err := service.ListPage(ctx, "work", "notes", 1000)
	if err != nil || !truncated || len(page) != 1000 || page[0].ID != "row_00000" {
		t.Fatalf("10k page = %d, truncated=%t, first=%#v, %v", len(page), truncated, page[0], err)
	}
	last, err := service.Get(ctx, "work", "notes", "row_09999")
	if err != nil || last.Values["title"] != "entry-09999" {
		t.Fatalf("last Row = %#v, %v", last, err)
	}

	root1, leaf1 := phaseBRoute(t, ctx, service, "db_work")
	seed, err := service.Insert(ctx, "work", "notes", map[string]any{
		"title": "seed index marker",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"seed-semantic"},
		RouteLeafIDs: []string{leaf1.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if postings, err := service.LookupAgentTerm(ctx, "db_work", "seed-semantic", 10); err != nil || len(postings) != 1 {
		t.Fatalf("seed Agent index = %#v, %v", postings, err)
	}
	if postings, err := service.LookupMechanicalTerm(ctx, "db_work", "seed", 10); err != nil || len(postings) != 1 {
		t.Fatalf("seed mechanical index = %#v, %v", postings, err)
	}
	if locators, _, err := service.ListRouterLeaf(ctx, leaf1.ID, 10); err != nil || len(locators) != 1 {
		t.Fatalf("seed Router index = %#v, %v", locators, err)
	}

	dropDerivedIndexes(t, ctx, databaseStore)
	if postings, err := service.LookupAgentTerm(ctx, "db_work", "seed-semantic", 10); err != nil || len(postings) != 0 {
		t.Fatalf("Agent index survived drop = %#v, %v", postings, err)
	}
	if postings, err := service.LookupMechanicalTerm(ctx, "db_work", "seed", 10); err != nil || len(postings) != 0 {
		t.Fatalf("mechanical index survived drop = %#v, %v", postings, err)
	}
	if _, err := service.GetRouterNode(ctx, root1.ID); err == nil {
		t.Fatal("Router tree survived drop")
	}

	_, leaf2 := phaseBRoute(t, ctx, service, "db_work")
	rebuilt, err := service.Update(ctx, "work", "notes", seed.ID, map[string]any{
		"title": "rebuilt exit marker",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := service.ClaimReindex(ctx, "phase-b", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.CompleteReindex(
		ctx, claim, []string{"rebuilt-semantic"}, []string{leaf2.ID},
	); err != nil {
		t.Fatal(err)
	}
	if err := service.RebuildMechanicalIndex(ctx, "work", "notes", seed.ID); err != nil {
		t.Fatal(err)
	}
	matched, err := service.Match(
		ctx, "db_work", "tbl_notes", "rebuilt exit marker",
		[]string{"rebuilt-semantic"}, 10,
	)
	if err != nil || len(matched.Matches) != 1 || matched.Matches[0].RowID != seed.ID {
		t.Fatalf("rebuilt MATCH = %#v, %v", matched, err)
	}
	if locators, _, err := service.ListRouterLeaf(ctx, leaf2.ID, 10); err != nil || len(locators) != 1 || locators[0].RowID != seed.ID {
		t.Fatalf("rebuilt Router = %#v, %v", locators, err)
	}

	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := transaction.Update(ctx, "work", "notes", "row_00001", map[string]any{
		"title": "transaction update",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		IndexTerms: []string{"transaction"}, RouteLeafIDs: []string{leaf2.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	inserted, err := transaction.Insert(ctx, "work", "notes", map[string]any{
		"title": "transaction insert",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, IndexTerms: []string{"transaction"},
		RouteLeafIDs: []string{leaf2.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.CommitSequence != inserted.CommitSequence {
		t.Fatalf("transaction sequences = %d, %d", updated.CommitSequence, inserted.CommitSequence)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := service.BeginTransaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rolledBack.Update(ctx, "work", "notes", "row_00002", map[string]any{
		"title": "must roll back",
	}, row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
		IndexTerms: []string{"rollback"}, RouteLeafIDs: []string{leaf2.ID},
	}); err != nil {
		t.Fatal(err)
	}
	if err := rolledBack.Rollback(); err != nil {
		t.Fatal(err)
	}
	unchanged, err := service.Get(ctx, "work", "notes", "row_00002")
	if err != nil || unchanged.Values["title"] != "entry-00002" || unchanged.Revision != 1 {
		t.Fatalf("rolled-back Row = %#v, %v", unchanged, err)
	}
	deleted, err := service.Delete(ctx, "work", "notes", "row_00003", row.WriteOptions{
		ExpectedSchemaVersion: 1, ExpectedRevision: 1,
	})
	if err != nil || deleted.State != row.StateDeleted {
		t.Fatalf("deleted Row = %#v, %v", deleted, err)
	}
	if records, err := service.History(ctx, "work", "notes", "row_00001"); err != nil || len(records) != 2 || records[1].Revision != 2 {
		t.Fatalf("updated History = %#v, %v", records, err)
	}

	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}
	databaseStore, err = sqlitestore.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	reopened := row.New(databaseStore, catalog.New(databaseStore, catalog.Options{}), row.Options{})
	persisted, err := reopened.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || persisted.Values["title"] != "transaction insert" {
		t.Fatalf("reopened inserted Row = %#v, %v", persisted, err)
	}
	if matches, err := reopened.Match(
		ctx, "db_work", "tbl_notes", "rebuilt exit marker",
		[]string{"rebuilt-semantic"}, 10,
	); err != nil || len(matches.Matches) != 1 || matches.Matches[0].RowID != rebuilt.ID {
		t.Fatalf("reopened MATCH = %#v, %v", matches, err)
	}
}

func phaseBSnapshot(t *testing.T, count int) []byte {
	t.Helper()
	timestamp := "2026-07-30T00:00:00Z"
	catalogValue := map[string]any{
		"version": "memora.catalog/v1",
		"databases": []any{map[string]any{
			"database_id": "db_work", "name": "work", "aliases": []any{},
			"purpose": "Phase B exit", "scope": "Fixed dataset",
			"schema_version": 1, "created_at": timestamp, "updated_at": timestamp,
			"tables": []any{map[string]any{
				"table_id": "tbl_notes", "database_id": "db_work",
				"name": "notes", "aliases": []any{}, "purpose": "Exit notes",
				"row_semantics": "One fixed note", "schema_version": 1,
				"created_at": timestamp, "updated_at": timestamp,
				"columns": []any{map[string]any{
					"column_id": "col_title", "name": "title", "aliases": []any{},
					"type": "TEXT", "max_characters": 2000, "nullable": false,
					"purpose": "Title", "schema_version": 1,
					"created_at": timestamp, "updated_at": timestamp,
				}},
			}},
		}},
	}
	rows := make([]any, 0, count)
	historyRows := make([]any, 0, count)
	for index := 0; index < count; index++ {
		rowID := fmt.Sprintf("row_%05d", index)
		title := fmt.Sprintf("entry-%05d", index)
		rows = append(rows, map[string]any{
			"row_id": rowID, "database_id": "db_work", "table_id": "tbl_notes",
			"schema_version": 1, "revision": 1, "commit_sequence": index + 1,
			"row_state": "live", "values": map[string]any{"col_title": title},
			"created_at": timestamp, "updated_at": timestamp,
		})
		historyRows = append(historyRows, map[string]any{
			"version":     "memora.history/v1",
			"database_id": "db_work", "table_id": "tbl_notes", "row_id": rowID,
			"schema_version": 1, "revision": 1, "commit_sequence": index + 1,
			"operation": "INSERT", "row_state": "live",
			"values": map[string]any{"col_title": title},
			"actor":  "phase-b:fixture", "source": "fixed-seed",
			"reason": "quality gate", "created_at": timestamp,
			"updated_at": timestamp, "recorded_at": timestamp,
		})
	}
	encoded, err := json.Marshal(map[string]any{
		"version": "memora.logical-snapshot/v1", "catalog": catalogValue,
		"rows": rows, "history": historyRows,
		"relations":       map[string]any{"current": []any{}, "versions": []any{}},
		"commit_sequence": count,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func phaseBRoute(
	t *testing.T,
	ctx context.Context,
	service *row.Service,
	databaseID string,
) (router.Node, router.Node) {
	t.Helper()
	root, err := service.CreateRouterRoot(ctx, databaseID, "Phase B root")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := service.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "exit", Kind: router.KindLeaf, Purpose: "Phase B exit rows",
	})
	if err != nil {
		t.Fatal(err)
	}
	return root, leaf
}

func dropDerivedIndexes(t *testing.T, ctx context.Context, database store.Store) {
	t.Helper()
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, bucket := range []string{
		"agent_index_snapshots", "agent_index_versions", "agent_index_postings",
		"mechanical_index_snapshots", "mechanical_index_versions", "mechanical_index_postings",
		"router_nodes", "router_paths", "router_children",
		"router_leaf_members", "router_row_memberships",
	} {
		entries, err := tx.Scan(ctx, bucket)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if err := tx.Delete(ctx, bucket, entry.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

type phaseBIDs struct {
	values []string
	index  int
}

func (source *phaseBIDs) Next() (string, error) {
	if source.index >= len(source.values) {
		return "", fmt.Errorf("phase B ID source exhausted")
	}
	value := source.values[source.index]
	source.index++
	return value, nil
}

type phaseBClock struct{}

func (phaseBClock) Now() time.Time {
	return time.Date(2026, 7, 30, 1, 0, 0, 0, time.UTC)
}
