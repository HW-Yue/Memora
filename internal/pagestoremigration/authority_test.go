package pagestoremigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestAuthorityReadsIgnorePoisonedLegacyEnumerationPaths(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_ = directory
	dictionary, rows, table, inserted := authorityValues(t, ctx, file, authority)

	if err := file.Put(nativestore.ObjectKindDatabase, 1, "poison_database", []byte("not-a-catalog-record")); err != nil {
		t.Fatal(err)
	}
	poisonRevision := fmt.Sprintf("%s@%020d", inserted.ID, 999)
	if err := file.Put(nativestore.ObjectKindRow, 1, poisonRevision, []byte("not-a-row-record")); err != nil {
		t.Fatal(err)
	}
	if _, err := nativecatalog.New(file).Read(); err == nil {
		t.Fatal("legacy Catalog enumeration unexpectedly accepted its poison record")
	}
	if _, err := nativerow.New(file).Read(inserted.ID); err == nil {
		t.Fatal("legacy Row enumeration unexpectedly accepted its poison revision")
	}

	gotTable, err := dictionary.DescribeTable(ctx, "work", "notes")
	if err != nil || gotTable.ID != table.ID {
		t.Fatalf("Page DescribeTable() = %+v, %v", gotTable, err)
	}
	got, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || got.ID != inserted.ID || got.Values["title"] != "indexed" {
		t.Fatalf("Page Get() = %+v, %v", got, err)
	}
}

func TestAuthorityMarkerGoldenEncoding(t *testing.T) {
	marker, err := newAuthorityMarker(generationManifest{
		PlanDigest: strings.Repeat("a", 64), SourceFingerprint: strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n" +
		"  \"version\": \"memora.page-store-authority/v1\",\n" +
		"  \"generation\": \"page-index-v1\",\n" +
		"  \"plan_digest\": \"" + strings.Repeat("a", 64) + "\",\n" +
		"  \"source_sha256\": \"" + strings.Repeat("b", 64) + "\",\n" +
		"  \"digest\": \"999b0627afcc3548975f316c2c6a15c22f2d4874f13d5828cde741e550ef1cd4\"\n" +
		"}"
	if string(encoded) != want {
		t.Fatalf("marker golden = %s", encoded)
	}
}

func TestAuthorityReopenRepairsBodyCommitMissingFromPageTrees(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	dictionary, _, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	legacyWriter := nativerow.NewService(nativerow.New(file), dictionary, nativerow.ServiceOptions{})
	inserted, err := legacyWriter.Insert(ctx, "work", "notes", map[string]any{"title": "repair"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := authority.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Get(ctx, table, inserted.ID, snapshot); err == nil {
		t.Fatal("unpublished native body was visible before reopen")
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
	t.Cleanup(func() { _ = reopenedFile.Close() })
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedCatalog := nativecatalog.NewService(
		nativecatalog.New(reopenedFile), nativecatalog.ServiceOptions{Authority: reopened},
	)
	reopenedRows := nativerow.NewService(
		nativerow.New(reopenedFile), reopenedCatalog, nativerow.ServiceOptions{Authority: reopened},
	)
	got, err := reopenedRows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || got.Values["title"] != "repair" {
		t.Fatalf("repaired Get() = %+v, %v", got, err)
	}
}

func TestAuthorityMarkerCorruptionFailsClosedWithoutRebuilding(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(directory, AuthorityMarkerFilename)
	encoded, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	encoded[len(encoded)/2] ^= 0x01
	if err := os.WriteFile(markerPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	if _, err := OpenAuthority(ctx, reopenedFile, directory); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("OpenAuthority(corrupt marker) error = %v", err)
	}
}

func TestAuthorityMarkerSymlinkFailsClosed(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(directory, AuthorityMarkerFilename)
	realPath := filepath.Join(directory, "saved-authority-marker.json")
	if err := os.Rename(markerPath, realPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Base(realPath), markerPath); err != nil {
		t.Fatal(err)
	}
	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	if _, err := OpenAuthority(ctx, reopenedFile, directory); !errors.Is(err, ErrTargetCorrupt) {
		t.Fatalf("OpenAuthority(symlink marker) error = %v", err)
	}
}

func TestAuthorityPublishesCurrentAndHistoricalRowRevisions(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "revised"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("Update() = %+v, %v", updated, err)
	}
	current, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || current.Revision != 2 || current.Values["title"] != "revised" {
		t.Fatalf("current Get() = %+v, %v", current, err)
	}
	first, err := rows.AsOfRevision(ctx, "work", "notes", inserted.ID, 1)
	if err != nil || first.Values["title"] != "indexed" {
		t.Fatalf("AsOfRevision(1) = %+v, %v", first, err)
	}
	page, more, err := rows.ListPage(ctx, "work", "notes", 10)
	if err != nil || more || len(page) != 1 || page[0].Revision != 2 {
		t.Fatalf("ListPage() = %+v, %v, %v", page, more, err)
	}
	deleted, err := rows.Delete(ctx, "work", "notes", inserted.ID, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: 2,
	})
	if err != nil || deleted.Revision != 3 || deleted.State != row.StateDeleted {
		t.Fatalf("Delete() = %+v, %v", deleted, err)
	}
	if _, err := rows.Get(ctx, "work", "notes", inserted.ID); err == nil {
		t.Fatal("deleted Row remained visible through Current Index")
	}
	// Deletion makes a Row unreachable, and AS OF is a read of the values
	// themselves — so publishing historical revisions must not leave them
	// readable once the Row is gone. Both of this Row's earlier revisions are
	// still published; the refusal is what keeps them out of reach.
	if second, err := rows.AsOfRevision(ctx, "work", "notes", inserted.ID, 2); err == nil {
		t.Fatalf("AsOfRevision must refuse a deleted Row, got %+v", second)
	}
	if first, err := rows.AsOfRevision(ctx, "work", "notes", inserted.ID, 1); err == nil {
		t.Fatalf("AsOfRevision must refuse every revision of a deleted Row, got %+v", first)
	}
}

func TestAuthorityMarkerPublicationFaultsAreRecoverableOrOutcomeUnknown(t *testing.T) {
	for _, test := range []struct {
		name    string
		mutate  func(*authorityMarkerOperations)
		unknown bool
		visible bool
	}{
		{
			name: "rename",
			mutate: func(operations *authorityMarkerOperations) {
				operations.rename = func(string, string) error { return errors.New("rename fault") }
			},
		},
		{
			name: "parent fsync", unknown: true, visible: true,
			mutate: func(operations *authorityMarkerOperations) {
				operations.syncDir = func(string) error { return errors.New("sync fault") }
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			reader, _ := NewNativeReader(file)
			plan, err := reader.Build(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			applier, _ := NewApplier(reader, directory)
			if _, err := applier.Apply(context.Background(), plan); err != nil {
				t.Fatal(err)
			}
			manifest, err := readManifest(filepath.Join(directory, GenerationDirectory))
			if err != nil {
				t.Fatal(err)
			}
			marker, _ := newAuthorityMarker(manifest)
			operations := authorityMarkerOperations{
				createTemp: os.CreateTemp, rename: os.Rename, syncDir: syncGenerationDirectory,
			}
			test.mutate(&operations)
			err = writeAuthorityMarkerWithOperations(directory, marker, operations)
			if test.unknown != errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("write marker error = %v, outcome-unknown = %v", err, test.unknown)
			}
			_, statErr := os.Stat(filepath.Join(directory, AuthorityMarkerFilename))
			if test.visible != (statErr == nil) {
				t.Fatalf("marker visibility error = %v, want visible %v", statErr, test.visible)
			}
			if test.visible {
				if _, err := readAuthorityMarker(directory, manifest); err != nil {
					t.Fatalf("visible marker is invalid: %v", err)
				}
			}
		})
	}
}

func TestAuthorityRowPublicationFaultsPoisonAndReopenConverges(t *testing.T) {
	for _, phase := range []authorityPhase{
		phaseRowBodyCommitted, phaseRowVersionPublished, phaseRowFulltextPublished, phaseRowCurrentPublished,
	} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, table, inserted := authorityValues(t, ctx, file, authority)
			injected := errors.New("injected publication fault")
			authority.checkpoint = func(current authorityPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			_, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "recovered"}, row.WriteOptions{
				ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
			})
			if !errors.Is(err, ErrOutcomeUnknown) || !strings.Contains(err.Error(), injected.Error()) {
				t.Fatalf("Update(%s fault) error = %v", phase, err)
			}
			// F226: an uncertain publication no longer blocks reads — the
			// committed generation stays consistent — but the affected
			// Database still fails closed for writes.
			if _, err := authority.Capture(ctx); err != nil {
				t.Fatalf("poisoned Capture() error = %v, want success", err)
			}
			if _, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "blocked"}, row.WriteOptions{
				ExpectedSchemaVersion: table.SchemaVersion,
			}); !errors.Is(err, ErrAuthorityPoisoned) {
				t.Fatalf("write to the poisoned Database = %v, want ErrAuthorityPoisoned", err)
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
			dictionary := nativecatalog.NewService(
				nativecatalog.New(reopenedFile), nativecatalog.ServiceOptions{Authority: reopened},
			)
			reopenedRows := nativerow.NewService(
				nativerow.New(reopenedFile), dictionary, nativerow.ServiceOptions{Authority: reopened},
			)
			got, err := reopenedRows.Get(ctx, "work", "notes", inserted.ID)
			if err != nil || got.Revision != 2 || got.Values["title"] != "recovered" {
				t.Fatalf("reopened recovered Get() = %+v, %v", got, err)
			}
			assertNoRowPosting(t, reopened, "indexed")
			assertRowPosting(t, reopened, "recovered", inserted.ID, 2)
		})
	}
}

func TestAuthorityCatalogPublicationFaultsPoisonAndReopenConverges(t *testing.T) {
	for _, phase := range []authorityPhase{phaseCatalogBodyCommitted, phaseCatalogPublished, phaseCatalogFulltextPublished} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			dictionary := nativecatalog.NewService(
				nativecatalog.New(file), nativecatalog.ServiceOptions{Authority: authority},
			)
			injected := errors.New("injected Catalog publication fault")
			authority.checkpoint = func(current authorityPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			_, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
				Name: "recovered", Purpose: "Recovered", Scope: "Fault fixture",
			})
			if !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("CreateDatabase(%s fault) error = %v", phase, err)
			}
			// F226: Catalog reads stay available after an uncertain publication.
			if _, err := authority.ShowDatabases(ctx); err != nil {
				t.Fatalf("poisoned ShowDatabases() error = %v, want success", err)
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
			got, err := reopened.DescribeDatabase(ctx, "recovered")
			if err != nil || got.Name != "recovered" {
				t.Fatalf("reopened recovered Database = %+v, %v", got, err)
			}
			assertCatalogPosting(t, reopened.Generation(), "recovered", fulltext.KindDatabase, got.ID, got.SchemaVersion)
		})
	}
}

func TestAuthorityRefusesToActivateStaleF106Generation(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, _ := NewNativeReader(file)
	plan, err := reader.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applier, _ := NewApplier(reader, directory)
	if _, err := applier.Apply(ctx, plan); err != nil {
		t.Fatal(err)
	}
	legacyCatalog := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	if _, err := legacyCatalog.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "late", Purpose: "Late", Scope: "Changed source",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenAuthority(ctx, file, directory); !errors.Is(err, ErrConflict) {
		t.Fatalf("OpenAuthority(stale generation) error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, AuthorityMarkerFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale generation published marker: %v", err)
	}
}

func TestAuthorityConcurrentSameRowWritersPublishExactlyOneRevision(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	const writers = 12
	var succeeded atomic.Int64
	errorsByWriter := make(chan error, writers)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(number int) {
			defer wait.Done()
			<-start
			_, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
				"title": fmt.Sprintf("writer-%d", number),
			}, row.WriteOptions{
				ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
			})
			if err == nil {
				succeeded.Add(1)
				return
			}
			errorsByWriter <- err
		}(index)
	}
	close(start)
	wait.Wait()
	close(errorsByWriter)
	if succeeded.Load() != 1 || len(errorsByWriter) != writers-1 {
		t.Fatalf("concurrent writers succeeded=%d conflicts=%d", succeeded.Load(), len(errorsByWriter))
	}
	for err := range errorsByWriter {
		var stable interface{ StableCode() string }
		if !errors.As(err, &stable) ||
			(stable.StableCode() != "revision_conflict" && stable.StableCode() != "write_conflict") {
			t.Fatalf("losing writer error = %v", err)
		}
	}
	current, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || current.Revision != 2 {
		t.Fatalf("current after concurrent writers = %+v, %v", current, err)
	}
}

func newAuthorityFixture(t *testing.T) (string, *nativestore.File, *Authority) {
	t.Helper()
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := OpenAuthority(context.Background(), file, directory)
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = authority.Close()
		_ = file.Close()
	})
	return directory, file, authority
}

func authorityValues(
	t *testing.T, ctx context.Context, file *nativestore.File, authority *Authority,
) (*nativecatalog.Service, *nativerow.Service, catalog.Table, row.Row) {
	t.Helper()
	dictionary, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "indexed"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	return dictionary, rows, table, inserted
}

func authorityValuesWithoutRow(
	t *testing.T, ctx context.Context, file *nativestore.File, authority *Authority,
) (*nativecatalog.Service, *nativerow.Service, catalog.Table, row.Row) {
	t.Helper()
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
		nativerow.New(file), dictionary, nativerow.ServiceOptions{Authority: authority},
	)
	return dictionary, rows, table, row.Row{}
}

// TestRowPublicationFaultIsolatesOneDatabase proves that a failed publication in
// one Database neither blocks reads anywhere nor blocks writes to a different
// Database. Before F226 the Authority carried a single instance-wide poisoned
// flag that both BeginWrite and lockRead consulted, so one bad publication took
// the whole Instance offline for reads and writes alike.
func TestRowPublicationFaultIsolatesOneDatabase(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	dictionary, rows, table, inserted := authorityValues(t, ctx, file, authority)

	// A second Database that the injected fault never touches.
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "personal", Purpose: "Personal", Scope: "Life",
	}); err != nil {
		t.Fatal(err)
	}
	other, err := dictionary.CreateTable(ctx, "personal", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(40)", Purpose: "Title"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	injected := errors.New("injected publication fault")
	authority.checkpoint = func(current authorityPhase) error {
		if current == phaseRowBodyCommitted {
			return injected
		}
		return nil
	}
	if _, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{"title": "recovered"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
	}); !errors.Is(err, ErrOutcomeUnknown) {
		t.Fatalf("faulted Update error = %v, want ErrOutcomeUnknown", err)
	}
	authority.checkpoint = nil

	// Reads stay available everywhere: each generation is swapped atomically
	// under the same lock readers hold, so a reader always sees one complete
	// generation. Refusing reads turns one uncertain write into a dead Instance.
	snapshot, err := authority.Capture(ctx)
	if err != nil {
		t.Fatalf("Capture() after fault = %v, want success", err)
	}
	if _, err := authority.Get(ctx, table, inserted.ID, snapshot); err != nil {
		t.Fatalf("Get() on the affected Database = %v, want success", err)
	}
	if _, err := authority.ShowDatabases(ctx); err != nil {
		t.Fatalf("ShowDatabases() after fault = %v, want success", err)
	}

	// Writes to the untouched Database stay available.
	if _, err := rows.Insert(ctx, "personal", "notes", map[string]any{"title": "unaffected"}, row.WriteOptions{
		ExpectedSchemaVersion: other.SchemaVersion,
	}); err != nil {
		t.Fatalf("Insert into the unaffected Database = %v, want success", err)
	}

	// Writes to the affected Database still fail closed, and say which one.
	_, err = rows.Insert(ctx, "work", "notes", map[string]any{"title": "blocked"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if !errors.Is(err, ErrAuthorityPoisoned) {
		t.Fatalf("Insert into the affected Database = %v, want ErrAuthorityPoisoned", err)
	}
	if !strings.Contains(err.Error(), table.DatabaseID) {
		t.Fatalf("poison error %q does not name the affected Database %q", err, table.DatabaseID)
	}
}

// assertDatabaseWritesPoisoned checks that one Database fails closed for writes
// after an uncertain publication. F226 scopes poison per Database, so probing a
// read (the old proxy) no longer proves anything: reads stay available.
func assertDatabaseWritesPoisoned(
	t *testing.T, ctx context.Context, authority *Authority, databaseID string,
) {
	t.Helper()
	release, err := authority.BeginRowWrite(ctx, databaseID, "tbl_probe", []string{"row_probe"})
	if err == nil {
		release()
		t.Fatalf("writes to database %s are still allowed, want ErrAuthorityPoisoned", databaseID)
	}
	if !errors.Is(err, ErrAuthorityPoisoned) {
		t.Fatalf("write to database %s = %v, want ErrAuthorityPoisoned", databaseID, err)
	}
}
