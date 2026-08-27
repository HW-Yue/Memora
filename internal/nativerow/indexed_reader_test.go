package nativerow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

func TestIndexedPointGetMatchesLegacyEnvelopeAndSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	request := indexedPointRequest()

	legacyCatalog := nativecatalog.NewService(nativecatalog.New(fixture.file), nativecatalog.ServiceOptions{})
	legacyRows := NewService(New(fixture.file), legacyCatalog, ServiceOptions{})
	legacySession := executor.NewBatchSession(ctx, legacyCatalog, legacyRows)
	want := legacySession.Execute(ctx, request)
	if !want.OK {
		t.Fatalf("legacy envelope = %#v", want)
	}
	_ = legacySession.Close()

	got, legacyCalls := executeIndexedPointRequest(t, ctx, fixture, request)
	assertEnvelopeJSONEqual(t, got, want)
	if *legacyCalls != 0 {
		t.Fatalf("indexed lane called legacy readers %d time(s)", *legacyCalls)
	}

	fixture.close(t)
	reopened := reopenIndexedPointFixture(t, fixture.directory)
	t.Cleanup(func() { reopened.close(t) })
	reopenedEnvelope, reopenedLegacyCalls := executeIndexedPointRequest(t, ctx, reopened, request)
	assertEnvelopeJSONEqual(t, reopenedEnvelope, want)
	if *reopenedLegacyCalls != 0 {
		t.Fatalf("reopened indexed lane called legacy readers %d time(s)", *reopenedLegacyCalls)
	}
}

func TestIndexedPointGetRejectsLocatorMismatchWithoutLegacyFallback(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, err := nativecatalog.NewIndexedReader(nativecatalog.New(fixture.file), fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	badVersions := &mismatchingVersionLookup{delegate: fixture.versions}
	points, err := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, badVersions)
	if err != nil {
		t.Fatal(err)
	}
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := points.Capture(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := points.Get(ctx, table, "row_live", snapshot); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("locator mismatch error = %v", err)
	}

	calls := 0
	session := executor.NewBatchSessionWithPointReads(
		ctx,
		&poisonIndexedCatalog{calls: &calls},
		&poisonIndexedRows{calls: &calls},
		points,
	)
	t.Cleanup(func() { _ = session.Close() })
	envelope := session.Execute(ctx, executor.BatchRequest{
		RequestID: "corrupt-indexed-point",
		Source:    "SELECT title FROM work.notes WHERE row_id = 'row_live' LIMIT 1",
	})
	if envelope.OK || len(envelope.Results) != 1 || envelope.Results[0].Error == nil ||
		envelope.Results[0].Error.Code != result.CodeInternal {
		t.Fatalf("corrupt indexed envelope = %#v", envelope)
	}
	if calls != 0 {
		t.Fatalf("corrupt indexed lane fell back %d time(s)", calls)
	}
	notFound := session.Execute(ctx, executor.BatchRequest{
		RequestID: "missing-indexed-history",
		Source:    "SELECT title FROM work.notes AS OF REVISION 99 WHERE row_id = 'row_live' LIMIT 1",
	})
	if notFound.OK || len(notFound.Results) != 1 || notFound.Results[0].Error == nil ||
		notFound.Results[0].Error.Code != result.CodeNotFound {
		t.Fatalf("missing indexed history envelope = %#v", notFound)
	}
	if calls != 0 {
		t.Fatalf("missing indexed history fell back %d time(s)", calls)
	}
}

type indexedPointFixture struct {
	directory                string
	file                     *nativestore.File
	catalog                  *catalogindex.Index
	current                  *currentrowindex.Index
	versions                 *rowversionindex.Index
	catalogTree, currentTree *indexedPointTree
	versionTree              *indexedPointTree
	closed                   bool
}

type indexedPointTree struct {
	set     *wal.SegmentSet
	manager *page.Manager
	runtime *treecommit.Runtime
}

func createIndexedPointFixture(t *testing.T) *indexedPointFixture {
	t.Helper()
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	database, rows := indexedPointData()
	catalogRepository := nativecatalog.New(file)
	if err := catalogRepository.Write([]catalog.Database{database}); err != nil {
		t.Fatal(err)
	}
	rowRepository := New(file)
	for _, versions := range rows {
		if err := rowRepository.Write(versions[0]); err != nil {
			t.Fatal(err)
		}
		for _, version := range versions[1:] {
			if err := rowRepository.WriteRevision(version); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture := openIndexedPointFixture(t, directory, file, false)
	if _, err := fixture.catalog.Replace(1, []catalog.Database{database}); err != nil {
		t.Fatal(err)
	}
	currentInitial := make([]currentrowindex.Update, 0, len(rows))
	currentFinal := make([]currentrowindex.Update, 0, len(rows))
	versionLocators := make([]rowversionindex.Locator, 0, 4)
	for _, versions := range rows {
		for _, value := range versions {
			versionLocators = append(versionLocators, versionLocator(value))
		}
		currentInitial = append(currentInitial, currentrowindex.Update{Locator: currentLocator(versions[0])})
		currentFinal = append(currentFinal, currentrowindex.Update{
			ExpectedRevision: versions[0].Revision,
			Locator:          currentLocator(versions[len(versions)-1]),
		})
	}
	if _, err := fixture.current.Apply(1, currentInitial); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.current.Apply(2, currentFinal); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.versions.Append(1, versionLocators); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func reopenIndexedPointFixture(t *testing.T, directory string) *indexedPointFixture {
	t.Helper()
	file, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	return openIndexedPointFixture(t, directory, file, true)
}

func openIndexedPointFixture(
	t *testing.T,
	directory string,
	file *nativestore.File,
	reopen bool,
) *indexedPointFixture {
	t.Helper()
	catalogTree := openIndexedPointTree(t, directory, "catalog", 701, reopen)
	currentTree := openIndexedPointTree(t, directory, "current", 702, reopen)
	versionTree := openIndexedPointTree(t, directory, "versions", 703, reopen)
	catalogLookup, err := catalogindex.Open(catalogTree.runtime)
	if err != nil {
		t.Fatal(err)
	}
	currentLookup, err := currentrowindex.Open(currentTree.runtime)
	if err != nil {
		t.Fatal(err)
	}
	versionLookup, err := rowversionindex.Open(versionTree.runtime)
	if err != nil {
		t.Fatal(err)
	}
	return &indexedPointFixture{
		directory: directory, file: file,
		catalog: catalogLookup, current: currentLookup, versions: versionLookup,
		catalogTree: catalogTree, currentTree: currentTree, versionTree: versionTree,
	}
}

func openIndexedPointTree(
	t *testing.T,
	directory, name string,
	spaceID uint64,
	reopen bool,
) *indexedPointTree {
	t.Helper()
	walPath := filepath.Join(directory, name+"-wal")
	pagePath := filepath.Join(directory, name+".pages")
	var (
		set     *wal.SegmentSet
		manager *page.Manager
		err     error
	)
	if reopen {
		set, err = wal.OpenSegmentSet(walPath, 0)
	} else {
		set, err = wal.CreateSegmentSet(walPath, 0)
	}
	if err != nil {
		t.Fatal(err)
	}
	if reopen {
		manager, err = page.Open(pagePath, spaceID)
	} else {
		manager, err = page.Create(pagePath, spaceID)
	}
	if err != nil {
		_ = set.Close()
		t.Fatal(err)
	}
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: spaceID, Capacity: 128, OldFrames: 64,
	})
	if err != nil {
		_ = set.Close()
		_ = manager.Close()
		t.Fatal(err)
	}
	return &indexedPointTree{set: set, manager: manager, runtime: runtime}
}

func (fixture *indexedPointFixture) close(t *testing.T) {
	t.Helper()
	if fixture == nil || fixture.closed {
		return
	}
	fixture.closed = true
	for _, tree := range []*indexedPointTree{fixture.catalogTree, fixture.currentTree, fixture.versionTree} {
		if err := tree.set.Close(); err != nil {
			t.Errorf("close WAL: %v", err)
		}
		if err := tree.manager.Close(); err != nil {
			t.Errorf("close Page file: %v", err)
		}
	}
	if err := fixture.file.Close(); err != nil {
		t.Errorf("close native file: %v", err)
	}
}

func executeIndexedPointRequest(
	t *testing.T,
	ctx context.Context,
	fixture *indexedPointFixture,
	request executor.BatchRequest,
) (result.Envelope, *int) {
	t.Helper()
	catalogReader, err := nativecatalog.NewIndexedReader(nativecatalog.New(fixture.file), fixture.catalog)
	if err != nil {
		t.Fatal(err)
	}
	points, err := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	session := executor.NewBatchSessionWithPointReads(
		ctx,
		&poisonIndexedCatalog{calls: &calls},
		&poisonIndexedRows{calls: &calls},
		points,
	)
	defer func() { _ = session.Close() }()
	envelope := session.Execute(ctx, request)
	return envelope, &calls
}

type poisonIndexedCatalog struct {
	executor.Catalog
	calls *int
}

func (dictionary *poisonIndexedCatalog) DescribeTable(context.Context, string, string) (catalog.Table, error) {
	(*dictionary.calls)++
	return catalog.Table{}, fmt.Errorf("legacy Catalog path was called")
}

type poisonIndexedRows struct {
	executor.Rows
	calls *int
}

func (rows *poisonIndexedRows) Get(context.Context, string, string, string) (row.Row, error) {
	(*rows.calls)++
	return row.Row{}, fmt.Errorf("legacy Row path was called")
}

func (rows *poisonIndexedRows) AsOfRevision(context.Context, string, string, string, uint64) (row.Row, error) {
	(*rows.calls)++
	return row.Row{}, fmt.Errorf("legacy revision path was called")
}

func (rows *poisonIndexedRows) AsOfCommit(context.Context, string, string, string, uint64) (row.Row, error) {
	(*rows.calls)++
	return row.Row{}, fmt.Errorf("legacy commit path was called")
}

type mismatchingVersionLookup struct{ delegate *rowversionindex.Index }

func (lookup *mismatchingVersionLookup) ByRevision(rowID string, revision uint64) (rowversionindex.Locator, error) {
	locator, err := lookup.delegate.ByRevision(rowID, revision)
	locator.CommitSequence++
	return locator, err
}

func (lookup *mismatchingVersionLookup) AsOfCommit(rowID string, sequence uint64) (rowversionindex.Locator, error) {
	return lookup.delegate.AsOfCommit(rowID, sequence)
}

func (lookup *mismatchingVersionLookup) VisibleAt(rowID string, sequence uint64) (rowversionindex.Locator, error) {
	locator, err := lookup.delegate.VisibleAt(rowID, sequence)
	locator.CommitSequence++
	return locator, err
}

func (lookup *mismatchingVersionLookup) HighWater() (uint64, error) {
	return lookup.delegate.HighWater()
}

func (lookup *mismatchingVersionLookup) RevisionsPage(
	rowID string, beforeRevision uint64, limit int,
) (rowversionindex.RevisionsPage, error) {
	return lookup.delegate.RevisionsPage(rowID, beforeRevision, limit)
}

func indexedPointRequest() executor.BatchRequest {
	return executor.BatchRequest{
		RequestID: "indexed-point-envelope",
		Source: `
			SELECT title, revision FROM work.notes WHERE row_id = 'row_live' LIMIT 1;
			SELECT title FROM work.notes WHERE row_id = 'row_missing' LIMIT 1;
			SELECT title FROM work.notes WHERE row_id = 'row_deleted' LIMIT 1;
			SELECT title, revision FROM work.notes AS OF REVISION 1 WHERE row_id = 'row_live' LIMIT 1;
			SELECT title, commit_sequence FROM work.notes AS OF COMMIT_SEQUENCE 15 WHERE row_id = 'row_live' LIMIT 1;
		`,
	}
}

func indexedPointData() (catalog.Database, map[string][]row.Row) {
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	database := catalog.Database{
		ID: "db_work", Name: "work", Aliases: []string{"工作"}, Purpose: "Work", Scope: "Projects",
		SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
		Tables: []catalog.Table{{
			ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{"知识"},
			Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1,
			CreatedAt: now, UpdatedAt: now,
			Columns: []catalog.Column{{
				ID: "col_title", Name: "title", Aliases: []string{"标题"}, Type: "TEXT",
				MaxCharacters: 100, Purpose: "Title", SchemaVersion: 1,
				CreatedAt: now, UpdatedAt: now,
			}},
		}},
	}
	makeRow := func(id string, revision, sequence uint64, state row.State, title string) row.Row {
		return row.Row{
			ID: id, DatabaseID: "db_work", TableID: "tbl_notes", SchemaVersion: 1,
			Revision: revision, CommitSequence: sequence, State: state,
			Values: map[string]any{"col_title": title}, CreatedAt: now,
			UpdatedAt: now.Add(time.Duration(revision) * time.Minute),
		}
	}
	return database, map[string][]row.Row{
		"row_live": {
			makeRow("row_live", 1, 10, row.StateLive, "historical"),
			makeRow("row_live", 2, 20, row.StateLive, "current"),
		},
		"row_deleted": {
			makeRow("row_deleted", 1, 15, row.StateLive, "before delete"),
			makeRow("row_deleted", 2, 25, row.StateDeleted, "before delete"),
		},
	}
}

func currentLocator(value row.Row) currentrowindex.Locator {
	return currentrowindex.Locator{
		DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
		SchemaRevision: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, State: value.State,
	}
}

func versionLocator(value row.Row) rowversionindex.Locator {
	return rowversionindex.Locator{
		DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
		SchemaRevision: value.SchemaVersion, Revision: value.Revision,
		CommitSequence: value.CommitSequence, State: value.State,
	}
}

func assertEnvelopeJSONEqual(t *testing.T, got, want result.Envelope) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("envelope JSON mismatch\n got: %s\nwant: %s", gotJSON, wantJSON)
	}
}

// oneTableRows adapts a single Table's current Row Index to the reader's
// CurrentLookup, which names a Table because production routes on it. This
// fixture has one Table, so the name picks the only Index there is.
type oneTableRows struct {
	index *currentrowindex.Index
}

func (rows oneTableRows) Lookup(_, rowID string) (currentrowindex.Locator, error) {
	return rows.index.Lookup(rowID)
}

func (rows oneTableRows) Page(_, afterRowID string, limit uint64) (currentrowindex.CursorPage, error) {
	return rows.index.Page(afterRowID, limit)
}
