package nativemutation

// This file is deliberately a small MSQL-facing transaction adapter. It
// buffers a validated mutation plan in memory and hands the complete plan to
// Coordinator only at COMMIT; the coordinator then uses one native store
// transaction and one Page publication.

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/google/uuid"
)

// Transaction is the native implementation of the executor's
// backend-neutral ExplicitTransaction contract. The write gate is held from
// BEGIN through COMMIT/ROLLBACK, so sequence reservation and validation are
// serialized with ordinary native writes.
type Transaction struct {
	service        *Service
	release        func()
	commitSequence uint64
	plan           Plan
	rows           map[string]row.Row
	relations      map[string]relation.Relation
	closed         bool
}

// BeginExplicitTransaction is the only native transaction entry point used
// by the daemon composition root. Agent code never sees this method.
func (service *Service) BeginExplicitTransaction(ctx context.Context) (*Transaction, error) {
	if service == nil || service.Service == nil || service.rows == nil || service.routes == nil || service.commit == nil {
		return nil, fmt.Errorf("native transaction service is incomplete")
	}
	release, err := service.Service.BeginAuthorityWrite(ctx)
	if err != nil {
		return nil, err
	}
	sequence, err := service.Service.NextCommitSequence(ctx)
	if err != nil {
		release()
		return nil, err
	}
	return &Transaction{
		service: service, release: release, commitSequence: sequence,
		rows:      make(map[string]row.Row),
		relations: make(map[string]relation.Relation),
	}, nil
}

func (transaction *Transaction) Commit() error {
	if transaction == nil || transaction.closed {
		return fmt.Errorf("native transaction is closed")
	}
	transaction.closed = true
	defer transaction.release()
	if len(transaction.plan.Changes) == 0 && len(transaction.plan.Relations) == 0 {
		return nil
	}
	transaction.plan.Memberships = append([]router.Membership(nil), transaction.plan.Memberships...)
	if err := transaction.service.commit.Commit(transaction.plan); err != nil {
		return err
	}
	return nil
}

func (transaction *Transaction) Rollback() error {
	if transaction == nil || transaction.closed {
		return nil
	}
	transaction.closed = true
	transaction.release()
	transaction.plan = Plan{}
	transaction.rows = nil
	transaction.relations = nil
	return nil
}

func (transaction *Transaction) ensureOpen(ctx context.Context) error {
	if transaction == nil || transaction.closed {
		return fmt.Errorf("native transaction is closed")
	}
	if ctx == nil {
		return context.Canceled
	}
	return ctx.Err()
}

func (transaction *Transaction) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return catalog.Database{}, err
	}
	return transaction.service.catalog.DescribeDatabase(ctx, name)
}

func (transaction *Transaction) DescribeTable(ctx context.Context, database, table string) (catalog.Table, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return catalog.Table{}, err
	}
	return transaction.service.catalog.DescribeTable(ctx, database, table)
}

func (transaction *Transaction) Get(ctx context.Context, database, table, id string) (row.Row, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return row.Row{}, err
	}
	value, ok := transaction.rows[id]
	if ok {
		definition, err := transaction.DescribeTable(ctx, database, table)
		if err != nil || value.DatabaseID != definition.DatabaseID || value.TableID != definition.ID || value.State != row.StateLive {
			return row.Row{}, transactionFailure(result.CodeNotFound, "Row was not found", err)
		}
		return projectTransactionRow(definition, value), nil
	}
	return transaction.service.Service.Get(ctx, database, table, id)
}

func (transaction *Transaction) ListPage(ctx context.Context, database, table string, limit int) ([]row.Row, bool, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return nil, false, err
	}
	if limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("invalid Row limit")
	}
	definition, err := transaction.DescribeTable(ctx, database, table)
	if err != nil {
		return nil, false, err
	}
	values, more, err := transaction.service.rows.List(definition.DatabaseID, definition.ID, 1000)
	if err != nil {
		return nil, false, err
	}
	byID := make(map[string]row.Row, len(values)+len(transaction.rows))
	for _, value := range values {
		byID[value.ID] = value
	}
	for id, value := range transaction.rows {
		if value.DatabaseID == definition.DatabaseID && value.TableID == definition.ID {
			if value.State == row.StateDeleted {
				delete(byID, id)
			} else {
				byID[id] = value
			}
		}
	}
	values = values[:0]
	for _, value := range byID {
		values = append(values, projectTransactionRow(definition, value))
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	if len(values) > limit {
		return values[:limit], true, nil
	}
	return values, more, nil
}

func (transaction *Transaction) Insert(ctx context.Context, database, table string, values map[string]any, options row.WriteOptions) (row.Row, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return row.Row{}, err
	}
	definition, err := transaction.DescribeTable(ctx, database, table)
	if err != nil {
		return row.Row{}, err
	}
	if options.ExpectedSchemaVersion != definition.SchemaVersion {
		return row.Row{}, transactionFailure(result.CodeRevisionConflict, "schema version conflicts with native catalog", nil)
	}
	bound, err := transaction.bindValues(definition, values, false)
	if err != nil {
		return row.Row{}, err
	}
	id := "row_" + uuid.NewString()
	now := time.Now().UTC()
	value := row.Row{ID: id, DatabaseID: definition.DatabaseID, TableID: definition.ID,
		SchemaVersion: definition.SchemaVersion, Revision: 1, CommitSequence: transaction.commitSequence,
		State: row.StateLive, Values: bound, CreatedAt: now, UpdatedAt: now}
	if err := transaction.addRowChange(ctx, definition, value, history.OperationInsert, options.Metadata, true, options.RouteLeafIDs); err != nil {
		return row.Row{}, err
	}
	return projectTransactionRow(definition, value), nil
}

func (transaction *Transaction) Update(ctx context.Context, database, table, id string, changes map[string]any, options row.WriteOptions) (row.Row, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return row.Row{}, err
	}
	definition, current, err := transaction.mutationTarget(ctx, database, table, id, options)
	if err != nil {
		return row.Row{}, err
	}
	bound, err := transaction.bindValues(definition, changes, true)
	if err != nil {
		return row.Row{}, err
	}
	updated := current
	updated.Values = cloneTransactionValues(current.Values)
	for key, value := range bound {
		updated.Values[key] = value
	}
	updated.Revision++
	updated.CommitSequence = transaction.commitSequence
	updated.SchemaVersion = definition.SchemaVersion
	updated.UpdatedAt = time.Now().UTC()
	if err := transaction.addRowChange(ctx, definition, updated, history.OperationUpdate, options.Metadata, false, options.RouteLeafIDs); err != nil {
		return row.Row{}, err
	}
	return projectTransactionRow(definition, updated), nil
}

func (transaction *Transaction) Delete(ctx context.Context, database, table, id string, options row.WriteOptions) (row.Row, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return row.Row{}, err
	}
	definition, current, err := transaction.mutationTarget(ctx, database, table, id, options)
	if err != nil {
		return row.Row{}, err
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence = transaction.commitSequence
	deleted.State = row.StateDeleted
	deleted.SchemaVersion = definition.SchemaVersion
	deleted.UpdatedAt = time.Now().UTC()
	if err := transaction.addRowChange(ctx, definition, deleted, history.OperationDelete, options.Metadata, false, []string{}); err != nil {
		return row.Row{}, err
	}
	return projectTransactionRow(definition, deleted), nil
}

func (transaction *Transaction) Restore(ctx context.Context, database, table, id string, target uint64, options row.WriteOptions) (row.Row, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return row.Row{}, err
	}
	definition, current, err := transaction.mutationTargetIncludingDeleted(ctx, database, table, id, options)
	if err != nil {
		return row.Row{}, err
	}
	previous, err := transaction.service.Service.AsOfRevision(ctx, database, table, id, target)
	if err != nil {
		return row.Row{}, err
	}
	values, err := transaction.bindValues(definition, previous.Values, false)
	if err != nil {
		return row.Row{}, err
	}
	if previous.State == row.StateLive && options.RouteLeafIDs == nil {
		return row.Row{}, transactionFailure(result.CodeValidation, "RESTORE to a live Row requires a complete Route snapshot", nil)
	}
	restored := current
	restored.Values = values
	restored.Revision++
	restored.CommitSequence = transaction.commitSequence
	restored.SchemaVersion = definition.SchemaVersion
	restored.State = previous.State
	restored.UpdatedAt = time.Now().UTC()
	desired := options.RouteLeafIDs
	if restored.State == row.StateDeleted {
		desired = []string{}
	}
	if err := transaction.addRowChange(ctx, definition, restored, history.OperationCompensate, options.Metadata, false, desired); err != nil {
		return row.Row{}, err
	}
	return projectTransactionRow(definition, restored), nil
}

func (transaction *Transaction) AsOfRevision(ctx context.Context, database, table, id string, revision uint64) (row.Row, error) {
	if staged, ok := transaction.rows[id]; ok && staged.Revision == revision {
		definition, err := transaction.DescribeTable(ctx, database, table)
		if err != nil {
			return row.Row{}, err
		}
		return projectTransactionRow(definition, staged), nil
	}
	return transaction.service.Service.AsOfRevision(ctx, database, table, id, revision)
}

func (transaction *Transaction) AsOfCommit(ctx context.Context, database, table, id string, sequence uint64) (row.Row, error) {
	if staged, ok := transaction.rows[id]; ok && staged.CommitSequence <= sequence {
		definition, err := transaction.DescribeTable(ctx, database, table)
		if err != nil {
			return row.Row{}, err
		}
		return projectTransactionRow(definition, staged), nil
	}
	return transaction.service.Service.AsOfCommit(ctx, database, table, id, sequence)
}

func (transaction *Transaction) HistoryPage(ctx context.Context, database, table, id, cursor string, limit int) ([]history.Record, history.ReadPage, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return nil, history.ReadPage{}, err
	}
	return transaction.service.Service.HistoryPage(ctx, database, table, id, cursor, limit)
}

func (transaction *Transaction) Relate(ctx context.Context, definition row.RelationDefinition) (relation.Relation, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return relation.Relation{}, err
	}
	source, err := transaction.resolveEndpoint(ctx, definition.Source)
	if err != nil {
		return relation.Relation{}, err
	}
	target, err := transaction.resolveEndpoint(ctx, definition.Target)
	if err != nil {
		return relation.Relation{}, err
	}
	if source.DatabaseID != target.DatabaseID {
		return relation.Relation{}, transactionFailure(result.CodeConstraint, "cross-database relation requires an explicit policy", nil)
	}
	if !utf8.ValidString(definition.Type) || len([]rune(definition.Type)) < 1 || len([]rune(definition.Type)) > 128 || !utf8.ValidString(definition.Description) || len([]rune(definition.Description)) > 1200 {
		return relation.Relation{}, transactionFailure(result.CodeConstraint, "relation type or description exceeds its semantic budget", nil)
	}
	existing, err := transaction.service.rows.ListRelations(source, true)
	if err != nil {
		return relation.Relation{}, err
	}
	for _, value := range existing {
		if value.Type == definition.Type && value.Target == target {
			return relation.Relation{}, transactionFailure(result.CodeAlreadyExists, "relation already exists", nil)
		}
	}
	for _, value := range transaction.relations {
		if value.Source == source && value.Type == definition.Type && value.Target == target && value.State == relation.StateLive {
			return relation.Relation{}, transactionFailure(result.CodeAlreadyExists, "relation already exists", nil)
		}
	}
	value := relation.Relation{Version: relation.Version, ID: "rel_" + uuid.NewString(), Source: source, Type: definition.Type,
		Target: target, Description: definition.Description, Revision: 1, CommitSequence: transaction.commitSequence,
		State: relation.StateLive, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	transaction.relations[value.ID] = value
	transaction.plan.Relations = append(transaction.plan.Relations, value)
	return value, nil
}

func (transaction *Transaction) GetRelation(ctx context.Context, id string) (relation.Relation, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return relation.Relation{}, err
	}
	if value, ok := transaction.relations[id]; ok && value.State == relation.StateLive {
		return value, nil
	}
	return transaction.service.Service.GetRelation(ctx, id)
}

func (transaction *Transaction) DeleteRelation(ctx context.Context, id string, expected uint64) (relation.Relation, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return relation.Relation{}, err
	}
	current, ok := transaction.relations[id]
	if !ok {
		var err error
		current, err = transaction.service.rows.GetRelation(id, false)
		if err != nil {
			return relation.Relation{}, err
		}
	}
	if current.Revision != expected {
		return relation.Relation{}, transactionFailure(result.CodeRevisionConflict, "relation revision conflicts with latest", nil)
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence = transaction.commitSequence
	deleted.State = relation.StateDeleted
	deleted.UpdatedAt = time.Now().UTC()
	transaction.relations[id] = deleted
	transaction.plan.Relations = append(transaction.plan.Relations, deleted)
	return deleted, nil
}

func (transaction *Transaction) ListOutgoingRelations(ctx context.Context, endpoint row.RelationEndpoint) ([]relation.Relation, error) {
	return transaction.listRelations(ctx, endpoint, true)
}

func (transaction *Transaction) ListIncomingRelations(ctx context.Context, endpoint row.RelationEndpoint) ([]relation.Relation, error) {
	return transaction.listRelations(ctx, endpoint, false)
}

func (transaction *Transaction) listRelations(ctx context.Context, endpoint row.RelationEndpoint, outgoing bool) ([]relation.Relation, error) {
	if err := transaction.ensureOpen(ctx); err != nil {
		return nil, err
	}
	resolved, err := transaction.resolveEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	values, err := transaction.service.rows.ListRelations(resolved, outgoing)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]relation.Relation, len(values)+len(transaction.relations))
	for _, value := range values {
		byID[value.ID] = value
	}
	for id, value := range transaction.relations {
		if value.State == relation.StateDeleted {
			delete(byID, id)
		} else if (outgoing && value.Source == resolved) || (!outgoing && value.Target == resolved) {
			byID[id] = value
		}
	}
	values = values[:0]
	for _, value := range byID {
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].ID < values[right].ID })
	return values, nil
}

func (transaction *Transaction) CreateRouterNode(context.Context, string, router.NodeDefinition) (router.Node, error) {
	return router.Node{}, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) RenameRouterNode(context.Context, string, string, uint64) (router.Node, error) {
	return router.Node{}, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) DeleteRouterNode(context.Context, string, uint64) (uint64, error) {
	return 0, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) RestoreRouterNode(context.Context, string, uint64) (uint64, error) {
	return 0, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) GetRouterNode(context.Context, string) (router.Node, error) {
	return router.Node{}, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) GetArchivedRouterNode(context.Context, string) (router.Node, error) {
	return router.Node{}, transactionFailure(result.CodeUnsupported, "Route mutations are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) ListRouterChildrenPage(context.Context, string, string, int) ([]router.Node, router.ReadPage, error) {
	return nil, router.ReadPage{}, transactionFailure(result.CodeUnsupported, "Route reads are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) ListRouterLeafPage(context.Context, string, string, int) ([]router.Locator, router.ReadPage, error) {
	return nil, router.ReadPage{}, transactionFailure(result.CodeUnsupported, "Route reads are not supported inside an explicit transaction", nil)
}
func (transaction *Transaction) MembershipsForRow(context.Context, string, string, string) ([]router.Membership, error) {
	return nil, transactionFailure(result.CodeUnsupported, "Route reads are not supported inside an explicit transaction", nil)
}

func (transaction *Transaction) mutationTarget(ctx context.Context, database, table, id string, options row.WriteOptions) (catalog.Table, row.Row, error) {
	definition, err := transaction.DescribeTable(ctx, database, table)
	if err != nil {
		return catalog.Table{}, row.Row{}, err
	}
	current, ok := transaction.rows[id]
	if !ok {
		current, err = transaction.service.Service.CurrentBody(ctx, definition, id)
		if err != nil {
			return catalog.Table{}, row.Row{}, transactionFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", id), err)
		}
	}
	if current.DatabaseID != definition.DatabaseID || current.TableID != definition.ID || current.State != row.StateLive {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", id), nil)
	}
	if options.ExpectedSchemaVersion != definition.SchemaVersion || options.ExpectedRevision != current.Revision {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeRevisionConflict, "row or schema revision conflicts with latest", nil)
	}
	if _, exists := transaction.rows[id]; exists {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeInvalidTransaction, "a Row may only be mutated once per native transaction", nil)
	}
	return definition, current, nil
}

func (transaction *Transaction) mutationTargetIncludingDeleted(ctx context.Context, database, table, id string, options row.WriteOptions) (catalog.Table, row.Row, error) {
	definition, err := transaction.DescribeTable(ctx, database, table)
	if err != nil {
		return catalog.Table{}, row.Row{}, err
	}
	current, ok := transaction.rows[id]
	if !ok {
		current, err = transaction.service.Service.CurrentBody(ctx, definition, id)
		if err != nil {
			return catalog.Table{}, row.Row{}, transactionFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", id), err)
		}
	}
	if current.DatabaseID != definition.DatabaseID || current.TableID != definition.ID || current.State == row.StateSuperseded {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", id), nil)
	}
	if options.ExpectedSchemaVersion != definition.SchemaVersion || options.ExpectedRevision != current.Revision {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeRevisionConflict, "row or schema revision conflicts with latest", nil)
	}
	if _, exists := transaction.rows[id]; exists {
		return catalog.Table{}, row.Row{}, transactionFailure(result.CodeInvalidTransaction, "a Row may only be mutated once per native transaction", nil)
	}
	return definition, current, nil
}

func (transaction *Transaction) resolveEndpoint(ctx context.Context, endpoint row.RelationEndpoint) (relation.Endpoint, error) {
	if strings.TrimSpace(endpoint.Database) == "" || strings.TrimSpace(endpoint.Table) == "" || strings.TrimSpace(endpoint.RowID) == "" {
		return relation.Endpoint{}, transactionFailure(result.CodeValidation, "relation endpoint requires database, table, and row ID", nil)
	}
	table, err := transaction.DescribeTable(ctx, endpoint.Database, endpoint.Table)
	if err != nil {
		return relation.Endpoint{}, err
	}
	value, ok := transaction.rows[endpoint.RowID]
	if !ok {
		value, err = transaction.service.Service.CurrentBody(ctx, table, endpoint.RowID)
	}
	if err != nil || value.State != row.StateLive || value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return relation.Endpoint{}, transactionFailure(result.CodeNotFound, "relation endpoint Row was not found", err)
	}
	return relation.Endpoint{DatabaseID: table.DatabaseID, TableID: table.ID, RowID: endpoint.RowID}, nil
}

func (transaction *Transaction) addRowChange(ctx context.Context, table catalog.Table, value row.Row, operation history.Operation, metadata row.WriteMetadata, initial bool, desiredRoutes []string) error {
	if _, exists := transaction.rows[value.ID]; exists {
		return transactionFailure(result.CodeInvalidTransaction, "a Row may only be mutated once per native transaction", nil)
	}
	if err := validateTransactionValues(table, value.Values); err != nil {
		return err
	}
	routes, err := transaction.membershipChanges(value, desiredRoutes)
	if err != nil {
		return err
	}
	transaction.rows[value.ID] = value
	transaction.plan.Changes = append(transaction.plan.Changes, RowChange{
		Row: value, Operation: operation, Metadata: metadata, RecordedAt: value.UpdatedAt, Initial: initial,
	})
	transaction.plan.Memberships = append(transaction.plan.Memberships, routes...)
	return nil
}

func (transaction *Transaction) membershipChanges(value row.Row, desired []string) ([]router.Membership, error) {
	current, err := transaction.service.routes.MembershipsIncludingDeleted(value.ID)
	if err != nil {
		return nil, err
	}
	if desired == nil {
		desired = make([]string, 0, len(current))
		for _, membership := range current {
			if !membership.Deleted {
				desired = append(desired, membership.LeafID)
			}
		}
	}
	wanted := make(map[string]bool, len(desired))
	for _, leaf := range desired {
		if wanted[leaf] {
			return nil, fmt.Errorf("duplicate Route membership %q", leaf)
		}
		wanted[leaf] = true
	}
	changes := make([]router.Membership, 0, len(current)+len(wanted))
	for _, membership := range current {
		keep := wanted[membership.LeafID]
		if keep {
			delete(wanted, membership.LeafID)
		} else if membership.Deleted {
			continue
		}
		membership.MembershipRevision++
		membership.Revision = value.Revision
		membership.Deleted = !keep
		changes = append(changes, membership)
	}
	for leaf := range wanted {
		changes = append(changes, router.Membership{LeafID: leaf, MembershipRevision: 1,
			Locator: router.Locator{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID, Revision: value.Revision}})
	}
	return changes, nil
}

func (transaction *Transaction) bindValues(table catalog.Table, values map[string]any, partial bool) (map[string]any, error) {
	bound := make(map[string]any, len(table.Columns))
	for name, value := range values {
		var column catalog.Column
		found := false
		candidate := strings.ToLower(strings.TrimSpace(name))
		for _, item := range table.Columns {
			if strings.ToLower(item.Name) == candidate || strings.ToLower(item.ID) == candidate {
				column, found = item, true
				break
			}
			for _, alias := range item.Aliases {
				if strings.ToLower(alias) == candidate {
					column, found = item, true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown column %q", name)
		}
		if _, duplicate := bound[column.ID]; duplicate {
			return nil, fmt.Errorf("column %q is specified more than once", name)
		}
		normalized, err := column.Validate(value)
		if err != nil {
			return nil, transactionFailure(result.CodeConstraint, fmt.Sprintf("column %q: %v", column.Name, err), err)
		}
		bound[column.ID] = normalized
	}
	if !partial {
		for _, column := range table.Columns {
			if _, exists := bound[column.ID]; !exists {
				if !column.Nullable {
					return nil, transactionFailure(result.CodeConstraint, fmt.Sprintf("missing column %q", column.Name), nil)
				}
				bound[column.ID] = nil
			}
		}
	}
	return bound, nil
}

func validateTransactionValues(table catalog.Table, values map[string]any) error {
	for _, column := range table.Columns {
		value, ok := values[column.ID]
		if !ok {
			return transactionFailure(result.CodeConstraint, fmt.Sprintf("missing column %q", column.Name), nil)
		}
		if _, err := column.Validate(value); err != nil {
			return transactionFailure(result.CodeConstraint, fmt.Sprintf("column %q: %v", column.Name, err), err)
		}
	}
	return nil
}

func projectTransactionRow(table catalog.Table, value row.Row) row.Row {
	projected := value
	projected.Values = make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		projected.Values[column.Name] = value.Values[column.ID]
	}
	return projected
}

func cloneTransactionValues(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

var _ interface {
	DescribeDatabase(context.Context, string) (catalog.Database, error)
	DescribeTable(context.Context, string, string) (catalog.Table, error)
	Commit() error
	Rollback() error
} = (*Transaction)(nil)

func transactionFailure(code result.Code, message string, cause error) error {
	err := &ServiceError{Code: code, Message: message}
	if cause == nil {
		return err
	}
	return fmt.Errorf("%w: %v", err, cause)
}
