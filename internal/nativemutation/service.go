package nativemutation

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/google/uuid"
)

type ServiceError struct {
	Code    result.Code
	Message string
}

func (err *ServiceError) Error() string      { return err.Message }
func (err *ServiceError) StableCode() string { return string(err.Code) }

// Service adds public semantic reshape operations to the native Row service.
// The embedded service remains the authority for ordinary Row operations.
type Service struct {
	*nativerow.Service
	catalog *nativecatalog.Service
	rows    *nativerow.Repository
	routes  *nativerouter.Repository
	commit  *Coordinator
}

func NewService(
	base *nativerow.Service,
	dictionary *nativecatalog.Service,
	rows *nativerow.Repository,
	routes *nativerouter.Repository,
	coordinator *Coordinator,
) *Service {
	return &Service{Service: base, catalog: dictionary, rows: rows, routes: routes, commit: coordinator}
}

func (service *Service) Split(
	ctx context.Context,
	databaseName, tableName string,
	sourceIDs []string,
	targetValues []map[string]any,
	options row.ReshapeOptions,
) ([]row.Row, error) {
	return service.reshape(ctx, history.OperationSplit, databaseName, tableName, sourceIDs, targetValues, options)
}

func (service *Service) Merge(
	ctx context.Context,
	databaseName, tableName string,
	sourceIDs []string,
	targetValues []map[string]any,
	options row.ReshapeOptions,
) ([]row.Row, error) {
	return service.reshape(ctx, history.OperationMerge, databaseName, tableName, sourceIDs, targetValues, options)
}

func (service *Service) reshape(
	ctx context.Context,
	operation history.Operation,
	databaseName, tableName string,
	sourceIDs []string,
	targetValues []map[string]any,
	options row.ReshapeOptions,
) ([]row.Row, error) {
	if service == nil || service.catalog == nil || service.rows == nil || service.routes == nil || service.commit == nil {
		return nil, reshapeError(result.CodeInternal, "native reshape service is incomplete")
	}
	if (operation == history.OperationSplit && (len(sourceIDs) != 1 || len(targetValues) != 2)) ||
		(operation == history.OperationMerge && (len(sourceIDs) < 2 || len(targetValues) != 1)) {
		return nil, reshapeError(result.CodeValidation, "%s shape is invalid", operation)
	}
	if len(options.TargetRouteLeafIDs) != len(targetValues) {
		return nil, reshapeError(result.CodeValidation, "one complete Route snapshot is required per target")
	}
	table, err := service.catalog.DescribeTable(ctx, databaseName, tableName)
	if err != nil {
		return nil, err
	}
	if table.SchemaVersion != options.ExpectedSchemaVersion {
		return nil, reshapeError(result.CodeRevisionConflict, "schema revision conflicts with latest")
	}
	sources, sourceSet, err := service.reshapeSources(table, sourceIDs, operation, options)
	if err != nil {
		return nil, err
	}
	sequence, err := service.rows.NextCommitSequence()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	changes := make([]RowChange, 0, len(sources))
	for _, source := range sources {
		source.Revision++
		source.CommitSequence = sequence
		source.SchemaVersion = table.SchemaVersion
		source.State = row.StateSuperseded
		source.UpdatedAt = now
		changes = append(changes, RowChange{
			Row: source, Operation: operation, Metadata: options.Metadata, RecordedAt: now,
		})
	}
	targets := make([]row.Row, 0, len(targetValues))
	targetChanges := make([]RowChange, 0, len(targetValues))
	for _, values := range targetValues {
		bound, bindErr := bindTarget(table, values)
		if bindErr != nil {
			return nil, bindErr
		}
		target := row.Row{
			ID: "row_" + uuid.NewString(), DatabaseID: table.DatabaseID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: sequence,
			State: row.StateLive, Values: bound, CreatedAt: now, UpdatedAt: now,
		}
		targets = append(targets, target)
		targetChanges = append(targetChanges, RowChange{
			Row: target, Operation: operation, Metadata: options.Metadata, RecordedAt: now, Initial: true,
		})
	}
	memberships, err := service.reshapeMemberships(sources, targets, options.TargetRouteLeafIDs)
	if err != nil {
		return nil, err
	}
	routeNodes, err := service.reshapeRouteNodes(options.RouteUpdates)
	if err != nil {
		return nil, err
	}
	relations, err := service.reshapeRelations(operation, sourceSet, targets, options.RelationTargetOrdinals, sequence, now)
	if err != nil {
		return nil, err
	}
	plan := ReshapePlan{
		Sources: Plan{
			Changes: changes, Relations: relations, Routes: routeNodes, Memberships: memberships,
		},
		Targets: targetChanges,
	}
	if operation == history.OperationSplit {
		err = service.commit.Split(plan)
	} else {
		err = service.commit.Merge(plan)
	}
	if err != nil {
		return nil, err
	}
	projected := make([]row.Row, 0, len(targets))
	for _, target := range targets {
		projected = append(projected, projectTarget(table, target))
	}
	return projected, nil
}

func (service *Service) reshapeRouteNodes(updates []row.RouteUpdate) ([]router.Node, error) {
	nodes := make([]router.Node, 0, len(updates))
	seen := map[string]bool{}
	for _, update := range updates {
		if seen[update.RouteID] {
			return nil, reshapeError(result.CodeValidation, "Route update %q is duplicated", update.RouteID)
		}
		seen[update.RouteID] = true
		current, err := service.routes.Get(update.RouteID)
		if err != nil {
			return nil, reshapeError(result.CodeNotFound, "Route %q was not found", update.RouteID)
		}
		if update.ExpectedRevision == 0 || current.Revision != update.ExpectedRevision {
			return nil, reshapeError(result.CodeRevisionConflict, "Route %q revision conflicts with latest", update.RouteID)
		}
		purpose := strings.TrimSpace(update.Purpose)
		if purpose == "" && update.Synopsis == nil {
			return nil, reshapeError(result.CodeValidation, "Route update purpose or synopsis is required")
		}
		if purpose != "" {
			current.Purpose = purpose
		}
		if update.Synopsis != nil {
			current.Synopsis = *update.Synopsis
		}
		current.Revision++
		nodes = append(nodes, current)
	}
	return nodes, nil
}

func (service *Service) reshapeSources(
	table catalog.Table,
	sourceIDs []string,
	operation history.Operation,
	options row.ReshapeOptions,
) ([]row.Row, map[string]bool, error) {
	values := make([]row.Row, 0, len(sourceIDs))
	seen := make(map[string]bool, len(sourceIDs))
	for _, sourceID := range sourceIDs {
		if seen[sourceID] {
			return nil, nil, reshapeError(result.CodeValidation, "source RowIDs must be unique")
		}
		seen[sourceID] = true
		value, err := service.rows.Read(sourceID)
		if err != nil || value.DatabaseID != table.DatabaseID || value.TableID != table.ID {
			return nil, nil, reshapeError(result.CodeNotFound, "source Row %q was not found in requested table", sourceID)
		}
		expected := options.SourceRevisions[sourceID]
		if operation == history.OperationSplit {
			expected = options.ExpectedRevision
		}
		if expected == 0 || value.Revision != expected {
			return nil, nil, reshapeError(result.CodeRevisionConflict, "source Row %q revision conflicts with latest", sourceID)
		}
		values = append(values, value)
	}
	return values, seen, nil
}

func bindTarget(table catalog.Table, values map[string]any) (map[string]any, error) {
	resultValues := make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		var input any
		found := false
		for name, value := range values {
			if strings.EqualFold(name, column.Name) || strings.EqualFold(name, column.ID) {
				input, found = value, true
				break
			}
			for _, alias := range column.Aliases {
				if strings.EqualFold(name, alias) {
					input, found = value, true
					break
				}
			}
		}
		if !found && !column.Nullable {
			return nil, reshapeError(result.CodeConstraint, "target is missing required column %q", column.Name)
		}
		normalized, err := column.Validate(input)
		if err != nil {
			return nil, reshapeError(result.CodeConstraint, "target column %q: %v", column.Name, err)
		}
		resultValues[column.ID] = normalized
	}
	return resultValues, nil
}

func projectTarget(table catalog.Table, value row.Row) row.Row {
	projected := value
	projected.Values = make(map[string]any, len(table.Columns))
	for _, column := range table.Columns {
		projected.Values[column.Name] = value.Values[column.ID]
	}
	return projected
}

func (service *Service) reshapeMemberships(
	sources, targets []row.Row,
	targetLeafIDs [][]string,
) ([]router.Membership, error) {
	resultMemberships := make([]router.Membership, 0)
	for _, source := range sources {
		current, err := service.routes.MembershipsIncludingDeleted(source.ID)
		if err != nil {
			return nil, err
		}
		for _, membership := range current {
			if membership.Deleted {
				continue
			}
			membership.MembershipRevision++
			membership.Revision = source.Revision
			membership.Deleted = true
			resultMemberships = append(resultMemberships, membership)
		}
	}
	for index, target := range targets {
		seen := map[string]bool{}
		for _, leafID := range targetLeafIDs[index] {
			if seen[leafID] {
				return nil, reshapeError(result.CodeValidation, "target Route snapshot contains duplicate leaf %q", leafID)
			}
			seen[leafID] = true
			resultMemberships = append(resultMemberships, router.Membership{
				LeafID: leafID, MembershipRevision: 1,
				Locator: router.Locator{
					DatabaseID: target.DatabaseID, TableID: target.TableID,
					RowID: target.ID, Revision: target.Revision,
				},
			})
		}
	}
	return resultMemberships, nil
}

func (service *Service) reshapeRelations(
	operation history.Operation,
	sourceSet map[string]bool,
	targets []row.Row,
	targetOrdinals map[string]int,
	sequence uint64,
	now time.Time,
) ([]relation.Relation, error) {
	latest := map[string]relation.Relation{}
	for sourceID := range sourceSet {
		source, err := service.rows.ReadIncludingDeleted(sourceID)
		if err != nil {
			return nil, err
		}
		endpoint := relation.Endpoint{DatabaseID: source.DatabaseID, TableID: source.TableID, RowID: source.ID}
		for _, outgoing := range []bool{true, false} {
			values, listErr := service.rows.ListRelations(endpoint, outgoing)
			if listErr != nil {
				return nil, listErr
			}
			for _, value := range values {
				latest[value.ID] = value
			}
		}
	}
	resultRelations := make([]relation.Relation, 0, len(latest)*2)
	signatures := map[string]bool{}
	for _, current := range latest {
		ordinal := 1
		if operation == history.OperationSplit {
			ordinal = targetOrdinals[current.ID]
			if ordinal < 1 || ordinal > len(targets) {
				return nil, reshapeError(
					result.CodeValidation,
					"SPLIT relation %q requires relation_target_ordinals between 1 and %d",
					current.ID, len(targets),
				)
			}
		}
		replacement := relation.Endpoint{
			DatabaseID: targets[ordinal-1].DatabaseID,
			TableID:    targets[ordinal-1].TableID,
			RowID:      targets[ordinal-1].ID,
		}
		updated := current
		updated.Revision++
		updated.CommitSequence = sequence
		updated.State = relation.StateDeleted
		updated.UpdatedAt = now
		resultRelations = append(resultRelations, updated)

		nextSource, nextTarget := current.Source, current.Target
		if sourceSet[nextSource.RowID] {
			nextSource = replacement
		}
		if sourceSet[nextTarget.RowID] {
			nextTarget = replacement
		}
		if nextSource == nextTarget {
			continue
		}
		signature := fmt.Sprintf("%s\x00%s\x00%s\x00%s", nextSource.RowID, current.Type, nextTarget.RowID, current.Description)
		if signatures[signature] {
			continue
		}
		signatures[signature] = true
		resultRelations = append(resultRelations, relation.Relation{
			Version: relation.Version, ID: "rel_" + uuid.NewString(),
			Source: nextSource, Type: current.Type, Target: nextTarget,
			Description: current.Description, Revision: 1, CommitSequence: sequence,
			State: relation.StateLive, CreatedAt: now, UpdatedAt: now,
		})
	}
	return resultRelations, nil
}

func reshapeError(code result.Code, format string, arguments ...any) error {
	return &ServiceError{Code: code, Message: fmt.Sprintf(format, arguments...)}
}
