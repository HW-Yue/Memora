package pagestoremigration

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/catalogfulltext"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/routefulltext"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/rowfulltext"
	"github.com/HW-Yue/Memora/internal/rowid"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
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
	// One redo log for the whole generation, created before any Tree: every
	// Tree commits into it, so a write spanning several Trees is one WAL
	// transaction. See docs/storage/shared-circular-redo-v1.md.
	log, err := wal.CreateSegmentSet(filepath.Join(staging, sharedWALDirectory), 0)
	if err != nil {
		return generationManifest{}, fmt.Errorf("create generation redo log: %w", err)
	}
	closeLog := true
	defer func() {
		if closeLog {
			_ = log.Close()
		}
	}()
	for index, specification := range expectedTrees {
		if err := ctx.Err(); err != nil {
			return generationManifest{}, err
		}
		state, err := buildTree(staging, specification, capacity, plan, log)
		if err != nil {
			return generationManifest{}, fmt.Errorf("build %s migration Tree: %w", specification.Kind, err)
		}
		specification.State = treeStateFromRuntime(state)
		manifest.Trees[index] = specification
		phase := []applyPhase{phaseCatalogBuilt, phaseVersionsBuilt, phaseFulltextBuilt}[index]
		if err := applier.checkpoint(ctx, phase); err != nil {
			return generationManifest{}, err
		}
	}
	// One Tree per Table, holding that Table's current Rows. The Table list
	// comes from the Catalog rather than from the Rows, so a Table with no Rows
	// still gets its Tree and the Tree set matches the Catalog exactly.
	// Two Trees per Table: its current Rows, and its history. The Table list
	// comes from the Catalog rather than from the Rows, so a Table with no Rows
	// still gets both Trees and the Tree set matches the Catalog exactly.
	for _, tableID := range planTableIDs(plan) {
		if err := ctx.Err(); err != nil {
			return generationManifest{}, err
		}
		for _, specification := range []treeManifest{tableTreeManifest(tableID), historyTreeManifest(tableID)} {
			state, err := buildTree(staging, specification, capacity, plan, log)
			if err != nil {
				return generationManifest{}, fmt.Errorf("build Table %q migration %s Tree: %w", tableID, specification.Kind, err)
			}
			specification.State = treeStateFromRuntime(state)
			manifest.Trees = append(manifest.Trees, specification)
		}
	}
	if err := applier.checkpoint(ctx, phaseCurrentBuilt); err != nil {
		return generationManifest{}, err
	}
	// The log has to be closed before the content digest is taken: its Segment
	// files are part of the generation's bytes, and OpenGeneration recomputes
	// the same digest from what is on disk.
	closeLog = false
	if err := log.Close(); err != nil {
		return generationManifest{}, fmt.Errorf("close generation redo log: %w", err)
	}
	manifest.ContentDigest, err = contentDigest(staging, manifest)
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
	log *wal.SegmentSet,
) (state treecontrol.State, result error) {
	manager, err := page.Create(filepath.Join(directory, specification.PageFile), specification.SpaceID)
	if err != nil {
		return treecontrol.State{}, err
	}
	defer func() {
		result = errors.Join(result, manager.Close())
	}()
	// Attach rather than open: the log is shared, and by the second Tree it
	// already carries the first Tree's Records. Recovering here would route
	// those Records to a space this call was not given and fail.
	runtime, err := treecommit.AttachRuntime(log, manager, runtimeConfig(specification, capacity))
	if err != nil {
		return treecontrol.State{}, err
	}
	// Transaction IDs are per log, and the log is now shared, so the bootstrap
	// of the second Tree can no longer reuse the first Tree's ID 1 — the Set
	// rejects a duplicate outright. Taking the next ID from the log's frontier
	// keeps them unique across the whole generation.
	frontier, err := log.DurableFrontier()
	if err != nil {
		return treecontrol.State{}, err
	}
	transactionID := frontier.LastTransactionID + 1
	switch specification.Kind {
	case "catalog":
		index, err := catalogindex.Open(runtime)
		if err == nil {
			_, err = index.Replace(transactionID, plan.Catalog)
		}
		if err != nil {
			return treecontrol.State{}, err
		}

	case "versions":
		index, err := rowversionindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(transactionID, plan.RowVersions)
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
				_, err = index.Bootstrap(transactionID, documents)
			}
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	case "current":
		// Pre-v5's shared current Row Tree, keyed by (Table, Row). Nothing
		// builds one any more; the case survives so the upgrade tests can
		// construct the old layout and prove a database in it still opens.
		index, err := currentrowindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(transactionID, plan.CurrentRows, 0)
		}
		if err != nil {
			return treecontrol.State{}, err
		}
	default:
		if tableID, isTable := tableTreeTableID(specification.Kind); isTable {
			locators := tableCurrentLocators(plan, tableID)
			index, err := currentrowindex.Open(runtime)
			if err == nil {
				// Seed the counter from the Rows this Table already holds, so a
				// rebuild never hands out an ID one of them is using.
				rowIDs := make([]string, 0, len(locators))
				for _, locator := range locators {
					rowIDs = append(rowIDs, locator.RowID)
				}
				_, err = index.Bootstrap(transactionID, locators, rowid.HighWater(rowIDs))
			}
			if err != nil {
				return treecontrol.State{}, err
			}
			break
		}
		tableID, isHistory := historyTreeTableID(specification.Kind)
		if !isHistory {
			return treecontrol.State{}, ErrInvalid
		}
		index, err := rowversionindex.Open(runtime)
		if err == nil {
			_, err = index.Bootstrap(transactionID, tableRowVersions(plan, tableID))
		}
		if err != nil {
			return treecontrol.State{}, err
		}
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

// planTableIDs lists every Table the Catalog names, in a stable order so two
// builds of the same plan produce the same Tree order and the same manifest.
func planTableIDs(plan Plan) []string {
	tableIDs := make([]string, 0)
	for _, database := range plan.Catalog {
		for _, table := range database.Tables {
			tableIDs = append(tableIDs, table.ID)
		}
	}
	sort.Strings(tableIDs)
	return tableIDs
}

// tableCurrentLocators picks out the current Rows belonging to one Table.
// tableRowVersions is every revision the plan holds for one Table, which is
// what that Table's history Tree is seeded with. The shared "versions" Tree is
// seeded with all of them; these are the same Locators partitioned by Table.
func tableRowVersions(plan Plan, tableID string) []rowversionindex.Locator {
	locators := make([]rowversionindex.Locator, 0)
	for _, locator := range plan.RowVersions {
		if locator.TableID == tableID {
			locators = append(locators, locator)
		}
	}
	return locators
}

func tableCurrentLocators(plan Plan, tableID string) []currentrowindex.Locator {
	locators := make([]currentrowindex.Locator, 0)
	for _, locator := range plan.CurrentRows {
		if locator.TableID == tableID {
			locators = append(locators, locator)
		}
	}
	return locators
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
	for _, route := range plan.CurrentRoutes {
		aliases := uint64(len(route.Aliases))
		if aliases > (math.MaxUint64-13)/2 {
			return 0, ErrInvalid
		}
		required := uint64(13) + aliases*2
		if objects > math.MaxUint64-required {
			return 0, ErrInvalid
		}
		objects += required
	}
	if objects > math.MaxUint64-128 {
		return 0, ErrInvalid
	}
	return max(uint64(128), objects+128), nil
}

func verifyGenerationPlan(ctx context.Context, generation *Generation, plan Plan) error {
	if generation == nil || generation.catalog == nil ||
		generation.versions == nil || generation.fulltext == nil {
		return ErrTargetCorrupt
	}
	if generation.PlanDigest() != plan.Digest || generation.SourceFingerprint() != plan.SourceFingerprint {
		return ErrConflict
	}
	if err := verifyCatalog(ctx, generation.catalog, plan.Catalog); err != nil {
		return err
	}
	if err := verifyCurrentRows(ctx, generation, plan); err != nil {
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
	routeDocuments, err := routefulltext.Project(plan.CurrentRoutes)
	if err != nil {
		return nil, fmt.Errorf("%w: Route fulltext documents: %v", ErrInvalid, err)
	}
	documents := append(catalogDocuments, routeDocuments...)
	return append(documents, rowValues...), nil
}

// clusteredVersions builds the leaf entries the Row version tree stores. The
// leaf carries the encoded Row, so publishing a revision puts the data in the
// tree rather than a pointer to somewhere else.
// historyLookup returns the metadata a revision was written with. It is a
// function rather than a store handle because the two callers reach it
// differently: the live authority holds the record file, while a generation
// rebuild goes through its Source.
type historyLookup func(value row.Row) (string, error)

func clusteredVersions(
	lookup historyLookup, databases []catalog.Database, rows []row.Row,
) ([]rowversionindex.Locator, error) {
	tables := make(map[string]catalog.Table)
	for _, database := range databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	versions := make([]rowversionindex.Locator, 0, len(rows))
	for _, value := range rows {
		table, exists := tables[value.TableID]
		if !exists {
			return nil, fmt.Errorf("%w: Row %q references an unknown Table", ErrInvalid, value.ID)
		}
		body, err := nativerow.EncodeBody(value, table)
		if err != nil {
			return nil, err
		}
		metadata := ""
		if lookup != nil {
			if metadata, err = lookup(value); err != nil {
				return nil, err
			}
		}
		versions = append(versions, rowversionindex.Locator{
			DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
			SchemaRevision: value.SchemaVersion, Revision: value.Revision,
			CommitSequence: value.CommitSequence, State: value.State,
			Body: string(body), History: metadata,
		})
	}
	return versions, nil
}

// recordFileHistory reads a revision's metadata straight out of the record file.
// Its record ID is derived from the Row's own identity, so publication does not
// need to be handed the metadata separately — it can look up what the write path
// just staged. A revision with no history record (an imported or bootstrapped
// Row) simply carries none.
func recordFileHistory(file *nativestore.File) historyLookup {
	if file == nil {
		return nil
	}
	return func(value row.Row) (string, error) {
		payload, err := file.Get(
			nativestore.ObjectKindHistory, nativerow.HistoryRecordID(value.ID, value.Revision),
		)
		if errors.Is(err, nativestore.ErrNotFound) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return string(payload), nil
	}
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

func projectRouteChangeDocuments(
	databases []catalog.Database, nodes []router.Node,
) ([]fulltext.Document, error) {
	tables := make(map[string]catalog.Table)
	for _, database := range databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	for _, node := range nodes {
		table, exists := tables[node.TableID]
		if !exists || table.DatabaseID != node.DatabaseID {
			return nil, fmt.Errorf("%w: Route fulltext scope", ErrInvalid)
		}
	}
	documents, err := routefulltext.ProjectChanges(nodes)
	if err != nil {
		return nil, fmt.Errorf("%w: Route fulltext document: %v", ErrInvalid, err)
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

// verifyCurrentRows checks every Table's Tree against the plan it was built
// from, and checks the split itself: a Table's Tree must contain that Table's
// Rows and nothing else.
func verifyCurrentRows(ctx context.Context, generation *Generation, plan Plan) error {
	expected := make(map[string]map[string]currentrowindex.Locator)
	for _, locator := range plan.CurrentRows {
		if _, exists := expected[locator.TableID]; !exists {
			expected[locator.TableID] = make(map[string]currentrowindex.Locator)
		}
		expected[locator.TableID][locator.RowID] = locator
	}
	seen := 0
	for _, tableID := range planTableIDs(plan) {
		index := generation.CurrentRowsFor(tableID)
		if index == nil {
			return fmt.Errorf("%w: Table %q has no Tree", ErrTargetCorrupt, tableID)
		}
		for rowID, locator := range expected[tableID] {
			if got, err := index.Lookup(rowID); err != nil || got != locator {
				return fmt.Errorf("%w: current Row %q", ErrTargetCorrupt, rowID)
			}
		}
		after := ""
		for {
			if err := ctx.Err(); err != nil {
				return err
			}
			page, err := index.Page(after, 1000)
			if err != nil {
				return fmt.Errorf("%w: current Table %q cursor", ErrTargetCorrupt, tableID)
			}
			for _, locator := range page.Locators {
				// Reading another Table's Row here would mean the Trees are not
				// actually separate, which is the whole property of the split.
				want, exists := expected[tableID][locator.RowID]
				if !exists || locator != want || locator.TableID != tableID {
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
