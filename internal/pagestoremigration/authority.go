package pagestoremigration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectlock"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

var ErrAuthorityPoisoned = errors.New("Page Store authority requires reopen recovery")

type authorityPhase string

const (
	phaseCatalogBodyCommitted authorityPhase = "catalog-body-committed"
	phaseCatalogPublished     authorityPhase = "catalog-published"
	phaseRowBodyCommitted     authorityPhase = "row-body-committed"
	phaseRowVersionPublished  authorityPhase = "row-version-published"
	phaseRowCurrentPublished  authorityPhase = "row-current-published"
)

// Authority owns one activated live generation. The native File remains the
// immutable body source; the Page trees decide which body revision is visible.
type Authority struct {
	mu         sync.RWMutex
	directory  string
	file       *nativestore.File
	marker     authorityMarker
	generation *Generation
	reader     *Reader
	catalog    *nativecatalog.IndexedReader
	rows       *nativerow.IndexedReader
	changes    *authorityChangeTree
	poisoned   bool
	closed     bool
	writeGate  chan struct{}
	locks      *objectlock.Manager
	nextOwner  atomic.Uint64
	checkpoint func(authorityPhase) error
}

func OpenAuthority(
	ctx context.Context,
	file *nativestore.File,
	databaseDirectory string,
) (*Authority, error) {
	if ctx == nil || file == nil || databaseDirectory == "" {
		return nil, fmt.Errorf("%w: authority request", ErrInvalid)
	}
	absolute, err := filepath.Abs(databaseDirectory)
	if err != nil {
		return nil, fmt.Errorf("%w: database directory", ErrInvalid)
	}
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("%w: database directory", ErrInvalid)
	}
	reader, err := NewNativeReader(file)
	if err != nil {
		return nil, err
	}
	generationDirectory := filepath.Join(absolute, GenerationDirectory)
	markerPath := filepath.Join(absolute, AuthorityMarkerFilename)
	if _, err := os.Lstat(markerPath); errors.Is(err, os.ErrNotExist) {
		if err := activateAuthority(ctx, reader, absolute, generationDirectory); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("%w: inspect authority marker: %v", ErrTargetCorrupt, err)
	}
	marker, err := decodeAuthorityMarker(absolute)
	if err != nil {
		return nil, err
	}
	generationDirectory = filepath.Join(absolute, marker.Generation)
	manifest, err := readManifest(generationDirectory)
	if err != nil {
		return nil, err
	}
	if err := marker.validate(manifest); err != nil {
		return nil, err
	}
	generation, err := openLiveGeneration(generationDirectory)
	if err != nil {
		return nil, err
	}
	catalogReader, err := nativecatalog.NewIndexedReader(nativecatalog.New(file), generation.catalog)
	if err != nil {
		_ = generation.Close()
		return nil, err
	}
	rowReader, err := nativerow.NewIndexedReader(
		nativerow.New(file), catalogReader, generation.current, generation.versions,
	)
	if err != nil {
		_ = generation.Close()
		return nil, err
	}
	changeTree, err := openAuthorityChangeTree(ctx, absolute, file)
	if err != nil {
		_ = generation.Close()
		return nil, err
	}
	authority := &Authority{
		directory: absolute, file: file, marker: marker, generation: generation,
		reader: reader, catalog: catalogReader, rows: rowReader, changes: changeTree,
		writeGate: make(chan struct{}, 1),
		locks:     objectlock.New(),
	}
	authority.writeGate <- struct{}{}
	if err := authority.reconcile(ctx); err != nil {
		return nil, errors.Join(err, generation.Close(), changeTree.Close())
	}
	return authority, nil
}

func (authority *Authority) BeginRowWrite(
	ctx context.Context, databaseID, tableID string, rowIDs []string,
) (func(), error) {
	if authority == nil || ctx == nil || len(rowIDs) == 0 {
		return nil, fmt.Errorf("%w: Row write lock", ErrInvalid)
	}
	keys := make([]objectlock.Key, 0, len(rowIDs))
	for _, rowID := range rowIDs {
		key, err := objectlock.RowKey(databaseID, tableID, rowID)
		if err != nil {
			return nil, err
		}
		keys = append(keys, key)
	}
	owner := authority.nextOwner.Add(1)
	if owner == 0 {
		return nil, fmt.Errorf("%w: write lock owner exhausted", ErrTargetCorrupt)
	}
	guard, err := authority.locks.Begin(owner)
	if err != nil {
		return nil, err
	}
	if err := guard.TryAcquire(ctx, keys...); err != nil {
		_ = guard.Release()
		return nil, err
	}
	releaseWrite, err := authority.BeginWrite(ctx)
	if err != nil {
		_ = guard.Release()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			releaseWrite()
			_ = guard.Release()
		})
	}, nil
}

func (authority *Authority) BeginWrite(ctx context.Context) (func(), error) {
	if authority == nil || ctx == nil {
		return nil, fmt.Errorf("%w: authority write", ErrInvalid)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-authority.writeGate:
	}
	authority.mu.RLock()
	err := authority.healthyLocked(ctx)
	authority.mu.RUnlock()
	if err != nil {
		authority.writeGate <- struct{}{}
		return nil, err
	}
	var once sync.Once
	return func() { once.Do(func() { authority.writeGate <- struct{}{} }) }, nil
}

func (authority *Authority) Capture(ctx context.Context) (uint64, error) {
	if err := authority.lockRead(ctx); err != nil {
		return 0, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.Capture(ctx)
}

// NextChangeSequence reserves the next logical timeline position while the
// caller holds the Authority operation gate. Failed native transactions do
// not persist the reservation, so retry may reuse the same position.
func (authority *Authority) NextChangeSequence(ctx context.Context) (uint64, error) {
	if err := authority.lockRead(ctx); err != nil {
		return 0, err
	}
	defer authority.mu.RUnlock()
	return nativechange.New(authority.file).NextSequence(0)
}

func (authority *Authority) ListCommittedChanges(
	ctx context.Context,
	databaseID string,
	after, snapshot uint64,
	limit int,
) ([]change.Envelope, uint64, bool, error) {
	if authority == nil || ctx == nil || limit < 1 || limit > 256 {
		return nil, 0, false, fmt.Errorf("%w: committed change list", ErrInvalid)
	}
	release, err := authority.BeginWrite(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	defer release()
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if err := authority.healthyLocked(ctx); err != nil {
		return nil, 0, false, err
	}
	if err := authority.changes.reconcile(ctx, false); err != nil {
		return nil, 0, false, err
	}
	highWater, err := authority.changes.index.HighWater()
	if err != nil {
		return nil, 0, false, err
	}
	if snapshot == 0 {
		snapshot = highWater
	}
	if snapshot > highWater || after > snapshot {
		return nil, 0, false, fmt.Errorf("%w: committed change snapshot", changeindex.ErrInvalid)
	}
	values, more, err := authority.changes.list(ctx, databaseID, after, snapshot, limit)
	return values, snapshot, more, err
}

func (authority *Authority) GetCommittedChange(
	ctx context.Context,
	transactionID, databaseID string,
) (change.Envelope, error) {
	if authority == nil || ctx == nil || transactionID == "" {
		return change.Envelope{}, fmt.Errorf("%w: committed change lookup", ErrInvalid)
	}
	release, err := authority.BeginWrite(ctx)
	if err != nil {
		return change.Envelope{}, err
	}
	defer release()
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if err := authority.healthyLocked(ctx); err != nil {
		return change.Envelope{}, err
	}
	if err := authority.changes.reconcile(ctx, false); err != nil {
		return change.Envelope{}, err
	}
	value, err := authority.changes.get(transactionID)
	if err != nil {
		return change.Envelope{}, err
	}
	if databaseID != "" && !envelopeHasDatabase(value, databaseID) {
		return change.Envelope{}, changeindex.ErrNotFound
	}
	return value, nil
}

func (authority *Authority) Get(
	ctx context.Context, table catalog.Table, rowID string, snapshot uint64,
) (row.Row, error) {
	if err := authority.lockRead(ctx); err != nil {
		return row.Row{}, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.Get(ctx, table, rowID, snapshot)
}

func (authority *Authority) CurrentIncludingDeleted(
	ctx context.Context, table catalog.Table, rowID string,
) (row.Row, error) {
	if err := authority.lockRead(ctx); err != nil {
		return row.Row{}, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.CurrentIncludingDeleted(ctx, table, rowID)
}

func (authority *Authority) ListPage(
	ctx context.Context, table catalog.Table, limit int, snapshot uint64,
) ([]row.Row, bool, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, false, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.ListPage(ctx, table, limit, snapshot)
}

func (authority *Authority) AsOfRevision(
	ctx context.Context, table catalog.Table, rowID string, revision, snapshot uint64,
) (row.Row, error) {
	if err := authority.lockRead(ctx); err != nil {
		return row.Row{}, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.AsOfRevision(ctx, table, rowID, revision, snapshot)
}

func (authority *Authority) AsOfCommit(
	ctx context.Context, table catalog.Table, rowID string, sequence, snapshot uint64,
) (row.Row, error) {
	if err := authority.lockRead(ctx); err != nil {
		return row.Row{}, err
	}
	defer authority.mu.RUnlock()
	return authority.rows.AsOfCommit(ctx, table, rowID, sequence, snapshot)
}

func (authority *Authority) PublishRows(
	ctx context.Context, values []row.Row, commit func() error,
) error {
	if authority == nil || ctx == nil || len(values) == 0 || commit == nil {
		return fmt.Errorf("%w: Row publication", ErrInvalid)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if err := authority.healthyLocked(ctx); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	if err := authority.checkpointPhase(phaseRowBodyCommitted); err != nil {
		return authority.poisonPublication("Row body", err)
	}
	versions := make([]rowversionindex.Locator, 0, len(values))
	current := make([]currentrowindex.Update, 0, len(values))
	for _, value := range values {
		version := rowversionindex.Locator{
			DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
			SchemaRevision: value.SchemaVersion, Revision: value.Revision,
			CommitSequence: value.CommitSequence, State: value.State,
		}
		versions = append(versions, version)
		current = append(current, currentrowindex.Update{
			ExpectedRevision: value.Revision - 1,
			Locator: currentrowindex.Locator{
				DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
				SchemaRevision: value.SchemaVersion, Revision: value.Revision,
				CommitSequence: value.CommitSequence, State: value.State,
			},
		})
	}
	versionID, err := authority.nextTransactionID("versions")
	if err == nil {
		_, err = authority.generation.versions.Append(versionID, versions)
	}
	if err == nil {
		err = authority.checkpointPhase(phaseRowVersionPublished)
	}
	if err == nil {
		currentID, idErr := authority.nextTransactionID("current")
		err = idErr
		if err == nil {
			_, err = authority.generation.current.Apply(currentID, current)
		}
	}
	if err == nil {
		err = authority.checkpointPhase(phaseRowCurrentPublished)
	}
	if err != nil {
		return authority.poisonPublication("Row body", err)
	}
	return nil
}

func (authority *Authority) SnapshotCatalog(ctx context.Context) ([]catalog.Database, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.Snapshot(ctx)
}

func (authority *Authority) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.ShowDatabases(ctx)
}

func (authority *Authority) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	if err := authority.lockRead(ctx); err != nil {
		return catalog.Database{}, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.DescribeDatabase(ctx, name)
}

func (authority *Authority) ShowTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.ShowTables(ctx, databaseName)
}

func (authority *Authority) DescribeTable(
	ctx context.Context, databaseName, tableName string,
) (catalog.Table, error) {
	if err := authority.lockRead(ctx); err != nil {
		return catalog.Table{}, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.DescribeTable(ctx, databaseName, tableName)
}

func (authority *Authority) PublishCatalog(
	ctx context.Context, databases []catalog.Database, commit func() error,
) error {
	if authority == nil || ctx == nil || commit == nil {
		return fmt.Errorf("%w: Catalog publication", ErrInvalid)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if err := authority.healthyLocked(ctx); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	if err := authority.checkpointPhase(phaseCatalogBodyCommitted); err != nil {
		return authority.poisonPublication("Catalog body", err)
	}
	id, err := authority.nextTransactionID("catalog")
	if err == nil {
		_, err = authority.generation.catalog.Replace(id, databases)
	}
	if err == nil {
		err = authority.checkpointPhase(phaseCatalogPublished)
	}
	if err != nil {
		return authority.poisonPublication("Catalog body", err)
	}
	return nil
}

func (authority *Authority) checkpointPhase(phase authorityPhase) error {
	if authority.checkpoint == nil {
		return nil
	}
	return authority.checkpoint(phase)
}

func (authority *Authority) poisonPublication(object string, err error) error {
	authority.poisoned = true
	return fmt.Errorf("%w: %s committed before Page publication completed: %v", ErrOutcomeUnknown, object, err)
}

func (authority *Authority) lockRead(ctx context.Context) error {
	if authority == nil || ctx == nil {
		return fmt.Errorf("%w: authority read", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authority.mu.RLock()
	if err := authority.healthyLocked(ctx); err != nil {
		authority.mu.RUnlock()
		return err
	}
	return nil
}

func (authority *Authority) healthyLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if authority.closed {
		return fmt.Errorf("%w: authority is closed", ErrAuthorityPoisoned)
	}
	if authority.poisoned {
		return ErrAuthorityPoisoned
	}
	return nil
}

func activateAuthority(
	ctx context.Context,
	reader *Reader,
	databaseDirectory, generationDirectory string,
) error {
	plan, err := reader.Build(ctx)
	if err != nil {
		return err
	}
	applier, err := NewApplier(reader, databaseDirectory)
	if err != nil {
		return err
	}
	if _, err := applier.Apply(ctx, plan); err != nil {
		return err
	}
	generation, err := OpenGeneration(generationDirectory)
	if err != nil {
		return err
	}
	verifyErr := verifyGenerationPlan(ctx, generation, plan)
	closeErr := generation.Close()
	if verifyErr != nil || closeErr != nil {
		return errors.Join(verifyErr, closeErr)
	}
	manifest, err := readManifest(generationDirectory)
	if err != nil {
		return err
	}
	marker, err := newAuthorityMarker(manifest)
	if err != nil {
		return err
	}
	return writeAuthorityMarker(databaseDirectory, marker)
}

func (authority *Authority) Generation() *Generation {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.generation
}

func (authority *Authority) reconcile(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	plan, err := authority.reader.Build(ctx)
	if err != nil {
		return err
	}
	if err := authority.replaceCatalog(plan); err != nil {
		return err
	}
	if err := authority.appendVersions(plan); err != nil {
		return err
	}
	return authority.advanceCurrent(plan)
}

func (authority *Authority) replaceCatalog(plan Plan) error {
	id, err := authority.nextTransactionID("catalog")
	if err != nil {
		return err
	}
	_, err = authority.generation.catalog.Replace(id, plan.Catalog)
	return err
}

func (authority *Authority) appendVersions(plan Plan) error {
	if len(plan.RowVersions) == 0 {
		return nil
	}
	id, err := authority.nextTransactionID("versions")
	if err != nil {
		return err
	}
	_, err = authority.generation.versions.Append(id, plan.RowVersions)
	return err
}

func (authority *Authority) advanceCurrent(plan Plan) error {
	updates := make([]currentrowindex.Update, 0, len(plan.CurrentRows))
	for _, locator := range plan.CurrentRows {
		current, err := authority.generation.current.Lookup(locator.TableID, locator.RowID)
		if errors.Is(err, currentrowindex.ErrNotFound) {
			updates = append(updates, currentrowindex.Update{Locator: locator})
			continue
		}
		if err != nil {
			return err
		}
		if current == locator {
			continue
		}
		updates = append(updates, currentrowindex.Update{
			ExpectedRevision: current.Revision, Locator: locator,
		})
	}
	if len(updates) == 0 {
		return nil
	}
	id, err := authority.nextTransactionID("current")
	if err != nil {
		return err
	}
	_, err = authority.generation.current.Apply(id, updates)
	return err
}

func (authority *Authority) nextTransactionID(kind string) (uint64, error) {
	for _, tree := range authority.generation.trees {
		if tree.manifest.Kind != kind {
			continue
		}
		frontier, err := tree.set.DurableFrontier()
		if err != nil {
			return 0, err
		}
		if frontier.LastTransactionID == ^uint64(0) {
			return 0, fmt.Errorf("%w: transaction ID exhausted", ErrTargetCorrupt)
		}
		return frontier.LastTransactionID + 1, nil
	}
	return 0, fmt.Errorf("%w: unknown Tree %q", ErrTargetCorrupt, kind)
}

func (authority *Authority) Close() error {
	if authority == nil {
		return nil
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return nil
	}
	authority.closed = true
	return errors.Join(authority.generation.Close(), authority.changes.Close())
}
