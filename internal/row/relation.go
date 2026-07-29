package row

import (
	"context"
	"fmt"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
)

type RelationEndpoint struct {
	Database string
	Table    string
	RowID    string
}

type RelationDefinition struct {
	Source      RelationEndpoint
	Type        string
	Target      RelationEndpoint
	Description string
}

func (transaction *Transaction) Relate(
	ctx context.Context,
	definition RelationDefinition,
) (relation.Relation, error) {
	sourceTable, source, err := transaction.resolveLiveEndpoint(ctx, definition.Source)
	if err != nil {
		return relation.Relation{}, err
	}
	targetTable, target, err := transaction.resolveLiveEndpoint(ctx, definition.Target)
	if err != nil {
		return relation.Relation{}, err
	}
	if !transaction.service.relationPolicy.AllowRelation(ctx, sourceTable, targetTable) {
		return relation.Relation{}, rowError(
			result.CodeConstraint,
			"relation policy denied the source and target endpoints",
		)
	}
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	created, err := transaction.service.relations.CreateIn(ctx, transaction.tx, relation.Definition{
		Source: source, Type: definition.Type, Target: target,
		Description: definition.Description, CommitSequence: commitSequence,
	})
	return created, stableError(err)
}

func (transaction *Transaction) GetRelation(
	ctx context.Context,
	relationID string,
) (relation.Relation, error) {
	value, err := transaction.service.relations.GetIn(ctx, transaction.tx, relationID)
	return value, stableError(err)
}

func (transaction *Transaction) DeleteRelation(
	ctx context.Context,
	relationID string,
	expectedRevision uint64,
) (relation.Relation, error) {
	commitSequence, err := transaction.ensureCommitSequence(ctx)
	if err != nil {
		return relation.Relation{}, err
	}
	value, err := transaction.service.relations.DeleteIn(
		ctx, transaction.tx, relationID, expectedRevision, commitSequence,
	)
	return value, stableError(err)
}

func (transaction *Transaction) ListOutgoingRelations(
	ctx context.Context,
	endpoint RelationEndpoint,
) ([]relation.Relation, error) {
	_, resolved, err := transaction.resolveEndpoint(ctx, endpoint, false)
	if err != nil {
		return nil, err
	}
	values, err := transaction.service.relations.ListOutgoingIn(ctx, transaction.tx, resolved)
	return values, stableError(err)
}

func (transaction *Transaction) ListIncomingRelations(
	ctx context.Context,
	endpoint RelationEndpoint,
) ([]relation.Relation, error) {
	_, resolved, err := transaction.resolveEndpoint(ctx, endpoint, false)
	if err != nil {
		return nil, err
	}
	values, err := transaction.service.relations.ListIncomingIn(ctx, transaction.tx, resolved)
	return values, stableError(err)
}

func (transaction *Transaction) resolveLiveEndpoint(
	ctx context.Context,
	endpoint RelationEndpoint,
) (catalog.Table, relation.Endpoint, error) {
	return transaction.resolveEndpoint(ctx, endpoint, true)
}

func (transaction *Transaction) resolveEndpoint(
	ctx context.Context,
	endpoint RelationEndpoint,
	requireLive bool,
) (catalog.Table, relation.Endpoint, error) {
	if strings.TrimSpace(endpoint.Database) == "" ||
		strings.TrimSpace(endpoint.Table) == "" ||
		strings.TrimSpace(endpoint.RowID) == "" {
		return catalog.Table{}, relation.Endpoint{}, rowError(
			result.CodeValidation,
			"relation endpoint requires database, table, and row ID",
		)
	}
	table, err := transaction.DescribeTable(ctx, endpoint.Database, endpoint.Table)
	if err != nil {
		return catalog.Table{}, relation.Endpoint{}, err
	}
	stored, err := getStored(ctx, transaction.tx, table.ID, endpoint.RowID)
	if err != nil {
		return catalog.Table{}, relation.Endpoint{}, stableError(err)
	}
	if requireLive && stored.State == StateDeleted {
		return catalog.Table{}, relation.Endpoint{}, rowError(
			result.CodeNotFound,
			fmt.Sprintf("row %q was not found", endpoint.RowID),
		)
	}
	return table, relation.Endpoint{
		DatabaseID: table.DatabaseID, TableID: table.ID, RowID: endpoint.RowID,
	}, nil
}

func (transaction *Transaction) invalidateRelations(
	ctx context.Context,
	endpoint relation.Endpoint,
	commitSequence uint64,
) error {
	outgoing, err := transaction.service.relations.ListOutgoingIn(ctx, transaction.tx, endpoint)
	if err != nil {
		return stableError(err)
	}
	incoming, err := transaction.service.relations.ListIncomingIn(ctx, transaction.tx, endpoint)
	if err != nil {
		return stableError(err)
	}
	seen := make(map[string]struct{}, len(outgoing)+len(incoming))
	for _, value := range append(outgoing, incoming...) {
		if _, exists := seen[value.ID]; exists {
			continue
		}
		seen[value.ID] = struct{}{}
		if _, err := transaction.service.relations.DeleteIn(
			ctx, transaction.tx, value.ID, value.Revision, commitSequence,
		); err != nil {
			return stableError(err)
		}
	}
	return nil
}
