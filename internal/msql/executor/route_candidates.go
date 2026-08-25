package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routeexact"
	"github.com/HW-Yue/Memora/internal/routelexical"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/routevector"
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
		return engine.showVectorRouteCandidates(ctx, show, bound)
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

func (engine *Engine) showVectorRouteCandidates(
	ctx context.Context,
	show *ast.ShowStatement,
	bound bindings,
) (Output, error) {
	if show == nil || show.Predictor != "VECTOR" || show.Query == nil || show.Space == nil ||
		show.Limit == nil || show.ByteLimit == nil {
		return Output{}, executeError(result.CodeValidation, "SHOW ROUTE CANDIDATES is incomplete")
	}
	vector, err := routeVectorParameter(show.Query, bound)
	if err != nil {
		return Output{}, err
	}
	spaceDigest, err := routerString(show.Space, bound, "Route embedding space digest")
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
	query := routeexact.Query{SpaceDigest: spaceDigest, Vector: vector, Limit: candidateLimit}
	if _, err := routeexact.Search(query, nil); err != nil {
		return Output{}, executeError(result.CodeValidation, err.Error())
	}
	source, err := engine.lexicalRouteSource(ctx)
	if err != nil {
		return Output{}, err
	}
	base, err := routelexical.Snapshot(source)
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Route candidate source is invalid")
	}

	scopes := make([]routeexact.Scope, 0, len(source.Databases))
	receipts := make([]routeexact.GenerationReceipt, 0, len(source.Databases))
	for _, database := range source.Databases {
		if engine.routeVectors == nil {
			continue
		}
		generation, marker, openErr := engine.routeVectors.OpenActive(ctx, database.ID)
		if openErr != nil {
			if ctx.Err() != nil {
				return Output{}, ctx.Err()
			}
			// Derived predictor failures are represented in the receipt and
			// never turn the authoritative Router read into a query failure.
			continue
		}
		manifest := generation.Manifest()
		if manifest.SpaceDigest != spaceDigest {
			continue
		}
		allowedTables := make(map[string]struct{}, len(database.Tables))
		for _, table := range database.Tables {
			allowedTables[table.ID] = struct{}{}
		}
		scopes = append(scopes, routeexact.Scope{
			DatabaseID: database.ID, Generation: generation, AllowedTableIDs: allowedTables,
		})
		receipts = append(receipts, routeexact.GenerationReceipt{
			DatabaseID: database.ID, GenerationID: marker.GenerationID,
			MarkerRevision: marker.Revision, ManifestSHA256: marker.ManifestSHA256,
		})
	}
	matched, err := routeexact.Search(query, scopes)
	if err != nil {
		if errors.Is(err, routeexact.ErrInvalidQuery) {
			return Output{}, executeError(result.CodeValidation, err.Error())
		}
		return Output{}, executeError(result.CodeInternal, "Route vector scope is invalid")
	}
	snapshot, err := routeexact.Snapshot(base.Snapshot, spaceDigest, receipts)
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Route vector snapshot could not be assembled")
	}
	// A frame carries no predictor receipt any more, so "the predictor could
	// not run" has nowhere to be reported inside a successful answer. Returning
	// an empty candidate list would assert something false — that the tree was
	// searched and held nothing — so an unavailable predictor is an error.
	if len(scopes) == 0 {
		return Output{}, executeError(
			result.CodeNotFound,
			"no current authorized generation matches the requested Route embedding space",
		)
	}
	builder, err := discovery.NewBuilder(snapshot, base.CatalogRevision, uint64(candidateLimit), uint64(byteLimit))
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Discovery Frame could not be initialized")
	}
	// The Router is the one place a path is spelled, so the path comes from the
	// Route node rather than from anything the vector generation stored.
	paths := make(map[string]string, len(source.Routes))
	for _, node := range source.Routes {
		if !node.Deleted {
			paths[node.ID] = node.Path
		}
	}
	candidates := make([]discovery.Candidate, 0, len(matched.Matches))
	for _, match := range matched.Matches {
		path, live := paths[match.RouteID]
		if !live {
			// The generation is derived and may lag the Router. A candidate
			// the Router no longer has is not a location.
			continue
		}
		candidates = append(candidates, discovery.Candidate{
			DatabaseID: match.DatabaseID, TableID: match.TableID, Path: path,
		})
	}
	if err := builder.Add(discovery.Batch{
		Snapshot: snapshot, CatalogRevision: base.CatalogRevision, Candidates: candidates,
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

func routeVectorParameter(expression *ast.Expression, bound bindings) ([]float32, error) {
	value, err := evaluate(expression, catalog.Table{}, nil, bound)
	if err != nil {
		return nil, err
	}
	switch typed := value.(type) {
	case []float32:
		return append([]float32(nil), typed...), nil
	case []float64:
		values := make([]float32, len(typed))
		for index, component := range typed {
			values[index] = float32(component)
		}
		return values, nil
	case []any:
		values := make([]float32, len(typed))
		for index, component := range typed {
			number, ok := routeVectorNumber(component)
			if !ok {
				return nil, executeError(result.CodeValidation, "Route query vector must contain only numeric components")
			}
			values[index] = float32(number)
		}
		return values, nil
	default:
		return nil, executeError(result.CodeValidation, "Route query vector must be a numeric array parameter")
	}
}

func routeVectorNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint32:
		return float64(typed), true
	case json.Number:
		value, err := typed.Float64()
		return value, err == nil
	default:
		return 0, false
	}
}

var _ RouteVectorReader = (*routevector.Service)(nil)
