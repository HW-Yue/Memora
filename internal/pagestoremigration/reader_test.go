package pagestoremigration_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/pagestoremigration"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestNativeReaderBuildsCatalogCurrentAndEveryRowVersion(t *testing.T) {
	file, first, second := migrationFixture(t)
	reader, err := pagestoremigration.NewNativeReader(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != pagestoremigration.PlanVersion || len(plan.SourceFingerprint) != 64 ||
		len(plan.Digest) != 64 || len(plan.Catalog) != 1 || len(plan.CurrentRows) != 1 ||
		len(plan.CurrentRowBodies) != 1 || len(plan.RowVersions) != 2 {
		t.Fatalf("migration Plan = %#v", plan)
	}
	if plan.CurrentRows[0].RowID != second.ID || plan.CurrentRows[0].Revision != second.Revision ||
		plan.CurrentRowBodies[0].ID != second.ID || plan.CurrentRowBodies[0].Revision != second.Revision ||
		plan.CurrentRowBodies[0].Values["col_title"] != second.Values["col_title"] ||
		plan.RowVersions[0].Revision != first.Revision || plan.RowVersions[1].Revision != second.Revision {
		t.Fatalf("migration Row locators = %#v / %#v", plan.CurrentRows, plan.RowVersions)
	}
}

func TestEmptyNativeDatabaseProducesValidBoundPlan(t *testing.T) {
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "empty.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil || len(plan.Catalog) != 0 || len(plan.CurrentRows) != 0 || len(plan.RowVersions) != 0 {
		t.Fatalf("empty Plan = %#v, %v", plan, err)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestNativeReaderBindsCanonicalCurrentRoutesIntoPlanV3(t *testing.T) {
	file, _, _ := migrationFixture(t)
	routes := nativerouter.New(file)
	root, err := routes.CreateRoot("route_root", "db_work", "tbl_notes", "All notes")
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := routes.CreateChild("route_leaf", root.ID, "architecture", router.KindLeaf, "Architecture decisions")
	if err != nil {
		t.Fatal(err)
	}
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != "memora.page-index-migration-plan/v3" || len(plan.CurrentRoutes) != 2 ||
		plan.CurrentRoutes[0].ID != root.ID || plan.CurrentRoutes[1].ID != leaf.ID {
		t.Fatalf("Route Plan v3 = %#v", plan)
	}
	plan.CurrentRoutes[1].Purpose = "digest tampered"
	if err := plan.Validate(); !errors.Is(err, pagestoremigration.ErrInvalid) {
		t.Fatalf("tampered Route Plan Validate() error = %v", err)
	}
}

func TestReaderRejectsRouteOutsideCurrentCatalogTable(t *testing.T) {
	database, _, _ := migrationValues()
	route := router.Node{Version: router.Version, ID: "route_wrong", DatabaseID: "db_work", TableID: "tbl_other",
		Name: "wrong", Path: "/", Kind: router.KindRoot, Purpose: "Wrong scope", Revision: 1}
	source := &fakeSource{
		states: []pagestoremigration.SourceState{sourceState(5, 0)}, catalog: database, routes: []router.Node{route},
	}
	reader, _ := pagestoremigration.NewReader(source)
	if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrCorrupt) {
		t.Fatalf("out-of-scope Route Build() error = %v", err)
	}
}

func TestNativePlanIsDeterministicReadOnlyAndStableAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.memora")
	file, _, _ := migrationFixtureAt(t, path)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	reader, _ := pagestoremigration.NewNativeReader(file)
	first, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := reader.Build(context.Background())
	if err != nil || !reflect.DeepEqual(second, first) {
		t.Fatalf("repeated Plan = %#v, %v; want %#v", second, err, first)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("Plan Validate() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedReader, _ := pagestoremigration.NewNativeReader(reopened)
	afterReopen, err := reopenedReader.Build(context.Background())
	if err != nil || !reflect.DeepEqual(afterReopen, first) {
		t.Fatalf("reopened Plan = %#v, %v; want %#v", afterReopen, err, first)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, before) {
		t.Fatalf("Reader changed legacy bytes: %v", err)
	}
}

func TestKnownNonIndexRecordChangesSourceBindingButStillPlans(t *testing.T) {
	file, _, _ := migrationFixture(t)
	reader, _ := pagestoremigration.NewNativeReader(file)
	before, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Put(nativestore.ObjectKindConfiguration, 1, "config_one", []byte("opaque-to-index-plan")); err != nil {
		t.Fatal(err)
	}
	after, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if before.SourceFingerprint == after.SourceFingerprint || before.Digest == after.Digest {
		t.Fatal("known non-index Record did not change source/Plan binding")
	}
	if countFor(after.RecordCounts, nativestore.ObjectKindConfiguration) != 1 {
		t.Fatalf("Configuration count = %#v", after.RecordCounts)
	}
}

func TestReaderVerifySourceRejectsStaleOrInvalidPlan(t *testing.T) {
	file, _, _ := migrationFixture(t)
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.VerifySource(context.Background(), plan); err != nil {
		t.Fatalf("unchanged source VerifySource() error = %v", err)
	}
	if err := file.Put(nativestore.ObjectKindConfiguration, 1, "config_one", []byte("changed")); err != nil {
		t.Fatal(err)
	}
	if err := reader.VerifySource(context.Background(), plan); !errors.Is(err, pagestoremigration.ErrSourceChanged) {
		t.Fatalf("changed source VerifySource() error = %v", err)
	}
	plan.Version = "caller-mutated"
	if err := reader.VerifySource(context.Background(), plan); !errors.Is(err, pagestoremigration.ErrInvalid) {
		t.Fatalf("invalid Plan VerifySource() error = %v", err)
	}
}

func TestNativeReaderRejectsUnknownKindAndCorruptPayload(t *testing.T) {
	t.Run("unknown kind", func(t *testing.T) {
		file, _, _ := migrationFixture(t)
		if err := file.Put(nativestore.ObjectKind(77), 1, "future", []byte("future")); err != nil {
			t.Fatal(err)
		}
		reader, _ := pagestoremigration.NewNativeReader(file)
		if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrUnsupported) {
			t.Fatalf("unknown kind Build() error = %v", err)
		}
	})
	t.Run("payload CRC", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "legacy.memora")
		file, _, _ := migrationFixtureAt(t, path)
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		var last [1]byte
		if _, err := handle.ReadAt(last[:], stat.Size()-1); err != nil {
			t.Fatal(err)
		}
		last[0] ^= 0xff
		if _, err := handle.WriteAt(last[:], stat.Size()-1); err != nil {
			t.Fatal(err)
		}
		if err := handle.Sync(); err != nil {
			t.Fatal(err)
		}
		_ = handle.Close()
		reader, _ := pagestoremigration.NewNativeReader(file)
		if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrCorrupt) {
			t.Fatalf("corrupt payload Build() error = %v", err)
		}
	})
	t.Run("Row Record identity", func(t *testing.T) {
		file, first, _ := migrationFixture(t)
		payload, err := file.Get(nativestore.ObjectKindRow, first.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Put(nativestore.ObjectKindRow, 1, "wrong@00000000000000000001", payload); err != nil {
			t.Fatal(err)
		}
		reader, _ := pagestoremigration.NewNativeReader(file)
		if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrCorrupt) {
			t.Fatalf("wrong Row Record identity Build() error = %v", err)
		}
	})
}

func TestReaderRejectsSourceChangeAndInvalidRowHistories(t *testing.T) {
	databases, first, second := migrationValues()
	t.Run("source changed", func(t *testing.T) {
		source := &fakeSource{
			states: []pagestoremigration.SourceState{sourceState(1, 0), sourceState(2, 0)},
		}
		reader, _ := pagestoremigration.NewReader(source)
		if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrSourceChanged) {
			t.Fatalf("changed source Build() error = %v", err)
		}
	})
	tests := []struct {
		name string
		rows []row.Row
	}{
		{"revision gap", []row.Row{second}},
		{"commit regression", func() []row.Row { value := second; value.CommitSequence = 5; return []row.Row{first, value} }()},
		{"zero after positive", func() []row.Row { value := second; value.CommitSequence = 0; return []row.Row{first, value} }()},
		{"identity drift", func() []row.Row { value := second; value.TableID = "tbl_other"; return []row.Row{first, value} }()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := &fakeSource{
				states:  []pagestoremigration.SourceState{sourceState(3, len(test.rows))},
				catalog: databases,
				rows:    test.rows,
			}
			reader, _ := pagestoremigration.NewReader(source)
			if _, err := reader.Build(context.Background()); !errors.Is(err, pagestoremigration.ErrCorrupt) {
				t.Fatalf("invalid history Build() error = %v", err)
			}
		})
	}
}

func TestPlanValidateRejectsCallerMutation(t *testing.T) {
	file, _, _ := migrationFixture(t)
	reader, _ := pagestoremigration.NewNativeReader(file)
	tests := []struct {
		name   string
		mutate func(*pagestoremigration.Plan)
	}{
		{"version locator", func(plan *pagestoremigration.Plan) { plan.RowVersions[0].Revision = 99 }},
		{"body locator", func(plan *pagestoremigration.Plan) { plan.CurrentRowBodies[0].Revision++ }},
		{"body schema", func(plan *pagestoremigration.Plan) { plan.CurrentRowBodies[0].SchemaVersion++ }},
		{"body projection", func(plan *pagestoremigration.Plan) {
			plan.CurrentRowBodies[0].Values["col_unknown"] = "poison"
		}},
		{"body digest", func(plan *pagestoremigration.Plan) {
			plan.CurrentRowBodies[0].Values["col_title"] = "different but valid"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan, err := reader.Build(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(&plan)
			if err := plan.Validate(); !errors.Is(err, pagestoremigration.ErrInvalid) {
				t.Fatalf("mutated Plan Validate() error = %v", err)
			}
		})
	}
}

func TestLargeNativeInventoryMatchesLocatorReferenceModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "large.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	databases, _, _ := migrationValues()
	if err := nativecatalog.New(file).Write(databases); err != nil {
		t.Fatal(err)
	}
	table := databases[0].Tables[0]
	repository := nativerow.New(file)
	transaction, err := file.Begin()
	if err != nil {
		t.Fatal(err)
	}
	now := databases[0].CreatedAt
	wantVersions := 0
	for number := 0; number < 1200; number++ {
		value := row.Row{
			ID: fmt.Sprintf("row_%04d", number), DatabaseID: databases[0].ID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: uint64(number + 1),
			State: row.StateLive, Values: map[string]any{"col_title": fmt.Sprintf("note-%d", number)},
			CreatedAt: now, UpdatedAt: now,
		}
		if err := repository.StageSnapshotRow(transaction, value, table); err != nil {
			t.Fatal(err)
		}
		wantVersions++
		if number%4 == 0 {
			value.Revision, value.CommitSequence = 2, uint64(10000+number)
			value.State = row.StateDeleted
			value.UpdatedAt = now.Add(time.Minute)
			if err := repository.StageSnapshotRow(transaction, value, table); err != nil {
				t.Fatal(err)
			}
			wantVersions++
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	reader, _ := pagestoremigration.NewNativeReader(file)
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.CurrentRows) != 1200 || len(plan.RowVersions) != wantVersions ||
		countFor(plan.RecordCounts, nativestore.ObjectKindRow) != uint64(wantVersions) {
		t.Fatalf("large Plan counts = current %d versions %d records %#v", len(plan.CurrentRows), len(plan.RowVersions), plan.RecordCounts)
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	versionIndex := 0
	for number, locator := range plan.CurrentRows {
		wantRevision := uint64(1)
		wantState := row.StateLive
		if number%4 == 0 {
			wantRevision, wantState = 2, row.StateDeleted
		}
		if locator.RowID != fmt.Sprintf("row_%04d", number) || locator.Revision != wantRevision || locator.State != wantState {
			t.Fatalf("current locator %d = %+v", number, locator)
		}
		if plan.RowVersions[versionIndex].RowID != locator.RowID || plan.RowVersions[versionIndex].Revision != 1 {
			t.Fatalf("first version %d = %+v", number, plan.RowVersions[versionIndex])
		}
		versionIndex++
		if wantRevision == 2 {
			if plan.RowVersions[versionIndex].RowID != locator.RowID || plan.RowVersions[versionIndex].Revision != 2 {
				t.Fatalf("second version %d = %+v", number, plan.RowVersions[versionIndex])
			}
			versionIndex++
		}
	}
	after, _ := os.ReadFile(path)
	if sha256.Sum256(before) != sha256.Sum256(after) {
		t.Fatal("large Reader changed source file")
	}
}

func TestCancelledAndWrongFileKindProduceNoPlan(t *testing.T) {
	file, _, _ := migrationFixture(t)
	reader, _ := pagestoremigration.NewNativeReader(file)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := reader.Build(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Build() error = %v", err)
	}
	system, err := nativestore.Create(filepath.Join(t.TempDir(), "system.memora"), nativestore.FileKindSystem)
	if err != nil {
		t.Fatal(err)
	}
	defer system.Close()
	if _, err := pagestoremigration.NewNativeReader(system); !errors.Is(err, pagestoremigration.ErrInvalid) {
		t.Fatalf("System NewNativeReader() error = %v", err)
	}
}

func migrationFixture(t *testing.T) (*nativestore.File, row.Row, row.Row) {
	t.Helper()
	return migrationFixtureAt(t, filepath.Join(t.TempDir(), "legacy.memora"))
}

func migrationFixtureAt(t *testing.T, path string) (*nativestore.File, row.Row, row.Row) {
	t.Helper()
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	databases, first, second := migrationValues()
	if err := nativecatalog.New(file).Write(databases); err != nil {
		t.Fatal(err)
	}
	repository := nativerow.New(file)
	if err := repository.Write(first); err != nil {
		t.Fatal(err)
	}
	if err := repository.WriteRevision(second); err != nil {
		t.Fatal(err)
	}
	return file, first, second
}

func migrationValues() ([]catalog.Database, row.Row, row.Row) {
	now := time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC)
	column := catalog.Column{
		ID: "col_title", Name: "title", Type: "TEXT", MaxCharacters: 100,
		Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	table := catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Purpose: "Notes",
		RowSemantics: "One note", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
		Columns: []catalog.Column{column},
	}
	database := catalog.Database{
		ID: "db_work", Name: "work", Purpose: "Work", Scope: "Personal",
		SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table},
	}
	first := row.Row{
		ID: "row_one", DatabaseID: database.ID, TableID: table.ID, SchemaVersion: 1,
		Revision: 1, CommitSequence: 10, State: row.StateLive,
		Values: map[string]any{"col_title": "first"}, CreatedAt: now, UpdatedAt: now,
	}
	second := first
	second.Revision, second.CommitSequence = 2, 20
	second.Values = map[string]any{"col_title": "second"}
	second.UpdatedAt = now.Add(time.Minute)
	return []catalog.Database{database}, first, second
}

type fakeSource struct {
	states  []pagestoremigration.SourceState
	calls   int
	catalog []catalog.Database
	rows    []row.Row
	routes  []router.Node
}

func (source *fakeSource) Inventory(context.Context) (pagestoremigration.SourceState, error) {
	index := min(source.calls, len(source.states)-1)
	source.calls++
	return source.states[index], nil
}

func (source *fakeSource) Catalog(context.Context) ([]catalog.Database, error) {
	return source.catalog, nil
}

func (source *fakeSource) RowVersions(context.Context) ([]row.Row, error) {
	return append([]row.Row(nil), source.rows...), nil
}

func (source *fakeSource) Routes(context.Context) ([]router.Node, error) {
	return append([]router.Node(nil), source.routes...), nil
}

func sourceState(seed byte, rowCount int) pagestoremigration.SourceState {
	var fingerprint [32]byte
	for index := range fingerprint {
		fingerprint[index] = seed
	}
	counts := make([]pagestoremigration.RecordCount, 0, 10)
	for kind := nativestore.ObjectKindDatabase; kind <= nativestore.ObjectKindConfiguration; kind++ {
		count := uint64(0)
		if kind == nativestore.ObjectKindDatabase || kind == nativestore.ObjectKindTable ||
			kind == nativestore.ObjectKindColumn {
			count = 1
		} else if kind == nativestore.ObjectKindRow {
			count = uint64(rowCount)
		}
		counts = append(counts, pagestoremigration.RecordCount{Kind: kind, Count: count})
	}
	return pagestoremigration.SourceState{Fingerprint: fingerprint, Counts: counts}
}

func countFor(counts []pagestoremigration.RecordCount, kind nativestore.ObjectKind) uint64 {
	for _, count := range counts {
		if count.Kind == kind {
			return count.Count
		}
	}
	return 0
}
