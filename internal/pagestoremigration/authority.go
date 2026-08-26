package pagestoremigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/catalogfulltext"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/routefulltext"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/fulltextindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectlock"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

var ErrAuthorityPoisoned = errors.New("Page Store authority requires reopen recovery")

var errFulltextRebuildRequired = errors.New("Fulltext generation requires COW rebuild")

type authorityPhase string

const (
	phaseCatalogBodyCommitted     authorityPhase = "catalog-body-committed"
	phaseCatalogPublished         authorityPhase = "catalog-published"
	phaseCatalogFulltextPublished authorityPhase = "catalog-fulltext-published"
	phaseRowBodyCommitted         authorityPhase = "row-body-committed"
	phaseRowVersionPublished      authorityPhase = "row-version-published"
	phaseRowFulltextPublished     authorityPhase = "row-fulltext-published"
	phaseRowCurrentPublished      authorityPhase = "row-current-published"
	phaseRouteBodyCommitted       authorityPhase = "route-body-committed"
	phaseRouteFulltextPublished   authorityPhase = "route-fulltext-published"
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
	// poisonedAll marks failures whose blast radius really is the whole
	// Instance: generation replacement and undeterminable Catalog transitions.
	// poisonedDatabases marks failures confined to named Databases. Reads are
	// never gated on either: generations are swapped under the same lock
	// readers hold, so a reader always sees one complete generation, and
	// refusing reads turns one uncertain write into a dead Instance.
	poisonedAll       bool
	poisonedDatabases map[string]struct{}
	// redoMaintenanceErr holds the last failed redo log maintenance round. It
	// never blocks a write — the write it followed was already committed — but
	// it must not vanish either, so RedoMaintenanceError exposes it.
	redoMaintenanceErr error
	// fulltextCatchUpErr holds the last failed Fulltext catch-up. Like the redo
	// maintenance error it never blocks a write, and the read-time trigger
	// retries, but it must not vanish silently.
	fulltextCatchUpErr error
	closed             bool
	writeGate          chan struct{}
	locks              *objectlock.Manager
	nextOwner          atomic.Uint64
	checkpoint         func(authorityPhase) error
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
	reconcileErr := authority.reconcile(ctx)
	if reconcileErr != nil && !errors.Is(reconcileErr, errFulltextRebuildRequired) {
		return nil, errors.Join(reconcileErr, generation.Close(), changeTree.Close())
	}
	// A generation with one redo log per Tree cannot publish atomically across
	// Trees, so it is upgraded on sight rather than written to. The COW rebuild
	// produces a v4 generation with one shared log; the old one is left intact.
	if reconcileErr != nil || authority.generation.fulltext == nil ||
		authority.generation.log == nil {
		if _, err := authority.ReplaceGeneration(ctx); err != nil {
			return nil, errors.Join(err, authority.Close())
		}
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
	// Fail early on a poisoned Database instead of doing the work and only
	// discovering it at publication time.
	authority.mu.RLock()
	writable := authority.writableLocked(ctx, databaseID)
	authority.mu.RUnlock()
	if writable != nil {
		_ = guard.Release()
		return nil, writable
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
	err := authority.writableLocked(ctx)
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
	if err := authority.openLocked(ctx); err != nil {
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
	if err := authority.openLocked(ctx); err != nil {
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

// History joins the two halves of a Row's story: the versions supply the data,
// and the Change Log supplies the attribution of the transaction that wrote each
// one. Attribution lives once per transaction, so a write touching many Rows
// reports the same actor and reason for all of them.
//
// A revision whose change sequence is zero predates that link; it falls back to
// the per-Row History record those Databases still hold.
func (authority *Authority) History(
	ctx context.Context, table catalog.Table, rowID string,
) ([]history.Record, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	versions, err := authority.rows.HistoryVersions(ctx, table, rowID)
	if err != nil {
		return nil, err
	}
	records := make([]history.Record, 0, len(versions))
	for _, value := range versions {
		if record, ok := authority.attribute(value); ok {
			records = append(records, record)
			continue
		}
		record, ok := authority.rows.LegacyHistoryRecord(value)
		if !ok {
			return nil, fmt.Errorf(
				"%w: revision %d of Row %q has no attribution",
				ErrTargetCorrupt, value.Revision, value.ID,
			)
		}
		records = append(records, record)
	}
	return records, nil
}

// attribute resolves one revision's attribution through the change sequence it
// carries. The caller holds the read lock.
func (authority *Authority) attribute(value row.Row) (history.Record, bool) {
	if authority.changes == nil || value.ChangeSequence == 0 {
		return history.Record{}, false
	}
	envelope, err := authority.changes.getBySequence(value.ChangeSequence)
	if err != nil {
		return history.Record{}, false
	}
	return nativerow.HistoryRecordFromEnvelope(value, envelope)
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
	return authority.PublishMutation(ctx, values, nil, commit)
}

func (authority *Authority) PublishMutation(
	ctx context.Context, rows []row.Row, routes []router.Node, commit func() error,
) error {
	if authority == nil || ctx == nil || len(rows)+len(routes) == 0 || commit == nil {
		return fmt.Errorf("%w: Row/Route publication", ErrInvalid)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	affected := mutationDatabaseIDs(rows, routes)
	if err := authority.writableLocked(ctx, affected...); err != nil {
		return err
	}
	databases, err := authority.catalog.Snapshot(ctx)
	if err != nil {
		return err
	}
	// Projected, then discarded. The Fulltext index is derived and catches up
	// from the change log, so nothing here writes it — but a Row that cannot be
	// projected at all is a Row the caller should not be allowed to commit, and
	// finding that out after the commit would only leave a mess to reconcile.
	if _, err := projectRowDocuments(databases, rows); err != nil {
		return err
	}
	if _, err := projectRouteChangeDocuments(databases, routes); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	if len(rows) != 0 {
		if err := authority.checkpointPhase(phaseRowBodyCommitted); err != nil {
			return authority.poisonPublication("Row/Route body", affected, err)
		}
	}
	if len(routes) != 0 {
		if err := authority.checkpointPhase(phaseRouteBodyCommitted); err != nil {
			return authority.poisonPublication("Row/Route body", affected, err)
		}
	}
	versions, err := clusteredVersions(recordFileHistory(authority.file), databases, rows)
	if err != nil {
		return err
	}
	current := make([]currentrowindex.Update, 0, len(rows))
	for _, value := range rows {
		current = append(current, currentrowindex.Update{
			ExpectedRevision: value.Revision - 1,
			Locator: currentrowindex.Locator{
				DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
				SchemaRevision: value.SchemaVersion, Revision: value.Revision,
				CommitSequence: value.CommitSequence, State: value.State,
			},
		})
	}
	// One WAL transaction for all three Trees. Before this the versions,
	// fulltext and current Trees were committed one after another, and a fault
	// between them left them describing different Rows — see
	// docs/storage/shared-circular-redo-v1.md §2.1.
	transactionID, err := authority.nextGroupTransactionID()
	if err == nil {
		err = treecommit.CommitGroupFunc(transactionID, func(group *treecommit.Group) error {
			if len(rows) != 0 {
				if err := authority.generation.versions.StageAppend(group, versions); err != nil {
					return err
				}
			}
			if len(rows) != 0 {
				return authority.generation.current.StageApply(group, current)
			}
			return nil
		})
	}
	// The phases now all fire after the single commit. They no longer mark
	// points a publication could be torn at — there is only one — but they stay
	// as fault-injection seams, and the tests use them to prove exactly that:
	// a fault at any of them leaves the three Trees agreeing.
	if err == nil && len(rows) != 0 {
		err = authority.checkpointPhase(phaseRowVersionPublished)
	}
	if err == nil && len(rows) != 0 {
		err = authority.checkpointPhase(phaseRowFulltextPublished)
	}
	if err == nil && len(routes) != 0 {
		err = authority.checkpointPhase(phaseRouteFulltextPublished)
	}
	if err == nil && len(rows) != 0 {
		err = authority.checkpointPhase(phaseRowCurrentPublished)
	}
	if err != nil {
		return authority.poisonPublication("Row/Route body", affected, err)
	}
	authority.catchUpFulltextAfterWrite(ctx)
	authority.maintainRedoLog()
	return nil
}

// maintainRedoLog runs one redo log maintenance round after a successful
// publication.
//
// It never returns an error, on purpose: the write it follows is already
// committed and durable, and failing it here would turn a successful write into
// a reported failure for a reason the caller can do nothing about. A failure
// still must not be silent, so it is recorded on the Authority and surfaced by
// Capture; the next publication tries again, and the worst case is the log
// keeps growing — which is exactly where this started, not something worse.
// RedoMaintenanceError reports the last failed redo log maintenance round, or
// nil when the last round succeeded. A failure here never failed a write.
func (authority *Authority) RedoMaintenanceError() error {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.redoMaintenanceErr
}

func (authority *Authority) maintainRedoLog() {
	if err := authority.generation.maintainRedoLog(); err != nil {
		authority.redoMaintenanceErr = err
		return
	}
	authority.redoMaintenanceErr = nil
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
	if err := authority.openLocked(ctx); err != nil {
		return err
	}
	current, err := authority.catalog.Snapshot(ctx)
	if err != nil {
		return err
	}
	// Only the Databases whose Catalog entry actually changes are left uncertain
	// by a failed publication; an undeterminable diff fails closed Instance-wide.
	affected, determined := changedDatabaseIDs(current, databases)
	if !determined {
		affected = nil
	}
	if err := authority.writableLocked(ctx, affected...); err != nil {
		return err
	}
	// Preflight only, as above: the transition has to be projectable before it
	// is committed, but the Fulltext index picks it up from the change log.
	if _, err := catalogTransitionDocuments(current, databases); err != nil {
		return err
	}
	if err := commit(); err != nil {
		return err
	}
	if err := authority.checkpointPhase(phaseCatalogBodyCommitted); err != nil {
		return authority.poisonPublication("Catalog body", affected, err)
	}
	// One WAL transaction for the Catalog Tree and the Fulltext Tree, for the
	// same reason as a Row publication: two commits could tear between them.
	transactionID, err := authority.nextGroupTransactionID()
	if err == nil {
		err = treecommit.CommitGroupFunc(transactionID, func(group *treecommit.Group) error {
			return authority.generation.catalog.StageReplace(group, databases)
		})
	}
	if err == nil {
		err = authority.checkpointPhase(phaseCatalogPublished)
	}
	if err == nil {
		err = authority.checkpointPhase(phaseCatalogFulltextPublished)
	}
	if err != nil {
		return authority.poisonPublication("Catalog body", affected, err)
	}
	authority.catchUpFulltextAfterWrite(ctx)
	authority.maintainRedoLog()
	return nil
}

func (authority *Authority) checkpointPhase(phase authorityPhase) error {
	if authority.checkpoint == nil {
		return nil
	}
	return authority.checkpoint(phase)
}

// poisonPublication records an uncertain publication. databaseIDs narrows the
// blast radius; an empty list means the outcome is undeterminable and the whole
// Instance must fail closed for writes.
func (authority *Authority) poisonPublication(
	object string, databaseIDs []string, err error,
) error {
	if len(databaseIDs) == 0 {
		authority.poisonedAll = true
		return fmt.Errorf(
			"%w: %s committed before Page publication completed: %v",
			ErrOutcomeUnknown, object, err,
		)
	}
	if authority.poisonedDatabases == nil {
		authority.poisonedDatabases = make(map[string]struct{}, len(databaseIDs))
	}
	for _, databaseID := range databaseIDs {
		authority.poisonedDatabases[databaseID] = struct{}{}
	}
	return fmt.Errorf(
		"%w: %s committed before Page publication completed for database %s; "+
			"other databases stay writable: %v",
		ErrOutcomeUnknown, object, strings.Join(databaseIDs, ", "), err,
	)
}

// mutationDatabaseIDs returns the sorted, deduplicated Databases a Row/Route
// publication touches.
func mutationDatabaseIDs(rows []row.Row, routes []router.Node) []string {
	seen := make(map[string]struct{}, len(rows)+len(routes))
	for _, value := range rows {
		seen[value.DatabaseID] = struct{}{}
	}
	for _, value := range routes {
		seen[value.DatabaseID] = struct{}{}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		if id != "" {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// changedDatabaseIDs reports which Databases differ between two Catalog
// snapshots. Only those are left uncertain by a failed Catalog publication; the
// rest are byte-identical in both generations. The second result is false when
// the difference cannot be determined, which must fail closed Instance-wide.
func changedDatabaseIDs(current, next []catalog.Database) ([]string, bool) {
	encode := func(values []catalog.Database) (map[string]string, bool) {
		result := make(map[string]string, len(values))
		for _, value := range values {
			encoded, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			result[value.ID] = string(encoded)
		}
		return result, true
	}
	before, ok := encode(current)
	if !ok {
		return nil, false
	}
	after, ok := encode(next)
	if !ok {
		return nil, false
	}
	changed := make([]string, 0)
	for id, encoded := range after {
		if before[id] != encoded {
			changed = append(changed, id)
		}
	}
	for id := range before {
		if _, exists := after[id]; !exists {
			changed = append(changed, id)
		}
	}
	sort.Strings(changed)
	return changed, true
}

func (authority *Authority) lockRead(ctx context.Context) error {
	if authority == nil || ctx == nil {
		return fmt.Errorf("%w: authority read", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	authority.mu.RLock()
	if err := authority.openLocked(ctx); err != nil {
		authority.mu.RUnlock()
		return err
	}
	return nil
}

// openLocked reports whether the Authority can serve reads. Only a closed
// Authority refuses; a poisoned publication does not, because the committed
// generation stays internally consistent and readable.
func (authority *Authority) openLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if authority.closed {
		return fmt.Errorf("%w: authority is closed", ErrAuthorityPoisoned)
	}
	return nil
}

// writableLocked reports whether the named Databases may be written. Passing no
// Database asks only about Instance-wide health, which is what Catalog and
// replacement gates need before they know which Databases they will touch.
func (authority *Authority) writableLocked(ctx context.Context, databaseIDs ...string) error {
	if err := authority.openLocked(ctx); err != nil {
		return err
	}
	if authority.poisonedAll {
		return ErrAuthorityPoisoned
	}
	for _, databaseID := range databaseIDs {
		if _, poisoned := authority.poisonedDatabases[databaseID]; poisoned {
			return fmt.Errorf(
				"%w: database %s awaits reopen recovery; other databases are unaffected",
				ErrAuthorityPoisoned, databaseID,
			)
		}
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
	if err := authority.reconcileFulltext(plan); err != nil {
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

func (authority *Authority) reconcileFulltext(plan Plan) error {
	if authority.generation.fulltext == nil {
		return nil
	}
	documents, err := generationDocuments(plan)
	if err != nil {
		return err
	}
	objects, err := authority.generation.fulltext.Objects()
	if err != nil {
		return err
	}
	current := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		if isReconciledFulltextKind(document.Kind) {
			current[catalogObjectKey(document.Kind, document.ObjectID)] = struct{}{}
		}
	}
	for _, object := range objects {
		if !isReconciledFulltextKind(object.Kind) || object.State != fulltext.StateLive {
			continue
		}
		if _, exists := current[catalogObjectKey(object.Kind, object.ObjectID)]; exists {
			continue
		}
		var tombstone fulltext.Document
		if isCatalogKind(object.Kind) {
			tombstone, err = catalogfulltext.Tombstone(object)
		} else {
			tombstone, err = routefulltext.Tombstone(object)
		}
		if err != nil {
			return err
		}
		documents = append(documents, tombstone)
	}
	// The open-time pass is a full reconcile against the authoritative source,
	// which is what catches drift a change-log replay cannot see — an object
	// live in the index but gone from the source leaves no change entry behind
	// once its log has been reclaimed. Recording the cursor at the same time is
	// what makes every later catch-up incremental instead of another full pass.
	next, err := authority.changes.source.NextSequence(0)
	if err != nil {
		return fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
	}
	cursor := next - 1
	if len(documents) == 0 && cursor == 0 {
		return nil
	}
	id, err := authority.nextGroupTransactionID()
	if err != nil {
		return err
	}
	if cursor == 0 {
		_, err = authority.generation.fulltext.ReplaceBatch(id, documents)
	} else {
		_, err = authority.generation.fulltext.AdvanceThrough(id, documents, cursor)
	}
	if errors.Is(err, fulltextindex.ErrConflict) {
		return fmt.Errorf("%w: %v", errFulltextRebuildRequired, err)
	}
	return err
}

func catalogTransitionDocuments(
	current, next []catalog.Database,
) ([]fulltext.Document, error) {
	currentDocuments, err := catalogfulltext.Project(current)
	if err != nil {
		return nil, fmt.Errorf("%w: current Catalog fulltext projection: %v", ErrTargetCorrupt, err)
	}
	nextDocuments, err := catalogfulltext.Project(next)
	if err != nil {
		return nil, fmt.Errorf("%w: next Catalog fulltext projection: %v", ErrInvalid, err)
	}
	nextObjects := make(map[string]struct{}, len(nextDocuments))
	for _, document := range nextDocuments {
		nextObjects[catalogObjectKey(document.Kind, document.ObjectID)] = struct{}{}
	}
	result := append([]fulltext.Document(nil), nextDocuments...)
	for _, document := range currentDocuments {
		if _, exists := nextObjects[catalogObjectKey(document.Kind, document.ObjectID)]; exists {
			continue
		}
		compiled, err := fulltext.Compile(document)
		if err != nil {
			return nil, fmt.Errorf("%w: current Catalog document: %v", ErrTargetCorrupt, err)
		}
		tombstone, err := catalogfulltext.Tombstone(fulltext.Object{
			Kind: compiled.Kind, DatabaseID: compiled.DatabaseID, TableID: compiled.TableID,
			ObjectID: compiled.ObjectID, Revision: compiled.Revision, State: compiled.State,
			Digest: compiled.Digest,
		})
		if err != nil {
			return nil, fmt.Errorf("%w: Catalog tombstone: %v", ErrTargetCorrupt, err)
		}
		result = append(result, tombstone)
	}
	return result, nil
}

func isCatalogKind(kind fulltext.ObjectKind) bool {
	return kind == fulltext.KindDatabase || kind == fulltext.KindTable || kind == fulltext.KindColumn
}

func isReconciledFulltextKind(kind fulltext.ObjectKind) bool {
	return isCatalogKind(kind) || kind == fulltext.KindRoute
}

func catalogObjectKey(kind fulltext.ObjectKind, objectID string) string {
	return string(kind) + "\x00" + objectID
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

// nextGroupTransactionID allocates one transaction ID for a publication that
// spans several Trees. Every Tree of a generation shares one log, so any Tree
// answers with the same next ID — the Catalog Tree is simply the one that is
// always present.
func (authority *Authority) nextGroupTransactionID() (uint64, error) {
	return authority.nextTransactionID("catalog")
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
