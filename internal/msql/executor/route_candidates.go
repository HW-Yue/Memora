package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	lexicalPredictor   = "lexical-route/v1"
	vectorPredictor    = "vector-route-exact/v1"
)

type routeCandidateCatalog interface {
	ShowDatabases(context.Context) ([]catalog.Database, error)
	ShowTables(context.Context, string) ([]catalog.Table, error)
}

type routeCandidateRows interface {
	ListRouterNodes(context.Context) ([]router.Node, error)
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
	builder, err := discovery.NewBuilder(matched.Snapshot, matched.CatalogRevision, discovery.Budget{
		CandidateLimit: uint64(candidateLimit), UTF8ByteLimit: uint64(byteLimit),
	})
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Discovery Frame could not be initialized")
	}
	candidates := make([]discovery.CandidateInput, 0, len(matched.Matches))
	for _, match := range matched.Matches {
		score := float64(match.MatchCount)
		candidates = append(candidates, discovery.CandidateInput{
			DatabaseID: match.DatabaseID, TableID: match.TableID, RouteID: match.RouteID,
			RouteRevision: match.RouteRevision, Score: &score,
			Reason:        lexicalReason(match.MatchCount, match.MatchedFields),
			MatchedFields: append([]string(nil), match.MatchedFields...),
		})
	}
	if err := builder.Add(discovery.Batch{
		Snapshot: matched.Snapshot, CatalogRevision: matched.CatalogRevision,
		Predictor: lexicalPredictor, Status: discovery.PredictorSucceeded,
		ScoreKind: discovery.ScoreMatchCount, Reason: "current authorized semantic metadata locations",
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
	builder, err := discovery.NewBuilder(snapshot, base.CatalogRevision, discovery.Budget{
		CandidateLimit: uint64(candidateLimit), UTF8ByteLimit: uint64(byteLimit),
	})
	if err != nil {
		return Output{}, executeError(result.CodeInternal, "Discovery Frame could not be initialized")
	}
	if len(scopes) == 0 {
		err = builder.Add(discovery.Batch{
			Snapshot: snapshot, CatalogRevision: base.CatalogRevision,
			Predictor: vectorPredictor, Status: discovery.PredictorUnavailable,
			ScoreKind: discovery.ScoreNone,
			Reason:    "no current authorized generation matches the requested embedding space",
		})
	} else {
		candidates := make([]discovery.CandidateInput, 0, len(matched.Matches))
		for _, match := range matched.Matches {
			score := match.Score
			candidates = append(candidates, discovery.CandidateInput{
				DatabaseID: match.DatabaseID, TableID: match.TableID, RouteID: match.RouteID,
				RouteRevision: match.RouteRevision, Score: &score,
				Reason: "exact dot product in the requested Route embedding space",
			})
		}
		err = builder.Add(discovery.Batch{
			Snapshot: snapshot, CatalogRevision: base.CatalogRevision,
			Predictor: vectorPredictor, Status: discovery.PredictorSucceeded,
			ScoreKind: discovery.ScoreDotProduct,
			Reason: fmt.Sprintf(
				"%d of %d authorized Database generations compatible; %d Route vectors scanned",
				len(scopes), len(source.Databases), matched.Scanned,
			),
			Candidates: candidates,
		})
	}
	if err != nil {
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

func lexicalReason(count uint64, fields []string) string {
	return fmt.Sprintf("%d lexical field-term hits in %s", count, strings.Join(fields, ","))
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
