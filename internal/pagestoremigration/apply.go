package pagestoremigration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/catalogfulltext"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/rowfulltext"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

type ApplyReceipt struct {
	Directory  string
	PlanDigest string
	Published  bool
	Reused     bool
}

type applyPhase string

const (
	phaseStagingCreated applyPhase = "staging-created"
	phaseCatalogBuilt   applyPhase = "catalog-built"
	phaseCurrentBuilt   applyPhase = "current-built"
	phaseVersionsBuilt  applyPhase = "versions-built"
	phaseFulltextBuilt  applyPhase = "fulltext-built"
	phaseManifestSynced applyPhase = "manifest-synced"
	phaseBeforeReverify applyPhase = "before-source-reverify"
	phaseBeforeRename   applyPhase = "before-rename"
	phaseAfterRename    applyPhase = "after-rename"
)

type applyOperations struct {
	mkdirTemp     func(string, string) (string, error)
	rename        func(string, string) error
	removeAll     func(string) error
	syncDirectory func(string) error
	checkpoint    func(applyPhase) error
}

type Applier struct {
	mu         sync.Mutex
	reader     *Reader
	directory  string
	target     string
	operations applyOperations
}

func NewApplier(reader *Reader, databaseDirectory string) (*Applier, error) {
	return newApplierWithOperations(reader, databaseDirectory, applyOperations{
		mkdirTemp: os.MkdirTemp, rename: os.Rename, removeAll: os.RemoveAll,
		syncDirectory: syncGenerationDirectory,
	})
}

func newApplierWithOperations(
	reader *Reader,
	databaseDirectory string,
	operations applyOperations,
) (*Applier, error) {
	if reader == nil || reader.source == nil || databaseDirectory == "" ||
		operations.mkdirTemp == nil || operations.rename == nil ||
		operations.removeAll == nil || operations.syncDirectory == nil {
		return nil, fmt.Errorf("%w: Applier configuration", ErrInvalid)
	}
	absolute, err := filepath.Abs(databaseDirectory)
	if err != nil {
		return nil, fmt.Errorf("%w: database directory", ErrInvalid)
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: database directory", ErrInvalid)
	}
	return &Applier{
		reader: reader, directory: absolute,
		target: filepath.Join(absolute, GenerationDirectory), operations: operations,
	}, nil
}

func (applier *Applier) Apply(ctx context.Context, plan Plan) (receipt ApplyReceipt, result error) {
	if applier == nil || applier.reader == nil || ctx == nil {
		return ApplyReceipt{}, fmt.Errorf("%w: Apply request", ErrInvalid)
	}
	applier.mu.Lock()
	defer applier.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ApplyReceipt{}, err
	}
	if err := plan.Validate(); err != nil {
		return ApplyReceipt{}, err
	}
	if err := applier.reader.VerifySource(ctx, plan); err != nil {
		return ApplyReceipt{}, err
	}
	if _, err := os.Lstat(applier.target); err == nil {
		return applier.reuse(ctx, plan)
	} else if !errors.Is(err, os.ErrNotExist) {
		return ApplyReceipt{}, fmt.Errorf("inspect generation target: %w", err)
	}

	staging, err := applier.operations.mkdirTemp(applier.directory, ".page-index-v1.staging-")
	if err != nil {
		return ApplyReceipt{}, fmt.Errorf("create generation staging directory: %w", err)
	}
	defer func() {
		if err := applier.operations.removeAll(staging); err != nil {
			result = errors.Join(result, fmt.Errorf("clean generation staging directory: %w", err))
		}
	}()
	if err := applier.checkpoint(ctx, phaseStagingCreated); err != nil {
		return ApplyReceipt{}, err
	}
	manifest, err := applier.build(ctx, staging, plan)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if err := applier.checkpoint(ctx, phaseBeforeReverify); err != nil {
		return ApplyReceipt{}, err
	}
	if err := applier.reader.VerifySource(ctx, plan); err != nil {
		return ApplyReceipt{}, err
	}
	if err := applier.checkpoint(ctx, phaseBeforeRename); err != nil {
		return ApplyReceipt{}, err
	}
	if err := applier.operations.rename(staging, applier.target); err != nil {
		if _, inspectErr := os.Lstat(applier.target); inspectErr == nil {
			return applier.reuse(ctx, plan)
		}
		return ApplyReceipt{}, fmt.Errorf("publish Page index generation: %w", err)
	}
	if err := applier.checkpoint(ctx, phaseAfterRename); err != nil {
		return ApplyReceipt{}, fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	if err := applier.operations.syncDirectory(applier.directory); err != nil {
		return ApplyReceipt{}, fmt.Errorf("%w: sync database directory: %v", ErrOutcomeUnknown, err)
	}
	return ApplyReceipt{
		Directory: applier.target, PlanDigest: manifest.PlanDigest, Published: true,
	}, nil
}

func (applier *Applier) build(
	ctx context.Context,
	staging string,
	plan Plan,
) (generationManifest, error) {
	capacity, err := migrationCapacity(plan)
	if err != nil {
		return generationManifest{}, err
	}
	manifest := generationManifest{
		Version: generationVersion, PlanVersion: plan.Version,
		PlanDigest: plan.Digest, SourceFingerprint: plan.SourceFingerprint,
		Trees: make([]treeManifest, len(expectedTrees)),
	}
	for index, specification := range expectedTrees {
		if err := ctx.Err(); err != nil {
			return generationManifest{}, err
		}
		state, err := buildTree(staging, specification, capacity, plan)
		if err != nil {
			return generationManifest{}, fmt.Errorf("build %s migration Tree: %w", specification.Kind, err)
		}
		specification.State = treeStateFromRuntime(state)
		manifest.Trees[index] = specification
		phase := []applyPhase{phaseCatalogBuilt, phaseCurrentBuilt, phaseVersionsBuilt, phaseFulltextBuilt}[index]
		if err := applier.checkpoint(ctx, phase); err != nil {
			return generationManifest{}, err
		}
	}
	manifest.ContentDigest, err = contentDigest(staging, expectedTrees)
	if err != nil {
		return generationManifest{}, err
	}
	if err := writeManifest(staging, manifest); err != nil {
		return generationManifest{}, err
	}
	if err := applier.checkpoint(ctx, phaseManifestSynced); err != nil {
		return generationManifest{}, err
	}
	generation, err := OpenGeneration(staging)
	if err != nil {
		return generationManifest{}, err
	}
	verifyErr := verifyGenerationPlan(ctx, generation, plan)
	closeErr := generation.Close()
	if verifyErr != nil || closeErr != nil {
		return generationManifest{}, errors.Join(verifyErr, closeErr)
	}
	return manifest, nil
}

func (applier *Applier) reuse(ctx context.Context, plan Plan) (ApplyReceipt, error) {
	generation, err := OpenGeneration(applier.target)
	if err != nil {
		return ApplyReceipt{}, err
	}
	if generation.PlanDigest() != plan.Digest ||
		generation.SourceFingerprint() != plan.SourceFingerprint {
		_ = generation.Close()
		return ApplyReceipt{}, ErrConflict
	}
	verifyErr := verifyGenerationPlan(ctx, generation, plan)
	closeErr := generation.Close()
	if verifyErr != nil || closeErr != nil {
		return ApplyReceipt{}, errors.Join(verifyErr, closeErr)
	}
	// Re-syncing the parent is required for both outcome-unknown retry and a
	// concurrent publisher observed immediately after its rename.
	if err := applier.operations.syncDirectory(applier.directory); err != nil {
		return ApplyReceipt{}, fmt.Errorf("%w: sync reused generation directory: %v", ErrOutcomeUnknown, err)
	}
	return ApplyReceipt{
		Directory: applier.target, PlanDigest: plan.Digest, Reused: true,
	}, nil
}

func (applier *Applier) checkpoint(ctx context.Context, phase applyPhase) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if applier.operations.checkpoint != nil {
		if err := applier.operations.checkpoint(phase); err != nil {
			return fmt.Errorf("Page index migration %s: %w", phase, err)
		}
	}
	return nil
}

func buildTree(
	directory string,
	specification treeManifest,
	capacity uint64,
	plan Plan,
) (state treecontrol.State, result error) {
	set, err := wal.CreateSegmentSet(filepath.Join(directory, specification.WALDirectory), 0)
	if err != nil {
		return treecontrol.State{}, err
	}
	manager, err := page.Create(filepath.Join(directory, specification.PageFile), specification.SpaceID)
	if err != nil {
		_ = set.Close()
		return treecontrol.State{}, err
	}
	defer func() {
		result = errors.Join(result, set.Close(), manager.Close())
	}()
	runtime, _, err := treecommit.OpenRuntime(set, manager, treecommit.RuntimeConfig{
		SpaceID: specification.SpaceID, Capacity: capacity, OldFrames: max(uint64(1), capacity/2),
	})
	if err != nil {
		return treecontrol.State{}, err
	}
	switch specification.Kind {
	case "catalog":
		index, err := catalogindex.Open(runtime)
		if err == nil {
			_, err = index.Replace(1, plan.Catalog)
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	case "current":
		index, err := currentrowindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(1, plan.CurrentRows)
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	case "versions":
		index, err := rowversionindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(1, plan.RowVersions)
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	case "fulltext":
		index, err := fulltextindex.Open(runtime)
		if err == nil {
			var documents []fulltext.Document
			documents, err = generationDocuments(plan)
			if err == nil {
				_, err = index.Bootstrap(1, documents)
			}
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	default:
		return treecontrol.State{}, ErrInvalid
	}
	report, err := runtime.FlushDirty(math.MaxUint64)
	if err != nil || report.Remaining != 0 {
		return treecontrol.State{}, errors.Join(err, fmt.Errorf("dirty Pages remaining: %d", report.Remaining))
	}
	if err := manager.Sync(); err != nil {
		return treecontrol.State{}, err
	}
	return runtime.State(), nil
}

func migrationCapacity(plan Plan) (uint64, error) {
	objects := uint64(len(plan.CurrentRows))
	for _, body := range plan.CurrentRowBodies {
		if objects > math.MaxUint64-1-uint64(len(body.Values))*2 {
			return 0, ErrInvalid
		}
		objects += 1 + uint64(len(body.Values))*2
	}
	if uint64(len(plan.RowVersions)) > (math.MaxUint64-objects)/4 {
		return 0, ErrInvalid
	}
	objects += uint64(len(plan.RowVersions)) * 4
	for _, database := range plan.Catalog {
		if objects > math.MaxUint64-1-uint64(len(database.Tables)) {
			return 0, ErrInvalid
		}
		objects++
		for _, table := range database.Tables {
			if objects > math.MaxUint64-1-uint64(len(table.Columns)) {
				return 0, ErrInvalid
			}
			objects += 1 + uint64(len(table.Columns))
		}
	}
	if objects > math.MaxUint64-128 {
		return 0, ErrInvalid
	}
	return max(uint64(128), objects+128), nil
}

func verifyGenerationPlan(ctx context.Context, generation *Generation, plan Plan) error {
	if generation == nil || generation.catalog == nil || generation.current == nil ||
		generation.versions == nil || generation.fulltext == nil {
		return ErrTargetCorrupt
	}
	if generation.PlanDigest() != plan.Digest || generation.SourceFingerprint() != plan.SourceFingerprint {
		return ErrConflict
	}
	if err := verifyCatalog(ctx, generation.catalog, plan.Catalog); err != nil {
		return err
	}
	if err := verifyCurrentRows(ctx, generation.current, plan); err != nil {
		return err
	}
	if err := verifyRowVersions(ctx, generation.versions, plan); err != nil {
		return err
	}
	return verifyFulltext(ctx, generation.fulltext, plan)
}

func rowDocuments(plan Plan) ([]fulltext.Document, error) {
	return projectRowDocuments(plan.Catalog, plan.CurrentRowBodies)
}

func generationDocuments(plan Plan) ([]fulltext.Document, error) {
	catalogDocuments, err := catalogfulltext.Project(plan.Catalog)
	if err != nil {
		return nil, fmt.Errorf("%w: Catalog fulltext documents: %v", ErrInvalid, err)
	}
	rowValues, err := rowDocuments(plan)
	if err != nil {
		return nil, err
	}
	return append(catalogDocuments, rowValues...), nil
}

func projectRowDocuments(databases []catalog.Database, bodies []row.Row) ([]fulltext.Document, error) {
	tables := make(map[string]catalog.Table)
	for _, database := range databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	documents := make([]fulltext.Document, 0, len(bodies))
	seen := make(map[string]struct{}, len(bodies))
	for _, body := range bodies {
		if _, duplicate := seen[body.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Row fulltext document", ErrInvalid)
		}
		seen[body.ID] = struct{}{}
		table, exists := tables[body.TableID]
		if !exists {
			return nil, ErrInvalid
		}
		document, err := rowfulltext.Project(table, body)
		if err != nil {
			return nil, fmt.Errorf("%w: Row fulltext document: %v", ErrInvalid, err)
		}
		documents = append(documents, document)
	}
	return documents, nil
}

func verifyFulltext(ctx context.Context, index *fulltextindex.Index, plan Plan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	documents, err := generationDocuments(plan)
	if err != nil {
		return err
	}
	reference, err := fulltext.Build(documents)
	if err != nil {
		return err
	}
	got, err := index.AllPostings()
	if err != nil {
		return fmt.Errorf("%w: Fulltext Tree: %v", ErrTargetCorrupt, err)
	}
	want := reference.AllPostings()
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%w: Fulltext postings disagree with Plan", ErrTargetCorrupt)
	}
	return nil
}

func verifyCatalog(ctx context.Context, index *catalogindex.Index, databases []catalog.Database) error {
	for _, database := range databases {
		if err := ctx.Err(); err != nil {
			return err
		}
		wantDatabase := catalogindex.Locator{
			Kind: catalogindex.KindDatabase, ID: database.ID, SchemaRevision: database.SchemaVersion,
		}
		if got, err := index.DatabaseByID(database.ID); err != nil || got != wantDatabase {
			return fmt.Errorf("%w: Database %q locator", ErrTargetCorrupt, database.ID)
		}
		for _, name := range append([]string{database.Name}, database.Aliases...) {
			if got, err := index.DatabaseByName(name); err != nil || got != wantDatabase {
				return fmt.Errorf("%w: Database %q name", ErrTargetCorrupt, database.ID)
			}
		}
		for _, table := range database.Tables {
			wantTable := catalogindex.Locator{
				Kind: catalogindex.KindTable, ID: table.ID, DatabaseID: database.ID,
				SchemaRevision: table.SchemaVersion,
			}
			if got, err := index.TableByID(table.ID); err != nil || got != wantTable {
				return fmt.Errorf("%w: Table %q locator", ErrTargetCorrupt, table.ID)
			}
			for _, name := range append([]string{table.Name}, table.Aliases...) {
				if got, err := index.TableByName(database.ID, name); err != nil || got != wantTable {
					return fmt.Errorf("%w: Table %q name", ErrTargetCorrupt, table.ID)
				}
			}
			columns, err := index.ColumnsForTable(table.ID)
			if err != nil || len(columns) != len(table.Columns) {
				return fmt.Errorf("%w: Table %q Columns", ErrTargetCorrupt, table.ID)
			}
			for _, column := range table.Columns {
				wantColumn := catalogindex.Locator{
					Kind: catalogindex.KindColumn, ID: column.ID, DatabaseID: database.ID,
					TableID: table.ID, SchemaRevision: column.SchemaVersion,
				}
				if got, err := index.ColumnByID(column.ID); err != nil || got != wantColumn {
					return fmt.Errorf("%w: Column %q locator", ErrTargetCorrupt, column.ID)
				}
				for _, name := range append([]string{column.Name}, column.Aliases...) {
					if got, err := index.ColumnByName(table.ID, name); err != nil || got != wantColumn {
						return fmt.Errorf("%w: Column %q name", ErrTargetCorrupt, column.ID)
					}
				}
			}
		}
	}
	return nil
}

func verifyCurrentRows(ctx context.Context, index *currentrowindex.Index, plan Plan) error {
	expected := make(map[string]map[string]currentrowindex.Locator)
	for _, locator := range plan.CurrentRows {
		if _, exists := expected[locator.TableID]; !exists {
			expected[locator.TableID] = make(map[string]currentrowindex.Locator)
		}
		expected[locator.TableID][locator.RowID] = locator
		if got, err := index.Lookup(locator.TableID, locator.RowID); err != nil || got != locator {
			return fmt.Errorf("%w: current Row %q", ErrTargetCorrupt, locator.RowID)
		}
	}
	seen := 0
	for _, database := range plan.Catalog {
		for _, table := range database.Tables {
			after := ""
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				page, err := index.Page(table.ID, after, 1000)
				if err != nil {
					return fmt.Errorf("%w: current Table %q cursor", ErrTargetCorrupt, table.ID)
				}
				for _, locator := range page.Locators {
					want, exists := expected[table.ID][locator.RowID]
					if !exists || locator != want {
						return fmt.Errorf("%w: unexpected current Row %q", ErrTargetCorrupt, locator.RowID)
					}
					seen++
				}
				if !page.HasMore {
					break
				}
				after = page.NextAfterRowID
			}
		}
	}
	if seen != len(plan.CurrentRows) {
		return fmt.Errorf("%w: current Row count", ErrTargetCorrupt)
	}
	return nil
}

func verifyRowVersions(ctx context.Context, index *rowversionindex.Index, plan Plan) error {
	highWater := uint64(0)
	for _, locator := range plan.RowVersions {
		if err := ctx.Err(); err != nil {
			return err
		}
		if got, err := index.ByRevision(locator.RowID, locator.Revision); err != nil || got != locator {
			return fmt.Errorf("%w: Row %q revision %d", ErrTargetCorrupt, locator.RowID, locator.Revision)
		}
		highWater = max(highWater, locator.CommitSequence)
	}
	if got, err := index.HighWater(); err != nil || got != highWater {
		return fmt.Errorf("%w: version high-water", ErrTargetCorrupt)
	}
	for _, current := range plan.CurrentRows {
		version := rowversionindex.Locator{
			DatabaseID: current.DatabaseID, TableID: current.TableID, RowID: current.RowID,
			SchemaRevision: current.SchemaRevision, Revision: current.Revision,
			CommitSequence: current.CommitSequence, State: current.State,
		}
		got, err := index.VisibleAt(current.RowID, highWater)
		if err != nil || got != version {
			return fmt.Errorf("%w: visible Row %q", ErrTargetCorrupt, current.RowID)
		}
	}
	return nil
}
