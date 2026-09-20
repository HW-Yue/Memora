package sqlstore

import (
	"context"

	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

// operations is the executor.Rows surface, implemented once over a tx. DB runs
// each call in its own transaction; Transaction runs them all in one.
type operations struct {
	run func(ctx context.Context, write bool, fn func(*tx) error) error
}

func (db *DB) ops() operations {
	return operations{run: func(ctx context.Context, write bool, fn func(*tx) error) error {
		if write {
			return db.update(ctx, fn)
		}
		return db.view(ctx, fn)
	}}
}

func (o operations) Get(ctx context.Context, databaseName, tableName, rowID string) (value row.Row, err error) {
	err = o.run(ctx, false, func(t *tx) error { value, err = t.get(ctx, databaseName, tableName, rowID); return err })
	return
}

func (o operations) ListPage(ctx context.Context, databaseName, tableName string, limit int) (values []row.Row, more bool, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		values, more, err = t.listPage(ctx, databaseName, tableName, limit)
		return err
	})
	return
}

func (o operations) Insert(ctx context.Context, databaseName, tableName string, values map[string]any, options row.WriteOptions) (value row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.insert(ctx, databaseName, tableName, values, options); return err })
	return
}

func (o operations) Update(ctx context.Context, databaseName, tableName, rowID string, changes map[string]any, options row.WriteOptions) (value row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error {
		value, err = t.updateRow(ctx, databaseName, tableName, rowID, changes, options)
		return err
	})
	return
}

func (o operations) Delete(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (value row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.deleteRow(ctx, databaseName, tableName, rowID, options); return err })
	return
}

func (o operations) AsOfRevision(ctx context.Context, databaseName, tableName, rowID string, revision uint64) (value row.Row, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		value, err = t.asOf(ctx, databaseName, tableName, rowID, func(record history.Record) bool { return record.Revision == revision })
		return err
	})
	return
}

func (o operations) AsOfCommit(ctx context.Context, databaseName, tableName, rowID string, sequence uint64) (value row.Row, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		value, err = t.asOf(ctx, databaseName, tableName, rowID, func(record history.Record) bool { return record.CommitSequence <= sequence })
		return err
	})
	return
}

func (o operations) HistoryPage(ctx context.Context, databaseName, tableName, rowID, cursor string, limit int) (records []history.Record, page history.ReadPage, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		records, page, err = t.historyPage(ctx, databaseName, tableName, rowID, cursor, limit)
		return err
	})
	return
}

func (o operations) Restore(ctx context.Context, databaseName, tableName, rowID string, revision uint64, options row.WriteOptions) (value row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error {
		value, err = t.restore(ctx, databaseName, tableName, rowID, revision, options)
		return err
	})
	return
}

func (o operations) CreateRouterNode(ctx context.Context, parentID string, definition router.NodeDefinition) (value router.Node, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.createNode(ctx, parentID, definition); return err })
	return
}

func (o operations) RenameRouterNode(ctx context.Context, routeID, name string, expected uint64) (value router.Node, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.renameNode(ctx, routeID, name, expected); return err })
	return
}

func (o operations) DeleteRouterNode(ctx context.Context, routeID string, expected uint64) (revision uint64, err error) {
	err = o.run(ctx, true, func(t *tx) error { revision, err = t.deprecateNode(ctx, routeID, expected); return err })
	return
}

func (o operations) GetRouterNode(ctx context.Context, routeID string) (value router.Node, err error) {
	err = o.run(ctx, false, func(t *tx) error { value, err = t.getNode(ctx, routeID); return err })
	return
}

func (o operations) ListRouterChildrenPage(ctx context.Context, parentID, cursor string, limit int) (values []router.Node, page router.ReadPage, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		values, page, err = t.childrenPage(ctx, parentID, cursor, limit)
		return err
	})
	return
}

func (o operations) ListRouterLeafPage(ctx context.Context, leafID, cursor string, limit int) (values []router.Locator, page router.ReadPage, err error) {
	err = o.run(ctx, false, func(t *tx) error { values, page, err = t.leafPage(ctx, leafID, cursor, limit); return err })
	return
}

func (o operations) CreateTableRouterRoot(ctx context.Context, databaseName, tableName, purpose, synopsis string) (value router.Node, err error) {
	err = o.run(ctx, true, func(t *tx) error {
		value, err = t.createTableRoot(ctx, databaseName, tableName, purpose, synopsis)
		return err
	})
	return
}

func (o operations) ListTableRouterRootsPage(ctx context.Context, databaseID, tableID, cursor string, limit int) (values []router.Node, page router.ReadPage, err error) {
	err = o.run(ctx, false, func(t *tx) error {
		values, page, err = t.tableRootChildren(ctx, databaseID, tableID, cursor, limit)
		return err
	})
	return
}

func (o operations) UpdateRouterSynopsis(ctx context.Context, routeID, synopsis string, expected uint64) (value router.Node, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.updateSynopsis(ctx, routeID, synopsis, expected); return err })
	return
}

func (o operations) UpdateRouterAliases(ctx context.Context, routeID string, aliases []string, expected uint64) (value router.Node, err error) {
	err = o.run(ctx, true, func(t *tx) error { value, err = t.updateAliases(ctx, routeID, aliases, expected); return err })
	return
}

func (o operations) ListRouterNodes(ctx context.Context) (values []router.Node, err error) {
	err = o.run(ctx, false, func(t *tx) error { values, err = t.allNodes(ctx); return err })
	return
}

func (o operations) Split(ctx context.Context, databaseName, tableName string, sources []string, targets []map[string]any, options row.ReshapeOptions) (values []row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error {
		values, err = t.reshape(ctx, databaseName, tableName, sources, targets, options, history.OperationSplit)
		return err
	})
	return
}

func (o operations) Merge(ctx context.Context, databaseName, tableName string, sources []string, targets []map[string]any, options row.ReshapeOptions) (values []row.Row, err error) {
	err = o.run(ctx, true, func(t *tx) error {
		values, err = t.reshape(ctx, databaseName, tableName, sources, targets, options, history.OperationMerge)
		return err
	})
	return
}

// Rows is the autocommit executor.Rows backed by db, together with the
// optional surfaces the executor discovers by type assertion (configuration,
// committed changes, route traces).
type Rows struct {
	operations
	*DB
}

// Rows returns the autocommit row surface.
func (db *DB) Rows() *Rows { return &Rows{operations: db.ops(), DB: db} }

// Transaction is one explicit BEGIN … COMMIT. Its reads see its own writes.
type Transaction struct {
	operations
	db     *DB
	t      *tx
	mu     sync.Mutex
	closed bool
}

// BeginTransaction starts an explicit transaction. It holds the write lock
// until Commit or Rollback: writers are serial.
func (db *DB) BeginTransaction(ctx context.Context) (*Transaction, error) {
	t, err := db.begin(ctx)
	if err != nil {
		return nil, err
	}
	transaction := &Transaction{db: db, t: t}
	transaction.operations = operations{run: func(ctx context.Context, _ bool, fn func(*tx) error) error {
		transaction.mu.Lock()
		defer transaction.mu.Unlock()
		if transaction.closed {
			return fail(result.CodeInvalidTransaction, "transaction is closed")
		}
		if metadata, ok := change.MetadataFrom(ctx); ok {
			transaction.t.claimAttribution(metadata)
		}
		return fn(transaction.t)
	}}
	return transaction, nil
}

func (transaction *Transaction) Commit() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return fail(result.CodeInvalidTransaction, "transaction is closed")
	}
	transaction.closed = true
	return transaction.t.commit(context.Background())
}

func (transaction *Transaction) Rollback() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.closed {
		return nil
	}
	transaction.closed = true
	return transaction.t.rollback()
}

func (transaction *Transaction) DescribeDatabase(ctx context.Context, name string) (value catalog.Database, err error) {
	err = transaction.run(ctx, false, func(t *tx) error {
		value, err = t.resolveDatabase(ctx, name)
		if err == nil && value.Archived() {
			err = catalogError(catalog.CodeArchived, "database", value.Name, "")
		}
		if err == nil {
			value.Tables = liveTables(value.Tables)
		}
		return err
	})
	return
}

func (transaction *Transaction) DescribeTable(ctx context.Context, databaseName, tableName string) (value catalog.Table, err error) {
	err = transaction.run(ctx, false, func(t *tx) error { value, err = t.liveTable(ctx, databaseName, tableName); return err })
	return
}

// ---- SPLIT / MERGE as deprecation with successors ----

func (t *tx) reshape(ctx context.Context, databaseName, tableName string, sources []string, targets []map[string]any,
	options row.ReshapeOptions, operation history.Operation) ([]row.Row, error) {
	if len(sources) == 0 || len(targets) == 0 {
		return nil, fail(result.CodeValidation, "%s needs sources and targets", operation)
	}
	if operation == history.OperationSplit && (len(sources) != 1 || len(targets) < 2) {
		return nil, fail(result.CodeValidation, "SPLIT turns one row into two or more")
	}
	if operation == history.OperationMerge && (len(sources) < 2 || len(targets) != 1) {
		return nil, fail(result.CodeValidation, "MERGE turns two or more rows into one")
	}
	table, err := t.liveTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	if options.ExpectedSchemaVersion != 0 && options.ExpectedSchemaVersion != table.SchemaVersion {
		return nil, fail(result.CodeRevisionConflict, "schema version %d does not match table %q at %d",
			options.ExpectedSchemaVersion, table.Name, table.SchemaVersion)
	}
	metadata := options.Metadata
	created := make([]row.Row, 0, len(targets))
	successorIDs := []string{}
	for index, values := range targets {
		write := row.WriteOptions{Metadata: metadata}
		if index < len(options.TargetRouteLeafIDs) {
			write.RouteLeafIDs = options.TargetRouteLeafIDs[index]
		}
		value, err := t.insert(ctx, databaseName, tableName, values, write)
		if err != nil {
			return nil, err
		}
		created = append(created, value)
		successorIDs = append(successorIDs, value.ID)
	}
	for _, sourceID := range sources {
		expected := options.SourceRevisions[sourceID]
		if expected == 0 && len(sources) == 1 {
			expected = options.ExpectedRevision
		}
		_, value, err := t.liveRowForWrite(ctx, databaseName, tableName, sourceID, expected)
		if err != nil {
			return nil, err
		}
		if err := t.advance(ctx, table, &value); err != nil {
			return nil, err
		}
		value.State = row.StateSuperseded
		value.SuccessorIDs = successorIDs
		if err := t.writeRow(ctx, table, value, false); err != nil {
			return nil, err
		}
		if err := t.appendHistory(ctx, table, value, operation, metadata); err != nil {
			return nil, err
		}
		kind := change.OperationSplit
		if operation == history.OperationMerge {
			kind = change.OperationMerge
		}
		t.rowChange(table, value, kind, metadata, successorIDs)
	}
	return created, nil
}

// Successors follows a superseded row to the rows that replaced it.
func (db *DB) Successors(ctx context.Context, databaseName, tableName, rowID string) ([]string, error) {
	var resolved []string
	err := db.view(ctx, func(t *tx) error {
		table, err := t.liveTable(ctx, databaseName, tableName)
		if err != nil {
			return err
		}
		frontier := []string{rowID}
		for hops := 0; hops < maxSuccessorHops; hops++ {
			next := []string{}
			settled := true
			for _, id := range frontier {
				value, err := t.readRow(ctx, table, id)
				if err != nil {
					return err
				}
				if value.State == row.StateSuperseded && len(value.SuccessorIDs) > 0 {
					next = append(next, value.SuccessorIDs...)
					settled = false
				} else {
					next = append(next, id)
				}
			}
			frontier = dedupe(next)
			if settled {
				break
			}
		}
		resolved = frontier
		return nil
	})
	return resolved, err
}
