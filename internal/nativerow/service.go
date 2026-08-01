package nativerow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/google/uuid"
)

var ErrUnsupported = errors.New("native row capability is not implemented yet")

type ServiceError struct {
	Code    result.Code
	Message string
	Cause   error
}

func (err *ServiceError) Error() string      { return err.Message }
func (err *ServiceError) StableCode() string { return string(err.Code) }
func (err *ServiceError) Unwrap() error      { return err.Cause }

type IDSource interface{ Next() (string, error) }
type Clock interface{ Now() time.Time }

// PageAuthority owns current/version visibility after F107.
type PageAuthority interface {
	BeginWrite(context.Context) (func(), error)
	BeginRowWrite(context.Context, string, string, []string) (func(), error)
	NextChangeSequence(context.Context) (uint64, error)
	Capture(context.Context) (uint64, error)
	Get(context.Context, catalog.Table, string, uint64) (row.Row, error)
	CurrentIncludingDeleted(context.Context, catalog.Table, string) (row.Row, error)
	ListPage(context.Context, catalog.Table, int, uint64) ([]row.Row, bool, error)
	AsOfRevision(context.Context, catalog.Table, string, uint64, uint64) (row.Row, error)
	AsOfCommit(context.Context, catalog.Table, string, uint64, uint64) (row.Row, error)
	PublishRows(context.Context, []row.Row, func() error) error
}

type ServiceOptions struct {
	IDs       IDSource
	Clock     Clock
	Authority PageAuthority
}

type Service struct {
	repository *Repository
	catalog    *nativecatalog.Service
	ids        IDSource
	clock      Clock
	authority  PageAuthority
}

func NewService(repository *Repository, dictionary *nativecatalog.Service, options ServiceOptions) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{
		repository: repository, catalog: dictionary, ids: options.IDs, clock: options.Clock,
		authority: options.Authority,
	}
}

func (service *Service) Insert(ctx context.Context, databaseName, tableName string, values map[string]any, options row.WriteOptions) (row.Row, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return row.Row{}, err
	}
	defer release()
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	if options.ExpectedSchemaVersion != table.SchemaVersion {
		return row.Row{}, serviceFailure(result.CodeRevisionConflict, "schema version conflicts with native catalog", ErrRevisionConflict)
	}
	bound, err := bindValues(table, values)
	if err != nil {
		return row.Row{}, err
	}
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return row.Row{}, fmt.Errorf("allocate RowID: %w", err)
	}
	now := service.clock.Now().UTC()
	sequence, err := service.NextCommitSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	value := row.Row{ID: "row_" + id, DatabaseID: table.DatabaseID, TableID: table.ID, SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: sequence, State: row.StateLive, Values: bound, CreatedAt: now, UpdatedAt: now}
	changeSequence, err := service.NextChangeSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return row.Row{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.repository.StageSnapshotRow(transaction, value, table); err != nil {
		return row.Row{}, err
	}
	if err := service.repository.StageHistory(transaction, value, history.OperationInsert, options.Metadata, now); err != nil {
		return row.Row{}, err
	}
	routes := nativerouter.New(service.repository.file)
	memberships := make([]router.Membership, 0, len(options.RouteLeafIDs))
	for _, leafID := range options.RouteLeafIDs {
		membership := router.Membership{LeafID: leafID, MembershipRevision: 1, Locator: router.Locator{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID, Revision: value.Revision}}
		if err := routes.StageMembership(transaction, membership); err != nil {
			return row.Row{}, err
		}
		memberships = append(memberships, membership)
	}
	if err := stageRowChange(transaction, changeSequence, value, history.OperationInsert, options.Metadata, memberships); err != nil {
		return row.Row{}, err
	}
	commit := transaction.Commit
	if service.authority != nil {
		commit = func() error { return service.authority.PublishRows(ctx, []row.Row{value}, transaction.Commit) }
	}
	if err := commit(); err != nil {
		return row.Row{}, err
	}
	return project(table, value), nil
}

func (service *Service) Get(ctx context.Context, databaseName, tableName, rowID string) (row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	if service.authority != nil {
		snapshot, err := service.authority.Capture(ctx)
		if err != nil {
			return row.Row{}, err
		}
		return service.authority.Get(ctx, table, rowID, snapshot)
	}
	value, err := service.repository.Read(rowID)
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return row.Row{}, err
	}
	if value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return row.Row{}, fmt.Errorf("row %q does not belong to requested table", rowID)
	}
	return project(table, value), nil
}

func (service *Service) ListPage(ctx context.Context, databaseName, tableName string, limit int) ([]row.Row, bool, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, false, err
	}
	if service.authority != nil {
		snapshot, err := service.authority.Capture(ctx)
		if err != nil {
			return nil, false, err
		}
		return service.authority.ListPage(ctx, table, limit, snapshot)
	}
	values, more, err := service.repository.List(table.DatabaseID, table.ID, limit)
	for index := range values {
		values[index] = project(table, values[index])
	}
	return values, more, err
}

func project(table catalog.Table, value row.Row) row.Row {
	projected := value
	projected.Values = make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		projected.Values[column.Name] = value.Values[column.ID]
	}
	return projected
}

func bindValues(table catalog.Table, values map[string]any) (map[string]any, error) {
	bound := make(map[string]any, len(values))
	for name, value := range values {
		column, ok := resolveColumn(table, name)
		if !ok {
			return nil, fmt.Errorf("unknown column %q", name)
		}
		if _, duplicate := bound[column.ID]; duplicate {
			return nil, fmt.Errorf("column %q is specified more than once", name)
		}
		bound[column.ID] = value
	}
	return bound, nil
}

func resolveColumn(table catalog.Table, name string) (catalog.Column, bool) {
	candidate := strings.ToLower(strings.TrimSpace(name))
	for _, column := range table.Columns {
		if strings.ToLower(column.Name) == candidate || strings.ToLower(column.ID) == candidate {
			return column, true
		}
		for _, alias := range column.Aliases {
			if strings.ToLower(alias) == candidate {
				return column, true
			}
		}
	}
	return catalog.Column{}, false
}

func serviceFailure(code result.Code, message string, cause error) error {
	return &ServiceError{Code: code, Message: message, Cause: cause}
}

func (service *Service) Update(ctx context.Context, databaseName, tableName, rowID string, changes map[string]any, options row.WriteOptions) (row.Row, error) {
	lockTable, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	release, err := service.BeginRowAuthorityWrite(ctx, lockTable, []string{rowID})
	if err != nil {
		return row.Row{}, err
	}
	defer release()
	table, current, err := service.mutationTarget(ctx, databaseName, tableName, rowID, options)
	if err != nil {
		return row.Row{}, err
	}
	bound, err := bindValues(table, changes)
	if err != nil {
		return row.Row{}, err
	}
	updated := current
	updated.Values = cloneStableValues(current.Values)
	for columnID, value := range bound {
		updated.Values[columnID] = value
	}
	updated.Revision++
	updated.CommitSequence, err = service.NextCommitSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	updated.SchemaVersion = table.SchemaVersion
	updated.UpdatedAt = service.clock.Now().UTC()
	if err := service.commitRowRevision(ctx, updated, history.OperationUpdate, options.Metadata, options.RouteLeafIDs); err != nil {
		return row.Row{}, err
	}
	return project(table, updated), nil
}
func (service *Service) Delete(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (row.Row, error) {
	lockTable, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	release, err := service.BeginRowAuthorityWrite(ctx, lockTable, []string{rowID})
	if err != nil {
		return row.Row{}, err
	}
	defer release()
	table, current, err := service.mutationTarget(ctx, databaseName, tableName, rowID, options)
	if err != nil {
		return row.Row{}, err
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence, err = service.NextCommitSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	deleted.SchemaVersion = table.SchemaVersion
	deleted.State = row.StateDeleted
	deleted.UpdatedAt = service.clock.Now().UTC()
	emptyRoutes := []string{}
	if err := service.commitRowRevision(ctx, deleted, history.OperationDelete, options.Metadata, emptyRoutes); err != nil {
		return row.Row{}, err
	}
	return project(table, deleted), nil
}

func (service *Service) commitRowRevision(ctx context.Context, value row.Row, operation history.Operation, metadata row.WriteMetadata, desired []string) error {
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.repository.StageRevision(transaction, value); err != nil {
		return revisionError(err)
	}
	if err := service.repository.StageHistory(transaction, value, operation, metadata, value.UpdatedAt); err != nil {
		return err
	}
	routes := nativerouter.New(service.repository.file)
	current, err := routes.MembershipsIncludingDeleted(value.ID)
	if err != nil {
		return err
	}
	if desired == nil {
		desired = make([]string, 0, len(current))
		for _, membership := range current {
			if !membership.Deleted {
				desired = append(desired, membership.LeafID)
			}
		}
	}
	wanted := map[string]bool{}
	changedMemberships := make([]router.Membership, 0)
	for _, leafID := range desired {
		if wanted[leafID] {
			return fmt.Errorf("duplicate Route membership %q", leafID)
		}
		wanted[leafID] = true
	}
	for _, membership := range current {
		desiredMembership := containsRoute(desired, membership.LeafID)
		if desiredMembership {
			delete(wanted, membership.LeafID)
		} else if membership.Deleted {
			continue
		}
		membership.MembershipRevision++
		membership.Revision = value.Revision
		membership.Deleted = !desiredMembership
		if err := routes.StageMembership(transaction, membership); err != nil {
			return err
		}
		changedMemberships = append(changedMemberships, membership)
	}
	for leafID := range wanted {
		membership := router.Membership{LeafID: leafID, MembershipRevision: 1, Locator: router.Locator{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID, Revision: value.Revision}}
		if err := routes.StageMembership(transaction, membership); err != nil {
			return err
		}
		changedMemberships = append(changedMemberships, membership)
	}
	changeSequence, err := service.NextChangeSequence(ctx)
	if err != nil {
		return err
	}
	if err := stageRowChange(transaction, changeSequence, value, operation, metadata, changedMemberships); err != nil {
		return err
	}
	if service.authority != nil {
		return service.authority.PublishRows(ctx, []row.Row{value}, transaction.Commit)
	}
	return transaction.Commit()
}

func containsRoute(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func stageRowChange(
	transaction *nativestore.Transaction,
	changeSequence uint64,
	value row.Row,
	operation history.Operation,
	metadata row.WriteMetadata,
	memberships []router.Membership,
) error {
	entries := make([]change.Entry, 0, 1+len(memberships))
	related := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		related = append(related, membership.LeafID)
		entries = append(entries, nativechange.MembershipEntry(membership))
	}
	entries = append(entries, nativechange.RowEntry(value, operation, related...))
	envelope, err := change.NewEnvelope(
		changeSequence, value.UpdatedAt, nativechange.RowMetadata(metadata), entries,
	)
	if err != nil {
		return err
	}
	return nativechange.Stage(transaction, envelope)
}

func (service *Service) mutationTarget(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (catalog.Table, row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, row.Row{}, err
	}
	var current row.Row
	if service.authority != nil {
		current, err = service.authority.CurrentIncludingDeleted(ctx, table, rowID)
	} else {
		current, err = service.repository.Read(rowID)
	}
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return catalog.Table{}, row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return catalog.Table{}, row.Row{}, err
	}
	if current.DatabaseID != table.DatabaseID || current.TableID != table.ID {
		return catalog.Table{}, row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found in requested table", rowID), nil)
	}
	if current.State == row.StateDeleted || current.State == row.StateSuperseded {
		return catalog.Table{}, row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), nil)
	}
	if options.ExpectedSchemaVersion != table.SchemaVersion || options.ExpectedRevision != current.Revision {
		return catalog.Table{}, row.Row{}, serviceFailure(result.CodeRevisionConflict, "row or schema revision conflicts with latest", ErrRevisionConflict)
	}
	return table, current, nil
}

func revisionError(err error) error {
	if errors.Is(err, ErrRevisionConflict) {
		return serviceFailure(result.CodeRevisionConflict, "row revision conflicts with latest", err)
	}
	return err
}

func cloneStableValues(values map[string]any) map[string]any {
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}
func (service *Service) AsOfRevision(ctx context.Context, databaseName, tableName, rowID string, revision uint64) (row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	if service.authority != nil {
		snapshot, err := service.authority.Capture(ctx)
		if err != nil {
			return row.Row{}, err
		}
		return service.authority.AsOfRevision(ctx, table, rowID, revision, snapshot)
	}
	value, err := service.repository.ReadRevision(rowID, revision)
	if err != nil {
		return row.Row{}, err
	}
	if value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return row.Row{}, serviceFailure(result.CodeNotFound, "historical Row was not found in requested table", nil)
	}
	return project(table, value), nil
}
func (service *Service) AsOfCommit(
	ctx context.Context,
	databaseName, tableName, rowID string,
	commitSequence uint64,
) (row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	if service.authority != nil {
		snapshot, err := service.authority.Capture(ctx)
		if err != nil {
			return row.Row{}, err
		}
		return service.authority.AsOfCommit(ctx, table, rowID, commitSequence, snapshot)
	}
	value, err := service.repository.ReadAsOfCommit(rowID, commitSequence)
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, "no Row history is visible at the requested commit sequence", err)
		}
		return row.Row{}, err
	}
	if value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return row.Row{}, serviceFailure(result.CodeNotFound, "historical Row was not found in requested table", nil)
	}
	return project(table, value), nil
}
func (service *Service) HistoryPage(ctx context.Context, databaseName, tableName, rowID string, limit int) ([]history.Record, bool, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, false, err
	}
	return service.repository.History(table.DatabaseID, table.ID, rowID, limit)
}
func (service *Service) Restore(
	ctx context.Context,
	databaseName, tableName, rowID string,
	targetRevision uint64,
	options row.WriteOptions,
) (row.Row, error) {
	lockTable, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	release, err := service.BeginRowAuthorityWrite(ctx, lockTable, []string{rowID})
	if err != nil {
		return row.Row{}, err
	}
	defer release()
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	var current row.Row
	if service.authority != nil {
		current, err = service.authority.CurrentIncludingDeleted(ctx, table, rowID)
	} else {
		current, err = service.repository.ReadIncludingDeleted(rowID)
	}
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return row.Row{}, err
	}
	if current.DatabaseID != table.DatabaseID || current.TableID != table.ID {
		return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found in requested table", rowID), nil)
	}
	if options.ExpectedSchemaVersion != table.SchemaVersion || options.ExpectedRevision != current.Revision {
		return row.Row{}, serviceFailure(result.CodeRevisionConflict, "row or schema revision conflicts with latest", ErrRevisionConflict)
	}
	var target row.Row
	if service.authority != nil {
		snapshot, captureErr := service.authority.Capture(ctx)
		if captureErr != nil {
			return row.Row{}, captureErr
		}
		target, err = service.authority.AsOfRevision(ctx, table, rowID, targetRevision, snapshot)
		if err == nil {
			target.Values, err = bindValues(table, target.Values)
		}
	} else {
		target, err = service.repository.ReadRevision(rowID, targetRevision)
	}
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, "target Row revision was not found", err)
		}
		return row.Row{}, err
	}
	if target.DatabaseID != table.DatabaseID || target.TableID != table.ID {
		return row.Row{}, serviceFailure(result.CodeNotFound, "target Row revision belongs to another table", nil)
	}
	if target.State != row.StateLive && target.State != row.StateDeleted {
		return row.Row{}, serviceFailure(result.CodeConstraint, "target Row revision cannot be restored", nil)
	}
	values := make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		value, ok := target.Values[column.ID]
		if !ok {
			value = nil
		}
		normalized, validateErr := column.Validate(value)
		if validateErr != nil {
			return row.Row{}, serviceFailure(
				result.CodeConstraint,
				fmt.Sprintf("restore column %q: %v", column.Name, validateErr),
				validateErr,
			)
		}
		values[column.ID] = normalized
	}
	if target.State == row.StateLive && options.RouteLeafIDs == nil {
		return row.Row{}, serviceFailure(result.CodeValidation, "RESTORE to a live Row requires a complete Route snapshot", nil)
	}
	sequence, err := service.NextCommitSequence(ctx)
	if err != nil {
		return row.Row{}, err
	}
	restored := current
	restored.Values = values
	restored.State = target.State
	restored.SchemaVersion = table.SchemaVersion
	restored.Revision++
	restored.CommitSequence = sequence
	restored.UpdatedAt = service.clock.Now().UTC()
	desired := options.RouteLeafIDs
	if restored.State == row.StateDeleted {
		desired = []string{}
	}
	if err := service.commitRowRevision(ctx, restored, history.OperationCompensate, options.Metadata, desired); err != nil {
		return row.Row{}, err
	}
	return project(table, restored), nil
}
func (service *Service) Relate(ctx context.Context, definition row.RelationDefinition) (relation.Relation, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	defer release()
	source, err := service.relationEndpoint(ctx, definition.Source)
	if err != nil {
		return relation.Relation{}, err
	}
	target, err := service.relationEndpoint(ctx, definition.Target)
	if err != nil {
		return relation.Relation{}, err
	}
	if source.DatabaseID != target.DatabaseID {
		return relation.Relation{}, serviceFailure(result.CodeConstraint, "cross-database relation requires an explicit policy", nil)
	}
	if !utf8.ValidString(definition.Type) || len([]rune(definition.Type)) < 1 || len([]rune(definition.Type)) > 128 || !utf8.ValidString(definition.Description) || len([]rune(definition.Description)) > 1200 {
		return relation.Relation{}, serviceFailure(result.CodeConstraint, "relation type or description exceeds its semantic budget", nil)
	}
	existing, err := service.repository.ListRelations(source, true)
	if err != nil {
		return relation.Relation{}, err
	}
	for _, value := range existing {
		if value.Type == definition.Type && value.Target == target {
			return relation.Relation{}, serviceFailure(result.CodeAlreadyExists, "relation already exists", nil)
		}
	}
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return relation.Relation{}, fmt.Errorf("allocate relation ID: %w", err)
	}
	now := service.clock.Now().UTC()
	sequence, err := service.NextCommitSequence(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	created := relation.Relation{Version: relation.Version, ID: "rel_" + id, Source: source, Type: definition.Type, Target: target, Description: definition.Description, Revision: 1, CommitSequence: sequence, State: relation.StateLive, CreatedAt: now, UpdatedAt: now}
	if err := service.commitRelationChange(ctx, created); err != nil {
		return relation.Relation{}, err
	}
	return created, nil
}
func (service *Service) GetRelation(_ context.Context, id string) (relation.Relation, error) {
	value, err := service.repository.GetRelation(id, false)
	if errors.Is(err, nativestore.ErrNotFound) {
		return relation.Relation{}, serviceFailure(result.CodeNotFound, "relation was not found", err)
	}
	return value, err
}
func (service *Service) DeleteRelation(ctx context.Context, id string, expected uint64) (relation.Relation, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	defer release()
	current, err := service.repository.GetRelation(id, false)
	if err != nil {
		return relation.Relation{}, err
	}
	if current.Revision != expected {
		return relation.Relation{}, serviceFailure(result.CodeRevisionConflict, "relation revision conflicts with latest", ErrRevisionConflict)
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence, err = service.NextCommitSequence(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	deleted.State = relation.StateDeleted
	deleted.UpdatedAt = service.clock.Now().UTC()
	if err := service.commitRelationChange(ctx, deleted); err != nil {
		return relation.Relation{}, err
	}
	return deleted, nil
}

func (service *Service) commitRelationChange(ctx context.Context, value relation.Relation) error {
	changeSequence, err := service.NextChangeSequence(ctx)
	if err != nil {
		return err
	}
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.repository.StageRelation(transaction, value); err != nil {
		return err
	}
	envelope, err := change.NewEnvelope(
		changeSequence, value.UpdatedAt,
		change.Metadata{Actor: "system:direct-api", Source: "direct-api", Reason: "relation mutation"},
		[]change.Entry{nativechange.RelationEntry(value)},
	)
	if err != nil {
		return err
	}
	if err := nativechange.Stage(transaction, envelope); err != nil {
		return err
	}
	return transaction.Commit()
}
func (service *Service) ListOutgoingRelations(ctx context.Context, endpoint row.RelationEndpoint) ([]relation.Relation, error) {
	resolved, err := service.relationEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return service.repository.ListRelations(resolved, true)
}
func (service *Service) ListIncomingRelations(ctx context.Context, endpoint row.RelationEndpoint) ([]relation.Relation, error) {
	resolved, err := service.relationEndpoint(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return service.repository.ListRelations(resolved, false)
}

func (service *Service) relationEndpoint(ctx context.Context, endpoint row.RelationEndpoint) (relation.Endpoint, error) {
	table, err := service.catalog.DescribeTable(ctx, endpoint.Database, endpoint.Table)
	if err != nil {
		return relation.Endpoint{}, err
	}
	value, err := service.CurrentBody(ctx, table, endpoint.RowID)
	if err != nil || value.State != row.StateLive || value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return relation.Endpoint{}, serviceFailure(result.CodeNotFound, "relation endpoint Row was not found", err)
	}
	return relation.Endpoint{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID}, nil
}

// CurrentBody resolves an unprojected current revision for engine-owned
// mutation planning. It is not an MSQL read surface.
func (service *Service) CurrentBody(ctx context.Context, table catalog.Table, rowID string) (row.Row, error) {
	if service == nil || service.repository == nil {
		return row.Row{}, fmt.Errorf("native Row service is incomplete")
	}
	if service.authority != nil {
		return service.authority.CurrentIncludingDeleted(ctx, table, rowID)
	}
	return service.repository.ReadIncludingDeleted(rowID)
}

func (service *Service) BeginAuthorityWrite(ctx context.Context) (func(), error) {
	if service == nil {
		return nil, fmt.Errorf("native Row service is incomplete")
	}
	if service.authority == nil {
		return func() {}, nil
	}
	return service.authority.BeginWrite(ctx)
}

// NextCommitSequence preserves the Row/Relation snapshot sequence contract.
func (service *Service) NextCommitSequence(ctx context.Context) (uint64, error) {
	if service == nil || service.repository == nil || service.repository.file == nil || ctx == nil {
		return 0, fmt.Errorf("native Row service is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return service.repository.NextCommitSequence()
}

// NextChangeSequence is the independent global cursor for committed logical
// envelopes. Row snapshot sequences remain stable and are referenced through
// the envelope's History locator.
func (service *Service) NextChangeSequence(ctx context.Context) (uint64, error) {
	if service == nil || service.repository == nil || service.repository.file == nil || ctx == nil {
		return 0, fmt.Errorf("native Row service is incomplete")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if service.authority != nil {
		return service.authority.NextChangeSequence(ctx)
	}
	return nativechange.New(service.repository.file).NextSequence(0)
}

func (service *Service) BeginRowAuthorityWrite(
	ctx context.Context, table catalog.Table, rowIDs []string,
) (func(), error) {
	if service == nil {
		return nil, fmt.Errorf("native Row service is incomplete")
	}
	if service.authority == nil {
		return func() {}, nil
	}
	return service.authority.BeginRowWrite(ctx, table.DatabaseID, table.ID, rowIDs)
}
func (service *Service) CreateRouterRoot(context.Context, string, string) (router.Node, error) {
	return router.Node{}, ErrUnsupported
}
func (service *Service) CreateTableRouterRoot(ctx context.Context, databaseName, tableName, purpose, synopsis string) (router.Node, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer release()
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return router.Node{}, err
	}
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return router.Node{}, fmt.Errorf("allocate RouteID: %w", err)
	}
	routes := nativerouter.New(service.repository.file)
	return service.commitRouteNodeChange(ctx, change.OperationInsert, "create Route root", func(transaction *nativestore.Transaction) (router.Node, error) {
		return routes.StageRoot(transaction, "route_"+id, table.DatabaseID, table.ID, purpose, synopsis)
	})
}
func (service *Service) ListTableRouterRoots(_ context.Context, databaseID, tableID, cursor string, limit int) ([]router.Node, string, error) {
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("invalid Table Router limit")
	}
	routes := nativerouter.New(service.repository.file)
	roots := routes.Roots(tableID)
	if len(roots) != 1 || roots[0].DatabaseID != databaseID || roots[0].Deleted {
		return nil, "", nativestore.ErrNotFound
	}
	return routes.ShowUnder(roots[0].ID, cursor, limit)
}
func (service *Service) CreateRouterNode(ctx context.Context, parentID string, definition router.NodeDefinition) (router.Node, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer release()
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return router.Node{}, fmt.Errorf("allocate RouteID: %w", err)
	}
	routes := nativerouter.New(service.repository.file)
	return service.commitRouteNodeChange(ctx, change.OperationInsert, "create Route node", func(transaction *nativestore.Transaction) (router.Node, error) {
		return routes.StageChild(
			transaction, "route_"+id, parentID, definition.Name,
			definition.Kind, definition.Purpose, definition.Synopsis,
		)
	})
}
func (service *Service) RenameRouterNode(ctx context.Context, id, name string, expected uint64) (router.Node, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer release()
	routes := nativerouter.New(service.repository.file)
	current, err := routes.Get(id)
	if err != nil {
		return router.Node{}, err
	}
	if current.Revision != expected || current.Kind == router.KindRoot || strings.TrimSpace(name) == "" {
		return router.Node{}, ErrRevisionConflict
	}
	parent, err := routes.Get(current.ParentID)
	if err != nil {
		return router.Node{}, err
	}
	current.Name, current.Path, current.Revision = name, strings.TrimSuffix(parent.Path, "/")+"/"+name, current.Revision+1
	return service.commitRouteNodeChange(ctx, change.OperationUpdate, "rename Route node", func(transaction *nativestore.Transaction) (router.Node, error) {
		return current, routes.StageNode(transaction, current)
	})
}
func (service *Service) UpdateRouterSynopsis(ctx context.Context, id, synopsis string, expected uint64) (router.Node, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return router.Node{}, err
	}
	defer release()
	routes := nativerouter.New(service.repository.file)
	current, err := routes.Get(id)
	if err != nil {
		return router.Node{}, err
	}
	if current.Revision != expected {
		return router.Node{}, ErrRevisionConflict
	}
	current.Synopsis, current.Revision = synopsis, current.Revision+1
	return service.commitRouteNodeChange(ctx, change.OperationUpdate, "update Route synopsis", func(transaction *nativestore.Transaction) (router.Node, error) {
		return current, routes.StageNode(transaction, current)
	})
}
func (service *Service) DeleteRouterNode(ctx context.Context, id string, expected uint64) (uint64, error) {
	release, err := service.BeginAuthorityWrite(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	routes := nativerouter.New(service.repository.file)
	current, err := routes.Get(id)
	if err != nil {
		return 0, err
	}
	if current.Revision != expected || len(routes.Children(id)) > 0 {
		return 0, ErrRevisionConflict
	}
	current.Revision, current.Deleted = current.Revision+1, true
	committed, err := service.commitRouteNodeChange(ctx, change.OperationDelete, "delete Route node", func(transaction *nativestore.Transaction) (router.Node, error) {
		return current, routes.StageNode(transaction, current)
	})
	return committed.Revision, err
}

func (service *Service) commitRouteNodeChange(
	ctx context.Context,
	operation change.Operation,
	reason string,
	stage func(*nativestore.Transaction) (router.Node, error),
) (router.Node, error) {
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return router.Node{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	value, err := stage(transaction)
	if err != nil {
		return router.Node{}, err
	}
	sequence, err := service.NextChangeSequence(ctx)
	if err != nil {
		return router.Node{}, err
	}
	envelope, err := change.NewEnvelope(
		sequence, service.clock.Now().UTC(),
		change.Metadata{Actor: "system:direct-api", Source: "direct-api", Reason: reason},
		[]change.Entry{nativechange.RouteNodeEntry(value, operation)},
	)
	if err != nil {
		return router.Node{}, err
	}
	if err := nativechange.Stage(transaction, envelope); err != nil {
		return router.Node{}, err
	}
	return value, transaction.Commit()
}
func (service *Service) GetRouterNode(_ context.Context, id string) (router.Node, error) {
	value, err := nativerouter.New(service.repository.file).Get(id)
	if err != nil {
		return router.Node{}, err
	}
	if value.Deleted {
		return router.Node{}, nativestore.ErrNotFound
	}
	return value, nil
}
func (service *Service) ResolveRouterPath(_ context.Context, databaseID, path string) (router.Node, error) {
	routes := nativerouter.New(service.repository.file)
	databases, err := nativecatalog.New(service.repository.file).Read()
	if err != nil {
		return router.Node{}, err
	}
	for _, database := range databases {
		if database.ID != databaseID {
			continue
		}
		for _, table := range database.Tables {
			for _, root := range routes.Roots(table.ID) {
				if root.Path == path {
					return root, nil
				}
				if value, ok := findRoutePath(routes, root.ID, path); ok {
					return value, nil
				}
			}
		}
	}
	return router.Node{}, nativestore.ErrNotFound
}
func (service *Service) ListRouterChildren(_ context.Context, parentID, cursor string, limit int) ([]router.Node, string, error) {
	return nativerouter.New(service.repository.file).ShowUnder(parentID, cursor, limit)
}
func (service *Service) ListRouterLeaf(_ context.Context, leafID string, limit int) ([]router.Locator, bool, error) {
	return nativerouter.New(service.repository.file).Open(leafID, limit)
}

func findRoutePath(routes *nativerouter.Repository, parentID, path string) (router.Node, bool) {
	for _, child := range routes.Children(parentID) {
		if child.Path == path {
			return child, true
		}
		if child.Kind != router.KindLeaf {
			if value, ok := findRoutePath(routes, child.ID, path); ok {
				return value, true
			}
		}
	}
	return router.Node{}, false
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
