package pagestoremigration

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
)

// TestTheObjectsTreeHoldsEveryCatalogBody is E7 stage 4's build gate.
//
// The Catalog Tree was already on disk, but it only ever held Locators: every
// DescribeTable resolved a name to an ID and then went back to the record log
// for the bytes — through File.records, the resident map with no capacity and no
// eviction. Putting the bodies in the objects Tree is what removes that second
// hop, and with it the Catalog's last reason to touch the map.
//
// The bytes are the record log's own encoding, verbatim, positional order
// included: the reader sorts Columns by the order stored in the body, so a
// re-encoding would not merely be a translation, it would reorder the schema.
func TestTheObjectsTreeHoldsEveryCatalogBody(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	applier, err := NewApplier(reader, directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := OpenGeneration(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	defer generation.Close()

	want, err := nativecatalog.ObjectRecords(plan.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(want) == 0 {
		t.Fatal("the fixture Plan carries no Catalog")
	}
	for _, record := range want {
		stored, err := generation.Objects().Lookup(record.Kind, record.ID)
		if err != nil {
			t.Fatalf("Catalog object %q of kind %d is not in the objects Tree: %v",
				record.ID, record.Kind, err)
		}
		if stored.Revision != record.Revision || !bytes.Equal(stored.Body, record.Body) {
			t.Fatalf("objects Tree holds %q at revision %d, want %d verbatim",
				record.ID, stored.Revision, record.Revision)
		}
	}
	// Nothing else of those kinds: the Catalog owns them outright, so an extra
	// entry is an object no Locator points at.
	for _, kind := range nativecatalog.ObjectKinds {
		expected := 0
		for _, record := range want {
			if record.Kind == kind {
				expected++
			}
		}
		walked, err := generation.Objects().Page(kind, "", expected+1)
		if err != nil {
			t.Fatal(err)
		}
		if len(walked.Records) != expected || walked.Truncated {
			t.Fatalf("objects Tree holds %d records of kind %d, want %d",
				len(walked.Records), kind, expected)
		}
	}
}

// TestACatalogPublicationMovesBothTreesTogether is the write gate.
//
// The reader treats a Locator naming an object the objects Tree does not hold as
// corruption rather than a race, and it is entitled to: the two go in as one
// group commit. This checks the claim from the outside — publish a schema
// change, then read it back through the Authority's own Catalog reader, which
// has no handle on the record log to fall back to.
func TestACatalogPublicationMovesBothTreesTogether(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)

	before, err := authority.DescribeTable(ctx, "work", table.Name)
	if err != nil {
		t.Fatal(err)
	}
	databases, err := authority.SnapshotCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A new Column, published the way a schema change publishes one.
	added := catalog.Column{
		ID: "col_added", Name: "added", Aliases: []string{}, Type: "TEXT",
		MaxCharacters: 50, Nullable: true, Purpose: "Added", SchemaVersion: 1,
		CreatedAt: before.CreatedAt, UpdatedAt: before.UpdatedAt,
	}
	next := editTable(databases, table.ID, func(target *catalog.Table) {
		target.Columns = append(target.Columns, added)
	})
	if err := authority.PublishCatalog(ctx, next, func() error {
		return nativecatalog.New(file).Write(next)
	}); err != nil {
		t.Fatal(err)
	}

	after, err := authority.DescribeTable(ctx, "work", table.Name)
	if err != nil {
		t.Fatalf("DescribeTable after the publication: %v", err)
	}
	if len(after.Columns) != len(before.Columns)+1 ||
		after.Columns[len(after.Columns)-1].ID != added.ID {
		t.Fatalf("Columns after the publication = %#v", after.Columns)
	}
	// The body in the Tree is at the revision the Catalog Tree's Locator names,
	// which is the agreement the reader checks on every read.
	stored, err := authority.Generation().Objects().Lookup(
		uint16(nativestore.ObjectKindTable), table.ID,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Revision != after.SchemaVersion {
		t.Fatalf("objects Tree holds Table revision %d, Catalog says %d",
			stored.Revision, after.SchemaVersion)
	}
}

// TestDroppingACatalogObjectRemovesItsBody covers the direction an append-only
// index gets wrong. DROP COLUMN is a real statement, so a Column that leaves the
// Catalog has to leave the objects Tree with it — otherwise the Tree keeps
// bodies nothing points at, and a re-created ID finds a stranger waiting.
func TestDroppingACatalogObjectRemovesItsBody(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	databases, err := authority.SnapshotCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Add the Column first, so the drop removes something this test watched
	// arrive rather than something the fixture happened to carry.
	const dropped = "col_temporary"
	added := catalog.Column{
		ID: dropped, Name: "temporary", Aliases: []string{}, Type: "TEXT",
		MaxCharacters: 50, Nullable: true, Purpose: "Temporary", SchemaVersion: 1,
		CreatedAt: table.CreatedAt, UpdatedAt: table.UpdatedAt,
	}
	withColumn := editTable(databases, table.ID, func(target *catalog.Table) {
		target.Columns = append(target.Columns, added)
	})
	if err := authority.PublishCatalog(ctx, withColumn, func() error {
		return nativecatalog.New(file).Write(withColumn)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Generation().Objects().Lookup(
		uint16(nativestore.ObjectKindColumn), dropped,
	); err != nil {
		t.Fatalf("the added Column has no body in the objects Tree: %v", err)
	}

	withoutColumn := editTable(withColumn, table.ID, func(target *catalog.Table) {
		target.Columns = target.Columns[:len(target.Columns)-1]
	})
	if err := authority.PublishCatalog(ctx, withoutColumn, func() error {
		return nativecatalog.New(file).Write(withoutColumn)
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Generation().Objects().Lookup(
		uint16(nativestore.ObjectKindColumn), dropped,
	); !errors.Is(err, objectindex.ErrNotFound) {
		t.Fatalf("the dropped Column %q is still in the objects Tree: %v", dropped, err)
	}
	// And the Catalog agrees, which is what makes the removal a fact rather
	// than a detail of one Tree.
	after, err := authority.DescribeTable(ctx, "work", table.Name)
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range after.Columns {
		if column.ID == dropped {
			t.Fatalf("DescribeTable still reports the dropped Column %q", dropped)
		}
	}
}

// editTable copies the Catalog, applies edit to the named Table, and advances
// that Table's schema version — the shape every Catalog publication takes.
func editTable(
	databases []catalog.Database, tableID string, edit func(*catalog.Table),
) []catalog.Database {
	next := cloneDatabases(databases)
	for databaseIndex := range next {
		for tableIndex := range next[databaseIndex].Tables {
			target := &next[databaseIndex].Tables[tableIndex]
			if target.ID != tableID {
				continue
			}
			edit(target)
			target.SchemaVersion++
		}
	}
	return next
}

func cloneDatabases(databases []catalog.Database) []catalog.Database {
	result := make([]catalog.Database, 0, len(databases))
	for _, database := range databases {
		next := database
		next.Tables = make([]catalog.Table, 0, len(database.Tables))
		for _, table := range database.Tables {
			copied := table
			copied.Columns = append([]catalog.Column(nil), table.Columns...)
			next.Tables = append(next.Tables, copied)
		}
		result = append(result, next)
	}
	return result
}
