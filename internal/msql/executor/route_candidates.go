package executor

import (
	"context"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routelexical"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/security"
)

const (
	maxRouteCandidates = 64
	minCandidateBytes  = 256
	maxCandidateBytes  = 65536
)

type routeCandidateCatalog interface {
	ShowDatabases(context.Context) ([]catalog.Database, error)
	ShowTables(context.Context, string) ([]catalog.Table, error)
}

type routeCandidateRows interface {
	ListRouterNodes(context.Context) ([]router.Node, error)
}

// archivedCatalogReader lets the scope check tell an archived Table apart from
// a Table that does not exist. Without it, a Route node under an archived
// Table looks identical to a dangling Route node.
type archivedCatalogReader interface {
	ShowArchivedTables(context.Context, string) ([]catalog.Table, error)
}

func (engine *Engine) showRouteCandidates(
	ctx context.Context,
	show *ast.ShowStatement,
	bound bindings,
) (Output, error) {
	if show == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTE CANDIDATES is incomplete")
	}
	switch show.Predictor {
	case "LEXICAL":
		return engine.showLexicalRouteCandidates(ctx, show, bound)
	case "VECTOR":
		// Vector retrieval was removed: it had no publisher, so the generation
		// it searched could never hold anything, and holding every Route vector
		// resident was an unbounded structure serving an unreachable feature.
		// The statement keeps parsing and keeps saying so.
		return Output{}, executeError(result.CodeUnsupported,
			"vector Route candidates are unavailable: vector retrieval is not implemented")
	default:
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTE CANDIDATES predictor is unsupported")
	}
}

func (engine *Engine) showLexicalRouteCandidates(
	ctx context.Context,
	show *ast.ShowStatement,
	bound bindings,
) (Output, error) {
	if show == nil || show.Predictor != "LEXICAL" || show.Query == nil ||
		show.Limit == nil || show.ByteLimit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTE CANDIDATES is incomplete")
	}
	query, err := routerString(show.Query, bound, "lexical Route query")
	if err != nil {
		return Output{}, err
	}
	candidateLimit, err := candidateInteger(show.Limit, bound, "SHOW ROUTE CANDIDATES LIMIT", 1, maxRouteCandidates)
	if err != nil {
		return Output{}, err
	}
	byteLimit, err := candidateInteger(show.ByteLimit, bound, "SHOW ROUTE CANDIDATES BYTES", minCandidateBytes, maxCandidateBytes)
	if err != nil {
		return Output{}, err
	}
	source, err := engine.lexicalRouteSource(ctx)
	if err != nil {
		return Output{}, err
	}
	matched, err := routelexical.Search(source, query)
	if err != nil {
		switch {
		case errors.Is(err, routelexical.ErrInvalidQuery):
			return Output{}, executeError(result.CodeValidation, err.Error())
		default:
			return Output{}, executeError(result.CodeInternal, "Route candidate source is invalid")
		}
	}
	builder, err := discovery.NewBuilder(matched.Snapshot, matched.CatalogRevision, uint64(candidateLimit), uint64(byteLimit))
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Discovery Frame could not be initialized")
	}
	// Ranking still happens inside routelexical — something has to decide which
	// hits survive the limit. It just does not leave the engine.
	candidates := make([]discovery.Candidate, 0, len(matched.Matches))
	for _, match := range matched.Matches {
		candidates = append(candidates, discovery.Candidate{
			DatabaseID: match.DatabaseID, TableID: match.TableID, Path: match.Path,
		})
	}
	if err := builder.Add(discovery.Batch{
		Snapshot: matched.Snapshot, CatalogRevision: matched.CatalogRevision,
		Candidates: candidates,
	}); err != nil {
		return Output{}, executeError(result.CodeInternal, "Discovery Frame could not be assembled")
	}
	frame := builder.Frame()
	return Output{
		Columns: []result.Column{}, Rows: []result.Row{}, Truncated: frame.Truncated, Discovery: &frame,
	}, nil
}

func (engine *Engine) lexicalRouteSource(ctx context.Context) (routelexical.Source, error) {
	if engine == nil || engine.candidateCatalog == nil || engine.rows == nil {
		return routelexical.Source{}, executeError(result.CodeUnsupported, "Route candidate discovery is not supported by this backend")
	}
	routeRows, ok := engine.rows.(routeCandidateRows)
	if !ok {
		return routelexical.Source{}, executeError(result.CodeUnsupported, "Route candidate discovery is not supported by this backend")
	}
	authorization, restricted := security.AuthorizationFrom(ctx)
	if restricted {
		if err := authorization.Validate(); err != nil {
			return routelexical.Source{}, normalizeError(err)
		}
	}
	databases, err := engine.candidateCatalog.ShowDatabases(ctx)
	if err != nil {
		return routelexical.Source{}, normalizeError(err)
	}
	visible := make([]catalog.Database, 0, len(databases))
	visibleDatabaseIDs := make(map[string]struct{})
	visibleTableIDs := make(map[string]struct{})
	archivedTableIDs := make(map[string]struct{})
	for _, database := range databases {
		if restricted && !security.AllowsAnyDatabase(
			authorization, append([]string{database.ID, database.Name}, database.Aliases...)...,
		) {
			continue
		}
		tables, err := engine.candidateCatalog.ShowTables(ctx, database.ID)
		if err != nil {
			return routelexical.Source{}, normalizeError(err)
		}
		database.Tables = append([]catalog.Table(nil), tables...)
		visible = append(visible, database)
		visibleDatabaseIDs[database.ID] = struct{}{}
		for _, table := range tables {
			visibleTableIDs[table.ID] = struct{}{}
		}
		if reader, ok := engine.candidateCatalog.(archivedCatalogReader); ok {
			archived, err := reader.ShowArchivedTables(ctx, database.ID)
			if err != nil {
				return routelexical.Source{}, normalizeError(err)
			}
			for _, table := range archived {
				archivedTableIDs[table.ID] = struct{}{}
			}
		}
	}
	nodes, err := routeRows.ListRouterNodes(ctx)
	if err != nil {
		return routelexical.Source{}, normalizeError(err)
	}
	visibleNodes := make([]router.Node, 0, len(nodes))
	for _, node := range nodes {
		if _, ok := visibleDatabaseIDs[node.DatabaseID]; !ok {
			continue
		}
		if node.TableID == "" {
			continue
		}
		if _, ok := visibleTableIDs[node.TableID]; !ok {
			// An archived Table is deliberately out of scope; anything else is
			// a Route pointing at a Table that does not exist, which is corrupt.
			if _, archived := archivedTableIDs[node.TableID]; archived {
				continue
			}
			return routelexical.Source{}, executeError(result.CodeInternal, "Route candidate source violates its visible Table scope")
		}
		visibleNodes = append(visibleNodes, node)
	}
	return routelexical.Source{Databases: visible, Routes: visibleNodes}, nil
}

func candidateInteger(expression *ast.Expression, bound bindings, label string, minimum, maximum int) (int, error) {
	value, err := historyPositiveInteger(expression, catalog.Table{}, bound, label)
	if err != nil {
		return 0, err
	}
	if value < uint64(minimum) || value > uint64(maximum) {
		return 0, executeError(result.CodeValidation, fmt.Sprintf("%s must be between %d and %d", label, minimum, maximum))
	}
	return int(value), nil
}
