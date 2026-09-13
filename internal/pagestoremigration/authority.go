package pagestoremigration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
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
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/rowid"
	"github.com/HW-Yue/Memora/internal/store/changeindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/objectindex"
	"github.com/HW-Yue/Memora/internal/store/objectlock"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
)

var ErrAuthorityPoisoned = errors.New("Page Store authority requires reopen recovery")

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
	// phaseFulltextCatchUp is a seam for failing a catch-up round. A failed
	// round followed by more writes is the case that used to wedge the index
	// permanently, so it needs to be reachable from a test.
	phaseFulltextCatchUp authorityPhase = "fulltext-catch-up"
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
	// catalog is swapped, not mutated, when a generation is replaced: readers
	// that run inside a publication need it without taking the Authority lock,
	// and taking that lock while the publication holds it is a self-deadlock.
	catalog atomic.Pointer[nativecatalog.IndexedReader]
	rows    *nativerow.IndexedReader
	changes *authorityChangeTree
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
	// archiveErr holds the last record log commit mark that could not be
	// written after the Trees had already committed. The write happened — the
	// Trees are the Database — so this cannot fail it, and it cannot fence the
	// Database either: what is behind is the archive, and the binlog already
	// holds the frame. It must not vanish, so ArchiveError exposes it.
	archiveErr       error
	changeCatchUpErr error
	closed           bool
	writeGate        chan struct{}
	locks            *objectlock.Manager
	nextOwner        atomic.Uint64
	checkpoint       func(authorityPhase) error
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
	catalogReader, err := nativecatalog.NewIndexedReader(generation.catalog, generation)
	if err != nil {
		_ = generation.Close()
		return nil, err
	}
	rowReader, err := nativerow.NewIndexedReader(
		nativerow.New(file), catalogReader, tableCurrentRows{generation: generation}, generation.versions,
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
		reader: reader, rows: rowReader, changes: changeTree,
		writeGate: make(chan struct{}, 1),
		locks:     objectlock.New(),
	}
	authority.catalog.Store(catalogReader)
	authority.writeGate <- struct{}{}
	// E8 stage 1 removed the reconcile pass that used to run here.
	//
	// It re-read the whole record log on every open and pushed the Trees back
	// to whatever it said. That was right while the record log was the
	// authority: publications committed it first, so a fault could leave the
	// Trees a revision behind, and this is where they caught up. It is wrong
	// now, and actively harmful — the Trees commit first, so a Tree ahead of
	// the record log is the normal outcome of a fault, and pushing them back to
	// the archive would discard a committed write. See
	// docs/storage/record-index-and-authority-v1.md §5.
	//
	// A generation missing a Tree, holding one redo log per Tree, or keeping
	// every Table's current Rows in one shared Tree, or no objects Tree, still
	// has to be replaced whatever its contents say — the second cannot publish
	// atomically across Trees, the third is the layout v5 replaced, and the
	// fourth leaves every Route and Relation with no index but the record
	// file's resident map.
	if authority.generation.fulltext == nil || authority.generation.log == nil ||
		!authority.generation.manifest.perTableRows() ||
		!authority.generation.manifest.physicalObjectIndex() {
		if _, err := authority.ReplaceGeneration(ctx); err != nil {
			return nil, errors.Join(err, authority.Close())
		}
	}
	return authority, nil
}

// NextRowID allocates the next Row ID number for a Table.
//
// It reads the Table's durable counter and returns the number after it. The
// advance is not written here: it lands with the Row that took the number, in
// PublishMutation's single commit. A write that then fails burns the number —
// a gap in the sequence, never a repeat, which is the property that matters.
//
// Callers hold the write lock, so no two allocations can see the same counter.
func (authority *Authority) NextRowID(ctx context.Context, databaseID, tableID string) (string, error) {
	if authority == nil || ctx == nil || tableID == "" {
		return "", fmt.Errorf("%w: Row ID allocation", ErrInvalid)
	}
	if err := authority.lockRead(ctx); err != nil {
		return "", err
	}
	defer authority.mu.RUnlock()
	if err := authority.writableLocked(ctx, databaseID); err != nil {
		return "", err
	}
	index := authority.generation.CurrentRowsFor(tableID)
	if index == nil {
		return "", fmt.Errorf("%w: Table %q has no Tree", ErrInvalid, tableID)
	}
	counter, err := index.RowIDCounter()
	if err != nil {
		return "", err
	}
	if counter == math.MaxUint64 {
		return "", fmt.Errorf("%w: Table %q Row IDs are exhausted", ErrTargetCorrupt, tableID)
	}
	return rowid.Format(tableSpaceID(tableID), counter+1)
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

// NextCommitSequence reserves the next point on the snapshot timeline.
//
// It comes from the versions Tree's high-water marker, which every publication
// advances past everything it wrote — Relations included, through the floor.
// It used to be computed by sweeping every Row and every Relation record in the
// Database and taking the maximum, on every write.
//
// A write that then fails burns the number: the marker only moves forward, and
// the caller retries with a later one. Nothing requires commit sequences to be
// dense — they order and locate AS OF reads, and a gap orders exactly as well.
func (authority *Authority) NextCommitSequence(ctx context.Context) (uint64, error) {
	if err := authority.lockRead(ctx); err != nil {
		return 0, err
	}
	defer authority.mu.RUnlock()
	if authority.generation == nil || authority.generation.versions == nil {
		return 0, fmt.Errorf("%w: commit sequence allocator", ErrInvalid)
	}
	high, err := authority.generation.versions.HighWater()
	if err != nil {
		return 0, err
	}
	return high + 1, nil
}

// mutationCommitHighWater is the highest commit sequence a publication writes.
func mutationCommitHighWater(rows []row.Row, relations []relation.Relation) uint64 {
	var highest uint64
	for _, value := range rows {
		if value.CommitSequence > highest {
			highest = value.CommitSequence
		}
	}
	for _, value := range relations {
		if value.CommitSequence > highest {
			highest = value.CommitSequence
		}
	}
	return highest
}

// TableByIdentity resolves a Table without taking the Authority lock.
//
// A Row cannot be encoded or decoded without its schema, so this is reached from
// inside publications — the Fulltext catch-up runs while the write lock is still
// held, and asking for the lock again there is a self-deadlock. It is also not
// needed: the Catalog reader is swapped atomically when a generation is
// replaced, and what it reads are Trees that carry their own locks.
func (authority *Authority) TableByIdentity(
	ctx context.Context, databaseID, tableID string,
) (catalog.Table, error) {
	if authority == nil {
		return catalog.Table{}, fmt.Errorf("%w: Table lookup", ErrInvalid)
	}
	reader := authority.catalog.Load()
	if reader == nil {
		return catalog.Table{}, fmt.Errorf("%w: Catalog reader", ErrInvalid)
	}
	return reader.DescribeTable(ctx, databaseID, tableID)
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
	// From the change index Tree's high-water, which is a durable floor: the
	// probe forward from it is one point read, where sweeping the whole change
	// log used to make every write cost a pass over every change ever committed.
	if authority.changes != nil {
		// The index's high-water is the log's whenever the index is level with
		// it, which every successful publication leaves it. Level is two numbers
		// — how far into the log the index has read, and how long the log is —
		// so establishing it reads no records at all.
		//
		// It is not blind trust: a publication that failed after committing to
		// the log leaves the index behind, and handing out its high-water would
		// reissue a sequence the log already holds. That case falls back to
		// probing the log, which is what this used to do every time.
		behind, err := authority.changes.behind()
		if err != nil {
			return 0, err
		}
		if !behind {
			high, _, err := authority.changes.currentHighWater()
			if err != nil {
				return 0, err
			}
			return high + 1, nil
		}
		_, high, err := authority.changes.sourceHighWater()
		if err != nil {
			return 0, err
		}
		return high + 1, nil
	}
	return nativechange.New(authority.file).NextSequence(0)
}

// ListCommittedChanges reads the committed change log.
//
// Reading it can require a write: the change index catches up lazily, and
// catching up advances a cursor in the index Tree. So the write lock is taken
// only when there is catch-up to do — checked first under the read lock, which
// is two high-water reads. A reader that finds the index current serves the
// whole page without ever serialising against writers.
//
// The check is racy by design: another writer may append between the check and
// the lock. Catch-up is idempotent and the next reader picks it up, so the cost
// of losing that race is one stale page, not a wrong answer.
func (authority *Authority) ListCommittedChanges(
	ctx context.Context,
	databaseID string,
	after, snapshot uint64,
	limit int,
) ([]change.Envelope, uint64, bool, error) {
	if authority == nil || ctx == nil || limit < 1 || limit > 256 {
		return nil, 0, false, fmt.Errorf("%w: committed change list", ErrInvalid)
	}
	current, err := authority.changeIndexIsCurrent(ctx)
	if err != nil {
		return nil, 0, false, err
	}
	if current {
		defer authority.mu.RUnlock()
	} else {
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
	// Same reasoning as ListCommittedChanges: the write lock only when the
	// index actually has catching up to do.
	current, err := authority.changeIndexIsCurrent(ctx)
	if err != nil {
		return change.Envelope{}, err
	}
	if current {
		defer authority.mu.RUnlock()
	} else {
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

// PublishMutation writes one mutation to the Trees and the record log.
//
// The Trees decide. The record log is prepared first — its records and the
// binlog frame go down, and nothing yet claims they happened — then the Trees
// commit as one WAL transaction, and only then does the log write its commit
// mark. That order is what E8 means by clustered promotion: the Trees plus the
// redo log are the Database, and the record log is the archive beside it
// (docs/storage/record-index-and-authority-v1.md §5).
//
// It buys an outcome where there used to be a question. Committing the record
// log first made it the authority, so a Tree failure after it left a write that
// had happened in one store and not the other — reported as ErrOutcomeUnknown,
// the Database fenced, and repaired on the next open by re-reading the record
// log. Now a Tree failure is a failed write: the Trees commit atomically or not
// at all, the prepared records are discarded by the next open, and the caller
// gets an error rather than a Database it has to ask about.
func (authority *Authority) PublishMutation(
	ctx context.Context, mutation nativerow.Mutation, commit nativerow.Publication,
) error {
	if authority == nil || ctx == nil || mutation.Empty() || commit == nil {
		return fmt.Errorf("%w: Row/Route/Relation publication", ErrInvalid)
	}
	rows, routes, relations := mutation.Rows, mutation.Routes, mutation.Relations
	authority.mu.Lock()
	defer authority.mu.Unlock()
	affected := mutationDatabaseIDs(rows, routes)
	if err := authority.writableLocked(ctx, affected...); err != nil {
		return err
	}
	databases, err := authority.catalog.Load().Snapshot(ctx)
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
	if err := commit.Prepare(); err != nil {
		return err
	}
	// From here to the Tree commit, every way out is a failed write: the
	// prepared records carry no commit mark, so the next open discards them and
	// the Database is exactly what it was. Nothing is fenced and nothing is
	// left to reconcile.
	if len(rows) != 0 {
		if err := authority.checkpointPhase(phaseRowBodyCommitted); err != nil {
			return err
		}
	}
	if len(routes) != 0 {
		if err := authority.checkpointPhase(phaseRouteBodyCommitted); err != nil {
			return err
		}
	}
	versions, err := clusteredVersions(databases, rows)
	if err != nil {
		return err
	}
	// Grouped by Table, because a Table's current Rows and its history both live
	// in Trees of that Table's own. A publication touching several Tables
	// therefore stages into several Trees — still one WAL transaction, which is
	// what makes it atomic.
	current := make(map[string][]currentrowindex.Update, 1)
	tableVersions := make(map[string][]rowversionindex.Locator, 1)
	for _, locator := range versions {
		tableVersions[locator.TableID] = append(tableVersions[locator.TableID], locator)
	}
	for _, value := range rows {
		current[value.TableID] = append(current[value.TableID], currentrowindex.Update{
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
	// Last chance to free ring space before the commit. A full ring refuses the
	// write (wal.ErrRingFull) rather than overwriting changes no Page file
	// holds yet, and only a checkpoint can move the tail. A failure here is not
	// reported on its own: the commit below is the authoritative outcome, and
	// it either succeeds or reports the back-pressure itself.
	if err := authority.generation.relieveRedoRing(); err != nil {
		authority.redoMaintenanceErr = err
	}
	// Routes and Relations share the objects Tree, so they are staged as one
	// batch. Two StageApply calls on one Index in one group would deadlock: the
	// first hands its write lock to the group, and the second waits for a lock
	// the group cannot release until the commit it is still being built for.
	objectRecords, err := routeObjectUpdates(routes)
	if err != nil {
		return err
	}
	relationRecords, err := nativerow.RelationObjectUpdates(relations)
	if err != nil {
		return err
	}
	objectRecords = append(objectRecords, relationRecords...)
	transactionID, err := authority.nextGroupTransactionID()
	if err == nil {
		err = treecommit.CommitGroupFunc(transactionID, func(group *treecommit.Group) error {
			// The Routes go into the same group as the Rows, so a Route landing
			// in the objects Tree and a Row landing in its Table's Tree are one
			// durable fact rather than two a fault can separate.
			if err := authority.generation.objects.StageApply(group, objectRecords); err != nil {
				return err
			}
			// The shared versions Tree carries the commit sequence allocator's
			// high-water. Relations take their numbers from the same allocator
			// but are not Row versions, so the floor is what stops a number a
			// Relation holds from being handed out again.
			if floor := mutationCommitHighWater(rows, relations); len(rows) != 0 || floor != 0 {
				if err := authority.generation.versions.StageAppendWithCommitFloor(
					group, versions, floor,
				); err != nil {
					return err
				}
			}
			// The same revisions again, into the Table's own history Tree.
			// Both are written until the read path moves over (stage 5); the
			// shared Tree is what still answers AS OF today, and writing only
			// one of them would make the switch a change of answer rather than
			// an equivalent replacement.
			for _, tableID := range sortedKeys(tableVersions) {
				index := authority.generation.HistoryFor(tableID)
				if index == nil {
					return fmt.Errorf("%w: Table %q has no history Tree", ErrInvalid, tableID)
				}
				if err := index.StageAppend(group, tableVersions[tableID]); err != nil {
					return err
				}
			}
			// Stage in Table order so a group's member Trees are always
			// enrolled in the same order, whatever order the Rows arrived in.
			for _, tableID := range sortedKeys(current) {
				index := authority.generation.CurrentRowsFor(tableID)
				if index == nil {
					return fmt.Errorf("%w: Table %q has no Tree", ErrInvalid, tableID)
				}
				// The counter moves with the Rows that took the numbers, so an
				// allocated ID becomes durable exactly when its Row does.
				rowIDs := make([]string, 0, len(current[tableID]))
				for _, update := range current[tableID] {
					rowIDs = append(rowIDs, update.Locator.RowID)
				}
				if err := index.StageApplyWithRowIDCounter(
					group, current[tableID], rowid.HighWater(rowIDs),
				); err != nil {
					return err
				}
			}
			return nil
		})
	}
	if err != nil {
		return err
	}
	// Past this line the write has happened: the group commit is the point, and
	// it returned. Everything below is the archive and the derived indexes
	// catching up to a fact that is already durable, so nothing below may turn
	// a committed write into a reported failure.
	//
	// The phases stay as fault-injection seams, and they now model exactly
	// that: a process that stops here has committed the Row and left the
	// archive a transaction behind. The tests assert the Database reads the new
	// revision anyway, because the Trees are the Database.
	var stopped error
	for _, phase := range []struct {
		name  authorityPhase
		fires bool
	}{
		{phaseRowVersionPublished, len(rows) != 0},
		{phaseRowFulltextPublished, len(rows) != 0},
		{phaseRouteFulltextPublished, len(routes) != 0},
		{phaseRowCurrentPublished, len(rows) != 0},
	} {
		if stopped != nil || !phase.fires {
			continue
		}
		stopped = authority.checkpointPhase(phase.name)
	}
	if stopped != nil {
		authority.archiveErr = stopped
		return nil
	}
	// The archive's mark. A failure here leaves the record log a transaction
	// behind the Database — an archive gap, not a lost write — and is reported
	// without taking the Database's writability with it.
	if err := commit.Complete(); err != nil {
		authority.archiveErr = err
		return nil
	}
	// The change index first: the Fulltext catch-up reads how far the change log
	// goes from that index, so catching Fulltext up before it would have it work
	// from a high-water one publication behind.
	authority.catchUpChangesAfterWrite(ctx)
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

// ArchiveError reports the last record log commit mark that could not follow a
// Tree commit.
//
// It is not a failed write. The Trees committed, so the Database holds the
// change; what is a transaction short is the archive beside it. Reported rather
// than swallowed because an archive that silently stops tracking the Database
// is the kind of thing only noticed when it is needed.
func (authority *Authority) ArchiveError() error {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.archiveErr
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
	return authority.catalog.Load().Snapshot(ctx)
}

func (authority *Authority) ShowDatabases(ctx context.Context) ([]catalog.Database, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.Load().ShowDatabases(ctx)
}

func (authority *Authority) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	if err := authority.lockRead(ctx); err != nil {
		return catalog.Database{}, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.Load().DescribeDatabase(ctx, name)
}

func (authority *Authority) ShowTables(ctx context.Context, databaseName string) ([]catalog.Table, error) {
	if err := authority.lockRead(ctx); err != nil {
		return nil, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.Load().ShowTables(ctx, databaseName)
}

func (authority *Authority) DescribeTable(
	ctx context.Context, databaseName, tableName string,
) (catalog.Table, error) {
	if err := authority.lockRead(ctx); err != nil {
		return catalog.Table{}, err
	}
	defer authority.mu.RUnlock()
	return authority.catalog.Load().DescribeTable(ctx, databaseName, tableName)
}

// PublishCatalog writes one Catalog transition to the Trees and the record log.
//
// Same order, same reason as PublishMutation: the record log is prepared, the
// Catalog and objects Trees commit as one WAL transaction, and the mark
// follows. The Trees decide.
func (authority *Authority) PublishCatalog(
	ctx context.Context, databases []catalog.Database, commit nativecatalog.Publication,
) error {
	if authority == nil || ctx == nil || commit == nil {
		return fmt.Errorf("%w: Catalog publication", ErrInvalid)
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if err := authority.openLocked(ctx); err != nil {
		return err
	}
	current, err := authority.catalog.Load().Snapshot(ctx)
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
	if err := commit.Prepare(); err != nil {
		return err
	}
	// Everything from here to the Tree commit is a failed write: the prepared
	// records carry no commit mark, so the next open discards them.
	if err := authority.checkpointPhase(phaseCatalogBodyCommitted); err != nil {
		return err
	}
	// One WAL transaction for the Catalog Tree and the Fulltext Tree, for the
	// same reason as a Row publication: two commits could tear between them.
	// Last chance to free ring space before the commit. A full ring refuses the
	// write (wal.ErrRingFull) rather than overwriting changes no Page file
	// holds yet, and only a checkpoint can move the tail. A failure here is not
	// reported on its own: the commit below is the authoritative outcome, and
	// it either succeeds or reports the back-pressure itself.
	if err := authority.generation.relieveRedoRing(); err != nil {
		authority.redoMaintenanceErr = err
	}
	catalogRecords, err := nativecatalog.ObjectRecords(databases)
	if err != nil {
		return err
	}
	transactionID, err := authority.nextGroupTransactionID()
	if err == nil {
		err = treecommit.CommitGroupFunc(transactionID, func(group *treecommit.Group) error {
			if err := authority.generation.catalog.StageReplace(group, databases); err != nil {
				return err
			}
			// The bodies go into the objects Tree in the same group as the
			// Locators that point at them. That is what the reader relies on:
			// a Locator naming an object the objects Tree does not hold is
			// corruption, not a race, so the two must never be committed apart.
			//
			// A replacement rather than an update, because a Catalog write
			// hands over the whole set — DROP COLUMN is a real statement, and
			// an object family that only ever grew would keep bodies nothing
			// points at any more.
			return authority.generation.objects.StageReplaceKinds(
				group, nativecatalog.ObjectKinds, catalogRecords,
			)
		})
	}
	if err != nil {
		return err
	}
	// Past this line the transition has happened. The phases below model a
	// process that stops after the Trees committed and before the archive
	// caught up, so they record the gap rather than failing a durable write.
	var stopped error
	for _, phase := range []authorityPhase{phaseCatalogPublished, phaseCatalogFulltextPublished} {
		if stopped == nil {
			stopped = authority.checkpointPhase(phase)
		}
	}
	if stopped == nil {
		if err := commit.Complete(); err != nil {
			stopped = err
		}
	}
	if stopped != nil {
		authority.archiveErr = stopped
		return nil
	}
	// A Table's Tree is created with the Table. Doing it after the Catalog is
	// published means the Tree set is derived from a Catalog that is already
	// durable, so a crash here leaves a Table whose Tree is merely missing —
	// and the next publication creates it — rather than a Tree belonging to a
	// Table that never existed.
	if err := authority.generation.EnsureTableTrees(catalogTableIDs(databases)); err != nil {
		return authority.poisonPublication("Catalog Table Trees", affected, err)
	}
	_ = affected
	// The change index first: the Fulltext catch-up reads how far the change log
	// goes from that index, so catching Fulltext up before it would have it work
	// from a high-water one publication behind.
	authority.catchUpChangesAfterWrite(ctx)
	authority.catchUpFulltextAfterWrite(ctx)
	authority.maintainRedoLog()
	return nil
}

// catalogTableIDs lists every Table in a Catalog snapshot, archived ones
// included: an archived Table is still readable, so its Tree still has to be
// there.
func catalogTableIDs(databases []catalog.Database) []string {
	tableIDs := make([]string, 0, len(databases))
	for _, database := range databases {
		for _, table := range database.Tables {
			tableIDs = append(tableIDs, table.ID)
		}
	}
	return tableIDs
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

// changeIndexIsCurrent reports whether the committed change index needs no
// catch-up. It returns holding the read lock when the answer is yes, so the
// caller can serve the read straight away; on no (or on error) no lock is held
// and the caller takes the write path.
func (authority *Authority) changeIndexIsCurrent(ctx context.Context) (bool, error) {
	if err := authority.lockRead(ctx); err != nil {
		return false, err
	}
	behind, err := authority.changes.behind()
	if err != nil || behind {
		authority.mu.RUnlock()
		return false, err
	}
	return true, nil
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

// RouteObjects hands out the objects Tree the current generation holds.
//
// It is resolved on every call rather than handed out once: a COW rebuild
// replaces the generation, and a caller holding the old Tree would answer from
// one nothing writes to any more.
func (authority *Authority) RouteObjects() *objectindex.Index {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	if authority.generation == nil {
		return nil
	}
	return authority.generation.objects
}

// RelationObjects hands out the objects Tree the current generation holds, for
// the same reason RouteObjects does: a COW rebuild replaces the generation.
// ChangeEnvelope resolves a committed change by the sequence a Row revision
// carries, for the history reader.
//
// It takes no Authority lock: the caller is already inside a read that holds
// one, and the change index has a lock of its own.
func (authority *Authority) ChangeEnvelope(sequence uint64) (change.Envelope, error) {
	if authority == nil || authority.changes == nil {
		return change.Envelope{}, changeindex.ErrNotFound
	}
	return authority.changes.getBySequence(sequence)
}

// CurrentRowRevision reports a Row's newest revision from the Table's current
// Row Tree, and whether the Tree holds it at all.
//
// It takes no Authority lock: this is reached from inside a publication, where
// the lock is already held — the same reason TableByIdentity is not
// DescribeTable.
//
// The write path used to establish this by probing the record log for
// revision 1, 2, 4, 8 … and bisecting, which is O(log revisions) point reads
// through that log's process-resident index. The Tree answers in one descent
// and is the structure that index was standing in for.
func (authority *Authority) CurrentRowRevision(
	databaseID, tableID, rowID string,
) (uint64, bool, error) {
	if authority == nil || authority.generation == nil || tableID == "" || rowID == "" {
		return 0, false, nil
	}
	index := authority.generation.CurrentRowsFor(tableID)
	if index == nil {
		return 0, false, nil
	}
	locator, err := index.Lookup(rowID)
	if errors.Is(err, currentrowindex.ErrNotFound) {
		return 0, true, nil
	}
	if err != nil {
		return 0, false, err
	}
	if locator.DatabaseID != databaseID || locator.TableID != tableID {
		return 0, false, fmt.Errorf("%w: current Row belongs to another Table", ErrTargetCorrupt)
	}
	return locator.Revision, true, nil
}

func (authority *Authority) RelationObjects() *objectindex.Index {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	if authority.generation == nil {
		return nil
	}
	return authority.generation.objects
}

func (authority *Authority) Generation() *Generation {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.generation
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

func catalogObjectKey(kind fulltext.ObjectKind, objectID string) string {
	return string(kind) + "\x00" + objectID
}

// sortedKeys returns a map's keys in a stable order, so a batch of Trees is
// always enrolled the same way regardless of map iteration order.
func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
