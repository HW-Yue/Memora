package nativerow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
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

type ServiceOptions struct {
	IDs   IDSource
	Clock Clock
}

type Service struct {
	repository *Repository
	catalog    *nativecatalog.Service
	ids        IDSource
	clock      Clock
}

func NewService(repository *Repository, dictionary *nativecatalog.Service, options ServiceOptions) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{repository: repository, catalog: dictionary, ids: options.IDs, clock: options.Clock}
}

func (service *Service) Insert(ctx context.Context, databaseName, tableName string, values map[string]any, options row.WriteOptions) (row.Row, error) {
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
	sequence, err := service.repository.NextCommitSequence()
	if err != nil {
		return row.Row{}, err
	}
	value := row.Row{ID: "row_" + id, DatabaseID: table.DatabaseID, TableID: table.ID, SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: sequence, State: row.StateLive, Values: bound, CreatedAt: now, UpdatedAt: now}
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
	for _, leafID := range options.RouteLeafIDs {
		membership := router.Membership{LeafID: leafID, MembershipRevision: 1, Locator: router.Locator{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID, Revision: value.Revision}}
		if err := routes.StageMembership(transaction, membership); err != nil {
			return row.Row{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return row.Row{}, err
	}
	return project(table, value), nil
}

func (service *Service) Get(ctx context.Context, databaseName, tableName, rowID string) (row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
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
	updated.CommitSequence, err = service.repository.NextCommitSequence()
	if err != nil {
		return row.Row{}, err
	}
	updated.SchemaVersion = table.SchemaVersion
	updated.UpdatedAt = service.clock.Now().UTC()
	if err := service.commitRowRevision(updated, history.OperationUpdate, options.Metadata, options.RouteLeafIDs); err != nil {
		return row.Row{}, err
	}
	return project(table, updated), nil
}
func (service *Service) Delete(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (row.Row, error) {
	table, current, err := service.mutationTarget(ctx, databaseName, tableName, rowID, options)
	if err != nil {
		return row.Row{}, err
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence, err = service.repository.NextCommitSequence()
	if err != nil {
		return row.Row{}, err
	}
	deleted.SchemaVersion = table.SchemaVersion
	deleted.State = row.StateDeleted
	deleted.UpdatedAt = service.clock.Now().UTC()
	emptyRoutes := []string{}
	if err := service.commitRowRevision(deleted, history.OperationDelete, options.Metadata, emptyRoutes); err != nil {
		return row.Row{}, err
	}
	return project(table, deleted), nil
}

func (service *Service) commitRowRevision(value row.Row, operation history.Operation, metadata row.WriteMetadata, desired []string) error {
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
	}
	for leafID := range wanted {
		membership := router.Membership{LeafID: leafID, MembershipRevision: 1, Locator: router.Locator{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID, Revision: value.Revision}}
		if err := routes.StageMembership(transaction, membership); err != nil {
			return err
		}
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

func (service *Service) mutationTarget(ctx context.Context, databaseName, tableName, rowID string, options row.WriteOptions) (catalog.Table, row.Row, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return catalog.Table{}, row.Row{}, err
	}
	current, err := service.repository.Read(rowID)
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return catalog.Table{}, row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return catalog.Table{}, row.Row{}, err
	}
	if current.DatabaseID != table.DatabaseID || current.TableID != table.ID {
		return catalog.Table{}, row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found in requested table", rowID), nil)
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
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return row.Row{}, err
	}
	current, err := service.repository.ReadIncludingDeleted(rowID)
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
	target, err := service.repository.ReadRevision(rowID, targetRevision)
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
	sequence, err := service.repository.NextCommitSequence()
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
	if err := service.commitRowRevision(restored, history.OperationCompensate, options.Metadata, desired); err != nil {
		return row.Row{}, err
	}
	return project(table, restored), nil
}
func (service *Service) Relate(ctx context.Context, definition row.RelationDefinition) (relation.Relation, error) {
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
	sequence, err := service.repository.NextCommitSequence()
	if err != nil {
		return relation.Relation{}, err
	}
	created := relation.Relation{Version: relation.Version, ID: "rel_" + id, Source: source, Type: definition.Type, Target: target, Description: definition.Description, Revision: 1, CommitSequence: sequence, State: relation.StateLive, CreatedAt: now, UpdatedAt: now}
	if err := service.repository.PutRelation(created); err != nil {
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
func (service *Service) DeleteRelation(_ context.Context, id string, expected uint64) (relation.Relation, error) {
	current, err := service.repository.GetRelation(id, false)
	if err != nil {
		return relation.Relation{}, err
	}
	if current.Revision != expected {
		return relation.Relation{}, serviceFailure(result.CodeRevisionConflict, "relation revision conflicts with latest", ErrRevisionConflict)
	}
	deleted := current
	deleted.Revision++
	deleted.CommitSequence, err = service.repository.NextCommitSequence()
	if err != nil {
		return relation.Relation{}, err
	}
	deleted.State = relation.StateDeleted
	deleted.UpdatedAt = service.clock.Now().UTC()
	if err := service.repository.PutRelation(deleted); err != nil {
		return relation.Relation{}, err
	}
	return deleted, nil
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
	value, err := service.repository.Read(endpoint.RowID)
	if err != nil || value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
		return relation.Endpoint{}, serviceFailure(result.CodeNotFound, "relation endpoint Row was not found", err)
	}
	return relation.Endpoint{DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID}, nil
}
func (service *Service) CreateRouterRoot(context.Context, string, string) (router.Node, error) {
	return router.Node{}, ErrUnsupported
}
func (service *Service) CreateTableRouterRoot(ctx context.Context, databaseName, tableName, purpose string) (router.Node, error) {
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return router.Node{}, err
	}
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return router.Node{}, fmt.Errorf("allocate RouteID: %w", err)
	}
	return nativerouter.New(service.repository.file).CreateRoot("route_"+id, table.DatabaseID, table.ID, purpose)
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
func (service *Service) CreateRouterNode(_ context.Context, parentID string, definition router.NodeDefinition) (router.Node, error) {
	id, err := service.ids.Next()
	if err != nil || strings.TrimSpace(id) == "" {
		return router.Node{}, fmt.Errorf("allocate RouteID: %w", err)
	}
	return nativerouter.New(service.repository.file).CreateChild("route_"+id, parentID, definition.Name, definition.Kind, definition.Purpose)
}
func (service *Service) RenameRouterNode(_ context.Context, id, name string, expected uint64) (router.Node, error) {
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
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return router.Node{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := routes.StageNode(transaction, current); err != nil {
		return router.Node{}, err
	}
	return current, transaction.Commit()
}
func (service *Service) DeleteRouterNode(_ context.Context, id string, expected uint64) (uint64, error) {
	routes := nativerouter.New(service.repository.file)
	current, err := routes.Get(id)
	if err != nil {
		return 0, err
	}
	if current.Revision != expected || len(routes.Children(id)) > 0 {
		return 0, ErrRevisionConflict
	}
	current.Revision, current.Deleted = current.Revision+1, true
	transaction, err := service.repository.file.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := routes.StageNode(transaction, current); err != nil {
		return 0, err
	}
	return current.Revision, transaction.Commit()
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
