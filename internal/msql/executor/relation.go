package executor

import (
	"context"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) relate(
	ctx context.Context,
	statement *ast.RelateStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRelationshipMutationOptions(options, false); err != nil {
		return Output{}, err
	}
	sourceDatabase, sourceTableName, sourceTable, err := engine.bindTable(ctx, statement.SourceTable)
	if err != nil {
		return Output{}, err
	}
	targetDatabase, targetTableName, targetTable, err := engine.bindTable(ctx, statement.TargetTable)
	if err != nil {
		return Output{}, err
	}
	sourceRow, err := relationshipString(statement.SourceRow, sourceTable, bound, "source Row ID")
	if err != nil {
		return Output{}, err
	}
	targetRow, err := relationshipString(statement.TargetRow, targetTable, bound, "target Row ID")
	if err != nil {
		return Output{}, err
	}
	relationType, err := relationshipString(statement.Type, sourceTable, bound, "relation type")
	if err != nil {
		return Output{}, err
	}
	description := ""
	if statement.Description != nil {
		description, err = relationshipString(statement.Description, sourceTable, bound, "relation description")
		if err != nil {
			return Output{}, err
		}
	}
	created, err := engine.rows.Relate(ctx, row.RelationDefinition{
		Source: row.RelationEndpoint{
			Database: sourceDatabase, Table: sourceTableName, RowID: sourceRow,
		},
		Type: relationType,
		Target: row.RelationEndpoint{
			Database: targetDatabase, Table: targetTableName, RowID: targetRow,
		},
		Description: description,
	})
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return relationshipMutationOutput(created), nil
}

func (engine *Engine) unrelate(
	ctx context.Context,
	statement *ast.UnrelateStatement,
	bound bindings,
	options MutationOptions,
) (Output, error) {
	if err := validateRelationshipMutationOptions(options, true); err != nil {
		return Output{}, err
	}
	relationID, err := relationshipString(
		statement.Relation, catalog.Table{}, bound, "relation ID",
	)
	if err != nil {
		return Output{}, err
	}
	if !strings.HasPrefix(relationID, "rel_") || strings.TrimPrefix(relationID, "rel_") == "" {
		return Output{}, executeError(result.CodeValidation, "relation ID must use the rel_ prefix")
	}
	if _, present := security.AuthorizationFrom(ctx); present {
		existing, err := engine.rows.GetRelation(ctx, relationID)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		if err := engine.authorizeRelation(ctx, existing); err != nil {
			return Output{}, err
		}
	}
	deleted, err := engine.rows.DeleteRelation(ctx, relationID, options.ExpectedRevision)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return relationshipMutationOutput(deleted), nil
}

func (engine *Engine) showRelations(
	ctx context.Context,
	statement ast.Statement,
	bound bindings,
) (Output, error) {
	show := statement.Show
	if show == nil || show.Table == nil || show.Row == nil || show.Limit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW RELATIONS is incomplete")
	}
	databaseName, tableNameValue, table, err := engine.bindTable(ctx, *show.Table)
	if err != nil {
		return Output{}, err
	}
	rowID, err := relationshipString(show.Row, table, bound, "Row ID")
	if err != nil {
		return Output{}, err
	}
	limit, err := historyPositiveInteger(show.Limit, table, bound, "SHOW RELATIONS LIMIT")
	if err != nil {
		return Output{}, err
	}
	if limit > maxQueryScan {
		return Output{}, executeError(
			result.CodeValidation,
			fmt.Sprintf("SHOW RELATIONS LIMIT must be between 1 and %d", maxQueryScan),
		)
	}
	endpoint := row.RelationEndpoint{
		Database: databaseName, Table: tableNameValue, RowID: rowID,
	}
	var relations []relation.Relation
	switch show.Direction {
	case "OUTGOING":
		relations, err = engine.rows.ListOutgoingRelations(ctx, endpoint)
	case "INCOMING":
		relations, err = engine.rows.ListIncomingRelations(ctx, endpoint)
	default:
		return Output{}, executeError(result.CodeValidation, "SHOW RELATIONS direction is invalid")
	}
	if err != nil {
		return Output{}, normalizeError(err)
	}
	for _, value := range relations {
		if err := engine.authorizeRelation(ctx, value); err != nil {
			return Output{}, err
		}
	}
	truncated := uint64(len(relations)) > limit
	if truncated {
		relations = relations[:limit]
	}
	output := Output{
		Columns: []result.Column{
			{Name: "relation_id", Type: "ID"},
			{Name: "relation_type", Type: "TEXT"},
			{Name: "source_database_id", Type: "ID"},
			{Name: "source_table_id", Type: "ID"},
			{Name: "source_row_id", Type: "ID"},
			{Name: "target_database_id", Type: "ID"},
			{Name: "target_table_id", Type: "ID"},
			{Name: "target_row_id", Type: "ID"},
			{Name: "description", Type: "TEXT"},
			{Name: "revision", Type: "INTEGER"},
			{Name: "commit_sequence", Type: "INTEGER"},
			{Name: "relation_state", Type: "TEXT"},
		},
		Rows: make([]result.Row, 0, len(relations)), Truncated: truncated,
	}
	for _, value := range relations {
		output.Rows = append(output.Rows, result.Row{
			"relation_id": value.ID, "relation_type": value.Type,
			"source_database_id": value.Source.DatabaseID,
			"source_table_id":    value.Source.TableID, "source_row_id": value.Source.RowID,
			"target_database_id": value.Target.DatabaseID,
			"target_table_id":    value.Target.TableID, "target_row_id": value.Target.RowID,
			"description": value.Description, "revision": value.Revision,
			"commit_sequence": value.CommitSequence, "relation_state": string(value.State),
		})
	}
	return output, nil
}

func (engine *Engine) authorizeRelation(ctx context.Context, value relation.Relation) error {
	if err := engine.authorizeDatabaseReference(ctx, value.Source.DatabaseID); err != nil {
		return err
	}
	return engine.authorizeDatabaseReference(ctx, value.Target.DatabaseID)
}

func relationshipString(
	expression *ast.Expression,
	table catalog.Table,
	bound bindings,
	label string,
) (string, error) {
	if expression == nil || containsIdentifier(expression) {
		return "", executeError(result.CodeValidation, label+" must be a literal or parameter")
	}
	value, err := evaluate(expression, table, nil, bound)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok {
		return "", executeError(result.CodeValidation, label+" must be TEXT")
	}
	return text, nil
}

func validateRelationshipMutationOptions(options MutationOptions, revision bool) error {
	if options.MaxAffectedRows < 1 || options.MaxAffectedRows > maxQueryScan {
		return executeError(
			result.CodeValidation,
			fmt.Sprintf("max_affected_rows must be between 1 and %d", maxQueryScan),
		)
	}
	if revision && options.ExpectedRevision == 0 {
		return executeError(result.CodeValidation, "UNRELATE requires expected revision")
	}
	return nil
}

func relationshipMutationOutput(changed relation.Relation) Output {
	revision := changed.Revision
	commitSequence := changed.CommitSequence
	return Output{
		Columns:      []result.Column{{Name: "relation_id", Type: "TEXT", Nullable: false}},
		Rows:         []result.Row{{"relation_id": changed.ID}},
		AffectedRows: 1, Revision: &revision, CommitSequence: &commitSequence,
	}
}
