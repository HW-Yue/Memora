package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestAuthorityChangeTimelinePinsSnapshotAndFiltersDatabase(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)
	work, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "private", Purpose: "Private", Scope: "Personal",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(40)", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}

	first, snapshot, more, err := authority.ListCommittedChanges(ctx, work.ID, 0, 0, 1)
	if err != nil || !more || len(first) != 1 || snapshot != 3 || first[0].CommitSequence != 1 {
		t.Fatalf("first Page = %+v, snapshot %d, more %v, %v", first, snapshot, more, err)
	}
	if _, err := dictionary.CreateTable(ctx, "private", catalog.TableDefinition{
		Name: "secrets", Purpose: "Secrets", RowSemantics: "One secret",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(40)", Purpose: "Title"}},
	}); err != nil {
		t.Fatal(err)
	}
	second, continuedSnapshot, more, err := authority.ListCommittedChanges(
		ctx, work.ID, first[0].CommitSequence, snapshot, 2,
	)
	if err != nil || more || continuedSnapshot != snapshot || len(second) != 1 || second[0].CommitSequence != 3 {
		t.Fatalf("continued Page = %+v, snapshot %d, more %v, %v", second, continuedSnapshot, more, err)
	}
	fresh, freshSnapshot, more, err := authority.ListCommittedChanges(ctx, "", 0, 0, 10)
	if err != nil || more || freshSnapshot != 4 || len(fresh) != 4 {
		t.Fatalf("fresh global Page = %d, snapshot %d, more %v, %v", len(fresh), freshSnapshot, more, err)
	}
}

func TestAuthorityChangeLookupUsesPageLocatorAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary := nativecatalog.NewService(
		nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
	)
	work, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	})
	if err != nil {
		t.Fatal(err)
	}
	private, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "private", Purpose: "Private", Scope: "Personal",
	})
	if err != nil {
		t.Fatal(err)
	}
	values, _, _, err := authority.ListCommittedChanges(ctx, work.ID, 0, 0, 10)
	if err != nil || len(values) != 1 {
		t.Fatalf("work timeline = %+v, %v", values, err)
	}
	want := values[0]
	got, err := authority.GetCommittedChange(ctx, want.TransactionID, work.ID)
	if err != nil || got.Checksum != want.Checksum {
		t.Fatalf("GetCommittedChange() = %+v, %v", got, err)
	}
	if _, err := authority.GetCommittedChange(ctx, want.TransactionID, private.ID); !errors.Is(err, changeindex.ErrNotFound) {
		t.Fatalf("cross-scope lookup error = %v", err)
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
	got, err = reopened.GetCommittedChange(ctx, want.TransactionID, work.ID)
	if err != nil || got.Checksum != want.Checksum {
		t.Fatalf("reopened GetCommittedChange() = %+v, %v", got, err)
	}
}
