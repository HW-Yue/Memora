package pagestoremigration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestPrePublicationFaultsLeaveNoVisibleOrStagedGeneration(t *testing.T) {
	phases := []applyPhase{
		phaseStagingCreated, phaseCatalogBuilt, phaseCurrentBuilt,
		phaseVersionsBuilt, phaseFulltextBuilt, phaseManifestSynced, phaseBeforeRename,
	}
	for _, phase := range phases {
		t.Run(string(phase), func(t *testing.T) {
			directory := t.TempDir()
			reader, plan, _ := faultPlan(t)
			injected := errors.New("injected migration fault")
			operations := testApplyOperations()
			operations.checkpoint = func(current applyPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			applier, err := newApplierWithOperations(reader, directory, operations)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := applier.Apply(context.Background(), plan); !errors.Is(err, injected) {
				t.Fatalf("Apply(%s fault) error = %v", phase, err)
			}
			assertNoGenerationOrStaging(t, directory)
		})
	}
}

func TestSourceChangeBeforePublishRejectsPlanAndCleansStaging(t *testing.T) {
	directory := t.TempDir()
	reader, plan, source := faultPlan(t)
	operations := testApplyOperations()
	operations.checkpoint = func(phase applyPhase) error {
		if phase == phaseBeforeReverify {
			source.changeFingerprint()
		}
		return nil
	}
	applier, _ := newApplierWithOperations(reader, directory, operations)
	if _, err := applier.Apply(context.Background(), plan); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("source-changed Apply() error = %v", err)
	}
	assertNoGenerationOrStaging(t, directory)
}

func TestRenameThenSyncFaultIsOutcomeUnknownAndRetryConverges(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*applyOperations)
	}{
		{"after rename", func(operations *applyOperations) {
			operations.checkpoint = func(phase applyPhase) error {
				if phase == phaseAfterRename {
					return errors.New("crash after rename")
				}
				return nil
			}
		}},
		{"parent sync", func(operations *applyOperations) {
			operations.syncDirectory = func(string) error { return errors.New("parent sync fault") }
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			reader, plan, _ := faultPlan(t)
			operations := testApplyOperations()
			test.configure(&operations)
			applier, _ := newApplierWithOperations(reader, directory, operations)
			if _, err := applier.Apply(context.Background(), plan); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("outcome-unknown Apply() error = %v", err)
			}
			if _, err := os.Stat(filepath.Join(directory, GenerationDirectory)); err != nil {
				t.Fatalf("published generation is missing: %v", err)
			}
			retry, _ := NewApplier(reader, directory)
			receipt, err := retry.Apply(context.Background(), plan)
			if err != nil || !receipt.Reused || receipt.Published {
				t.Fatalf("retry Apply() = %+v, %v", receipt, err)
			}
			assertNoStaging(t, directory)
		})
	}
}

func TestCancelledApplyAndMutatedPlanPublishNothing(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	applier, _ := NewApplier(reader, directory)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := applier.Apply(ctx, plan); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Apply() error = %v", err)
	}
	plan.CurrentRows[0].Revision++
	if _, err := applier.Apply(context.Background(), plan); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mutated Plan Apply() error = %v", err)
	}
	assertNoGenerationOrStaging(t, directory)
}

func TestConcurrentAppliersPublishExactlyOneEquivalentGeneration(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	const workers = 8
	var published atomic.Uint64
	var reused atomic.Uint64
	var wait sync.WaitGroup
	for index := 0; index < workers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			applier, err := NewApplier(reader, directory)
			if err != nil {
				t.Errorf("NewApplier() error = %v", err)
				return
			}
			receipt, err := applier.Apply(context.Background(), plan)
			if err != nil {
				t.Errorf("concurrent Apply() error = %v", err)
				return
			}
			if receipt.Published {
				published.Add(1)
			}
			if receipt.Reused {
				reused.Add(1)
			}
		}()
	}
	wait.Wait()
	if published.Load() != 1 || reused.Load() != workers-1 {
		t.Fatalf("concurrent receipts = published %d reused %d", published.Load(), reused.Load())
	}
	generation, err := OpenGeneration(filepath.Join(directory, GenerationDirectory))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyGenerationPlan(context.Background(), generation, plan); err != nil {
		t.Fatal(err)
	}
	_ = generation.Close()
	assertNoStaging(t, directory)
}

func testApplyOperations() applyOperations {
	return applyOperations{
		mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
		syncDirectory: syncGenerationDirectory,
	}
}

func assertNoGenerationOrStaging(t *testing.T, directory string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(directory, GenerationDirectory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("generation target exists after failed Apply: %v", err)
	}
	assertNoStaging(t, directory)
}

func assertNoStaging(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".page-index-v1.staging-") {
			t.Fatalf("staging residue %q", entry.Name())
		}
	}
}

type faultMigrationSource struct {
	mu        sync.Mutex
	state     SourceState
	databases []catalog.Database
	rows      []row.Row
	routes    []router.Node
	relations []relation.Relation
}

func (source *faultMigrationSource) Inventory(context.Context) (SourceState, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	return SourceState{
		Fingerprint: source.state.Fingerprint,
		Counts:      append([]RecordCount(nil), source.state.Counts...),
	}, nil
}

func (source *faultMigrationSource) Catalog(context.Context) ([]catalog.Database, error) {
	return source.databases, nil
}

func (source *faultMigrationSource) RowVersions(context.Context) ([]row.Row, error) {
	return append([]row.Row(nil), source.rows...), nil
}

func (source *faultMigrationSource) Routes(context.Context) ([]router.Node, error) {
	return append([]router.Node(nil), source.routes...), nil
}

func (source *faultMigrationSource) Relations(context.Context) ([]relation.Relation, error) {
	return append([]relation.Relation(nil), source.relations...), nil
}

func (source *faultMigrationSource) changeFingerprint() {
	source.mu.Lock()
	source.state.Fingerprint[0]++
	source.mu.Unlock()
}

func faultPlan(t *testing.T) (*Reader, Plan, *faultMigrationSource) {
	t.Helper()
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	column := catalog.Column{
		ID: "col_title", Name: "title", Aliases: []string{"heading"}, Type: "TEXT",
		MaxCharacters: 100, Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
	}
	table := catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{"memos"},
		Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1,
		CreatedAt: now, UpdatedAt: now, Columns: []catalog.Column{column},
	}
	database := catalog.Database{
		ID: "db_work", Name: "work", Aliases: []string{"job"}, Purpose: "Work", Scope: "Personal",
		SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table},
	}
	routes := []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: database.ID, TableID: table.ID,
			Name: "root", Path: "/", Kind: router.KindRoot, Purpose: "All notes", Revision: 1},
		{Version: router.Version, ID: "route_branch", DatabaseID: database.ID, TableID: table.ID,
			ParentID: "route_root", Name: "architecture", Path: "/architecture", Kind: router.KindBranch,
			Purpose: "Architecture decisions", Synopsis: "System boundaries", Revision: 2},
		{Version: router.Version, ID: "route_leaf", DatabaseID: database.ID, TableID: table.ID,
			ParentID: "route_branch", Name: "storage", Path: "/architecture/storage", Kind: router.KindLeaf,
			Purpose: "Storage decisions", Revision: 1},
	}
	first := row.Row{
		ID: "row_one", DatabaseID: database.ID, TableID: table.ID, SchemaVersion: 1,
		Revision: 1, CommitSequence: 10, State: row.StateLive,
		Values: map[string]any{"col_title": "first"}, CreatedAt: now, UpdatedAt: now,
	}
	second := first
	second.Revision, second.CommitSequence, second.State = 2, 20, row.StateDeleted
	second.UpdatedAt = now.Add(time.Minute)
	counts := make([]RecordCount, 0, 10)
	for kind := nativestore.ObjectKindDatabase; kind <= nativestore.ObjectKindConfiguration; kind++ {
		count := uint64(0)
		switch kind {
		case nativestore.ObjectKindDatabase, nativestore.ObjectKindTable, nativestore.ObjectKindColumn:
			count = 1
		case nativestore.ObjectKindRow:
			count = 2
		case nativestore.ObjectKindRoute:
			for _, route := range routes {
				count += route.Revision
			}
		}
		counts = append(counts, RecordCount{Kind: kind, Count: count})
	}
	source := &faultMigrationSource{
		state:     SourceState{Fingerprint: [32]byte{1}, Counts: counts},
		databases: []catalog.Database{database}, rows: []row.Row{first, second}, routes: routes,
	}
	reader, err := NewReader(source)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reader.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return reader, plan, source
}
