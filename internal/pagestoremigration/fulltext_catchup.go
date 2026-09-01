package pagestoremigration

import (
	"context"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/catalogfulltext"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/routefulltext"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// fulltextCatchUpBatch is how many committed changes one catch-up round takes.
// Bounded so a long-idle index catches up in steps that each fit one WAL
// transaction rather than one enormous one.
const fulltextCatchUpBatch = uint64(128)

// FulltextBehind reports whether the derived Fulltext index has fallen behind
// the committed change log.
//
// It is deliberately cheap — a cursor read and a high-water read — because it
// runs before reads that may well have nothing to do.
func (authority *Authority) FulltextBehind(ctx context.Context) (bool, error) {
	if err := authority.lockRead(ctx); err != nil {
		return false, err
	}
	defer authority.mu.RUnlock()
	_, behind, err := authority.fulltextRangeLocked()
	return behind, err
}

// fulltextRangeLocked returns the range of committed change sequences the
// Fulltext index has not applied yet.
func (authority *Authority) fulltextRangeLocked() (uint64, bool, error) {
	if authority.generation == nil || authority.generation.fulltext == nil ||
		authority.changes == nil || authority.changes.source == nil {
		return 0, false, nil
	}
	cursor, err := authority.generation.fulltext.Cursor()
	if err != nil {
		return 0, false, fmt.Errorf("%w: Fulltext cursor: %v", ErrTargetCorrupt, err)
	}
	_, high, err := authority.changes.sourceHighWater()
	if err != nil {
		return 0, false, err
	}
	if cursor > high {
		return 0, false, fmt.Errorf("%w: Fulltext cursor leads the change log", ErrTargetCorrupt)
	}
	return cursor, cursor < high, nil
}

// catchUpFulltextLocked brings the Fulltext index level with the change log.
//
// This is the whole of what makes the index derived rather than co-written: the
// change log says what happened, the index reads it and re-projects whatever
// was touched. The write that made the change is long since committed and is
// not waiting for any of this.
//
// The caller must hold the Authority write lock.
func (authority *Authority) catchUpFulltextLocked(ctx context.Context) error {
	err := authority.runCatchUpLocked(ctx)
	authority.fulltextCatchUpErr = err
	return err
}

func (authority *Authority) runCatchUpLocked(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		cursor, behind, err := authority.fulltextRangeLocked()
		if err != nil || !behind {
			return err
		}
		next, err := authority.changes.source.NextSequence(0)
		if err != nil {
			return fmt.Errorf("%w: committed change high-water: %v", ErrTargetCorrupt, err)
		}
		high := next - 1
		last := min(high, cursor+fulltextCatchUpBatch)
		documents, err := authority.projectChangeRange(ctx, cursor+1, last)
		if err != nil {
			return err
		}
		transactionID, err := authority.nextGroupTransactionID()
		if err != nil {
			return err
		}
		if _, err := authority.generation.fulltext.AdvanceThrough(
			transactionID, documents, last,
		); err != nil {
			return fmt.Errorf("%w: Fulltext catch-up: %v", ErrTargetCorrupt, err)
		}
	}
}

// touchedObjects is what one range of committed changes actually disturbed.
//
// Sets rather than lists: a row updated five times in the range still needs one
// document, projected from its current state. Re-projecting the end state is
// both correct and what makes catching up cheaper than replaying.
type touchedObjects struct {
	rows       map[string]change.Entry
	routes     map[string]change.Entry
	catalogAny bool
}

// projectChangeRange reads the committed changes in [first, last] and projects
// a document for everything they touched.
func (authority *Authority) projectChangeRange(
	ctx context.Context, first, last uint64,
) ([]fulltext.Document, error) {
	touched := touchedObjects{
		rows: map[string]change.Entry{}, routes: map[string]change.Entry{},
	}
	for sequence := first; sequence <= last; sequence++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		envelope, err := authority.changes.source.Get(sequence)
		if err != nil {
			return nil, fmt.Errorf(
				"%w: committed change sequence %d: %v", ErrTargetCorrupt, sequence, err,
			)
		}
		for _, entry := range envelope.Entries {
			switch entry.ObjectKind {
			case change.ObjectRow:
				touched.rows[entry.ObjectID] = entry
			case change.ObjectRouteNode:
				touched.routes[entry.ObjectID] = entry
			case change.ObjectDatabase, change.ObjectTable, change.ObjectColumn:
				// Catalog documents are projected per Database, so the identity
				// of the individual Table or Column adds nothing here.
				touched.catalogAny = true
			}
		}
	}
	return authority.projectTouched(ctx, touched)
}

func (authority *Authority) projectTouched(
	ctx context.Context, touched touchedObjects,
) ([]fulltext.Document, error) {
	// Through the Trees, not the record log. Catch-up follows every write, and
	// reading the Catalog out of the record log made each one cost a rebuild of
	// the whole Catalog from a full sweep of the file.
	databases, err := authority.catalog.Snapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: Catalog for Fulltext catch-up: %v", ErrTargetCorrupt, err)
	}
	var documents []fulltext.Document

	if touched.catalogAny {
		// Catalog projection is bounded by the schema, not by the data, so a
		// touched Column is re-projected along with its Database rather than
		// singled out.
		projected, err := catalogfulltext.Project(databases)
		if err != nil {
			return nil, fmt.Errorf("%w: Catalog fulltext documents: %v", ErrInvalid, err)
		}
		documents = append(documents, projected...)
	}

	if len(touched.routes) != 0 {
		// The objects Tree, for the same reason — and through the generation
		// rather than the Authority, because the Authority lock is already held.
		nodes, err := nativerouter.NewWithObjects(authority.file, authority.generation).Nodes()
		if err != nil {
			return nil, fmt.Errorf("%w: Routes for Fulltext catch-up: %v", ErrTargetCorrupt, err)
		}
		live := make(map[string]router.Node, len(nodes))
		for _, node := range nodes {
			live[node.ID] = node
		}
		changed := make([]router.Node, 0, len(touched.routes))
		for objectID, entry := range touched.routes {
			node, exists := live[objectID]
			if exists {
				changed = append(changed, node)
				continue
			}
			// The Router hands out live nodes only, so a Route the change log
			// named but the Router no longer has is a deletion. Skipping it
			// here is what would leave stale postings behind — the exact
			// failure a derived index is supposed to make impossible.
			tombstone, err := routefulltext.TombstoneFor(
				entry.DatabaseID, entry.TableID, objectID, entry.AfterRevision,
			)
			if err != nil {
				return nil, fmt.Errorf("%w: Route tombstone: %v", ErrInvalid, err)
			}
			documents = append(documents, tombstone)
		}
		projected, err := projectRouteChangeDocuments(databases, changed)
		if err != nil {
			return nil, err
		}
		documents = append(documents, projected...)
	}

	if len(touched.rows) != 0 {
		bodies, err := authority.currentRowBodies(ctx, databases, touched.rows)
		if err != nil {
			return nil, err
		}
		projected, err := projectRowDocuments(databases, bodies)
		if err != nil {
			return nil, err
		}
		documents = append(documents, projected...)
	}
	return documents, nil
}

// currentRowBodies reads the present state of every Row the range touched.
//
// Rows are the one unbounded family here, so they are read one by one rather
// than through a whole-instance scan. A Row whose Table has since disappeared
// is skipped: there is nothing left to index, and its postings go away with the
// Table's own tombstone.
func (authority *Authority) currentRowBodies(
	ctx context.Context, databases []catalog.Database, touched map[string]change.Entry,
) ([]row.Row, error) {
	tables := make(map[string]catalog.Table)
	for _, database := range databases {
		for _, table := range database.Tables {
			tables[table.ID] = table
		}
	}
	bodies := make([]row.Row, 0, len(touched))
	for rowID, entry := range touched {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		table, exists := tables[entry.TableID]
		if !exists {
			continue
		}
		body, err := authority.rows.CurrentIncludingDeleted(ctx, table, rowID)
		if err != nil {
			if errors.Is(err, ErrTargetCorrupt) {
				return nil, err
			}
			// The Row is gone from the authoritative store. Nothing to index.
			continue
		}
		bodies = append(bodies, body)
	}
	return bodies, nil
}

// catchUpFulltextAfterWrite brings the Fulltext index level immediately after a
// successful publication, while the write lock is still held.
//
// Decoupling is about the transaction, not the timing: the point is that a
// derived index can no longer fail or poison a user's write, not that it should
// lag behind one. Running catch-up here, outside the committed transaction,
// gets both — the write is already durable and cannot be undone by anything
// that happens next.
//
// A failure is therefore never returned. It is recorded, and the read-time
// trigger retries; the worst case is one read paying for the catch-up this
// round did not manage.
func (authority *Authority) catchUpFulltextAfterWrite(ctx context.Context) {
	if err := authority.checkpointPhase(phaseFulltextCatchUp); err != nil {
		authority.fulltextCatchUpErr = err
		return
	}
	_ = authority.catchUpFulltextLocked(ctx)
}

// FulltextCatchUpError reports the last failed Fulltext catch-up, or nil. A
// failure here never failed a write.
func (authority *Authority) FulltextCatchUpError() error {
	if authority == nil {
		return nil
	}
	authority.mu.RLock()
	defer authority.mu.RUnlock()
	return authority.fulltextCatchUpErr
}
