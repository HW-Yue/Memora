package pagestoremigration

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestAuthorityLogicalMutationsPublishCompleteOrderedChangeEnvelopes(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(40)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := nativerow.NewService(
		nativerow.NewWithObjects(file, authority), dictionary, nativerow.ServiceOptions{Authority: authority},
	)
	root, err := rows.CreateTableRouterRoot(ctx, "work", "notes", "Notes Router", "")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := rows.CreateRouterNode(ctx, root.ID, router.NodeDefinition{
		Name: "storage", Kind: router.KindLeaf, Purpose: "Storage notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "first"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, RouteLeafIDs: []string{leaf.ID},
		Metadata: row.WriteMetadata{Actor: "agent:test", Source: "msql", Reason: "remember"},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "second"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Relate(ctx, row.RelationDefinition{
		Source:      row.RelationEndpoint{Database: "work", Table: "notes", RowID: first.ID},
		Type:        "supports",
		Target:      row.RelationEndpoint{Database: "work", Table: "notes", RowID: second.ID},
		Description: "first supports second",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := rows.Update(ctx, "work", "notes", first.ID, map[string]any{"title": "revised"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: first.Revision,
		RouteLeafIDs: []string{leaf.ID},
		Metadata:     row.WriteMetadata{Actor: "agent:test", Source: "feedback", Reason: "correct"},
	}); err != nil {
		t.Fatal(err)
	}
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	release, err := authority.BeginWrite(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changeSequence, err := authority.NextChangeSequence(ctx)
	if err == nil {
		budgets := nativeconfig.Defaults()
		budgets.SelectRows = 5
		_, err = configuration.UpdateCommitted(budgets, 1, "agent:test", "bound result", changeSequence)
	}
	release()
	if err != nil {
		t.Fatal(err)
	}

	changes, more, err := nativechange.New(file).ListAfter(0, 20)
	if err != nil || more || len(changes) != 9 {
		t.Fatalf("change timeline = %+v, %v, %v", changes, more, err)
	}
	for index, envelope := range changes {
		if envelope.CommitSequence != uint64(index+1) || envelope.Validate() != nil {
			t.Fatalf("change %d = %+v", index, envelope)
		}
		for _, entry := range envelope.Entries {
			if entry.ObjectKind != change.ObjectRow {
				continue
			}
			// The locator names a revision, and the Table's history Tree is
			// where revisions live. It used to be asserted by reading a
			// per-Row History record; nothing writes one any more (E5 stage
			// 5), and asserting against it would pin the storage this stage
			// removed rather than the contract the entry carries.
			if entry.HistoryLocator != nativerow.HistoryRecordID(entry.ObjectID, entry.AfterRevision) {
				t.Fatalf("History locator %q does not name revision %d of %q",
					entry.HistoryLocator, entry.AfterRevision, entry.ObjectID)
			}
			history := authority.generation.HistoryFor(entry.TableID)
			if history == nil {
				t.Fatalf("Table %q has no history Tree", entry.TableID)
			}
			if _, err := history.ByRevision(entry.ObjectID, entry.AfterRevision); err != nil {
				t.Fatalf("history Tree is missing revision %d of %q: %v",
					entry.AfterRevision, entry.ObjectID, err)
			}
		}
	}
	if len(changes[1].Entries) != 3 ||
		countChangeKind(changes[1], change.ObjectDatabase) != 1 ||
		countChangeKind(changes[1], change.ObjectTable) != 1 ||
		countChangeKind(changes[1], change.ObjectColumn) != 1 {
		t.Fatalf("CREATE TABLE envelope = %+v", changes[1])
	}
	// Mounting a Row on a leaf is a change to the leaf, so the envelope carries
	// a route_node entry where it used to carry a route_membership one. The
	// reader learns the same fact: this Row now hangs under that leaf.
	if changes[4].Actor != "agent:test" ||
		countChangeKind(changes[4], change.ObjectRow) != 1 ||
		countChangeKind(changes[4], change.ObjectRouteNode) != 1 ||
		countChangeKind(changes[4], change.ObjectRouteMembership) != 0 {
		t.Fatalf("Row + mount envelope = %+v", changes[4])
	}
	if countChangeKind(changes[6], change.ObjectRelation) != 1 ||
		countChangeKind(changes[8], change.ObjectConfiguration) != 1 {
		t.Fatalf("Relation/configuration envelopes = %+v / %+v", changes[6], changes[8])
	}

	if _, err := authority.ReplaceGeneration(ctx); err != nil {
		t.Fatalf("replacement with committed changes = %v", err)
	}
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
	reopenedChanges, more, err := nativechange.New(reopenedFile).ListAfter(0, 20)
	if err != nil || more || len(reopenedChanges) != len(changes) ||
		reopenedChanges[len(reopenedChanges)-1].Checksum != changes[len(changes)-1].Checksum {
		t.Fatalf("reopened change timeline = %+v, %v, %v", reopenedChanges, more, err)
	}
}

func TestAuthorityConcurrentMutationsSerializeChangeSequence(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	table, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(40)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := nativerow.NewService(
		nativerow.NewWithObjects(file, authority), dictionary, nativerow.ServiceOptions{Authority: authority},
	)
	const writers = 16
	start := make(chan struct{})
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			_, err := rows.Insert(ctx, "work", "notes", map[string]any{
				"title": fmt.Sprintf("note-%02d", index),
			}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
			errorsByWriter <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	for err := range errorsByWriter {
		if err != nil {
			t.Fatal(err)
		}
	}
	changes, more, err := nativechange.New(file).ListAfter(0, writers+2)
	if err != nil || more || len(changes) != writers+2 {
		t.Fatalf("concurrent changes = %d, %v, %v", len(changes), more, err)
	}
	for index, envelope := range changes {
		if envelope.CommitSequence != uint64(index+1) {
			t.Fatalf("change[%d] sequence = %d", index, envelope.CommitSequence)
		}
	}
}

func countChangeKind(envelope change.Envelope, kind change.ObjectKind) int {
	count := 0
	for _, entry := range envelope.Entries {
		if entry.ObjectKind == kind {
			count++
		}
	}
	return count
}
