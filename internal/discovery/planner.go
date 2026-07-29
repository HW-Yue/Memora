package discovery

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/search"
)

const (
	maxCandidates         = 1000
	maxLeafCandidates     = 100
	defaultRouteNodes     = 32
	defaultRelationSeeds  = 8
	defaultRelationFanout = 12
)

type Source interface {
	Root(context.Context, string) (router.Node, error)
	Children(context.Context, string) ([]router.Node, error)
	Leaf(context.Context, string, int) ([]router.Locator, bool, error)
	Match(context.Context, string, string, []string, int) (search.Result, error)
	Relations(context.Context, search.Locator, int) ([]search.Locator, bool, error)
}

type Options struct {
	RouteWeight         float64
	SearchWeight        float64
	RelationWeight      float64
	MaxRouteNodes       int
	MaxRelationSeeds    int
	MaxRelationsPerSeed int
}

type Request struct {
	DatabaseID string
	RoutePaths []string
	Query      string
	QueryTerms []string
	Limit      int
}

type Candidate struct {
	search.Locator
	Score         float64 `json:"score"`
	RouteScore    float64 `json:"route_score"`
	SearchScore   float64 `json:"search_score"`
	RelationScore float64 `json:"relation_score"`
}

type Result struct {
	Candidates      []Candidate `json:"candidates"`
	Truncated       bool        `json:"truncated"`
	BudgetExhausted bool        `json:"budget_exhausted"`
	RouteNodes      int         `json:"route_nodes"`
}

type Planner struct {
	source              Source
	routeWeight         float64
	searchWeight        float64
	relationWeight      float64
	maxRouteNodes       int
	maxRelationSeeds    int
	maxRelationsPerSeed int
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(source Source, options Options) (*Planner, error) {
	if source == nil {
		return nil, discoveryError(result.CodeInternal, "discovery source is not configured")
	}
	if options.RouteWeight == 0 && options.SearchWeight == 0 && options.RelationWeight == 0 {
		options.RouteWeight, options.SearchWeight, options.RelationWeight = 0.6, 1, 0.3
	}
	for _, weight := range []float64{options.RouteWeight, options.SearchWeight, options.RelationWeight} {
		if math.IsNaN(weight) || math.IsInf(weight, 0) || weight < 0 {
			return nil, discoveryError(result.CodeValidation, "discovery weights must be finite and non-negative")
		}
	}
	if options.MaxRouteNodes == 0 {
		options.MaxRouteNodes = defaultRouteNodes
	}
	if options.MaxRelationSeeds == 0 {
		options.MaxRelationSeeds = defaultRelationSeeds
	}
	if options.MaxRelationsPerSeed == 0 {
		options.MaxRelationsPerSeed = defaultRelationFanout
	}
	if options.MaxRouteNodes < 1 || options.MaxRelationSeeds < 1 ||
		options.MaxRelationsPerSeed < 1 {
		return nil, discoveryError(result.CodeValidation, "discovery budgets must be positive")
	}
	return &Planner{
		source: source, routeWeight: options.RouteWeight,
		searchWeight: options.SearchWeight, relationWeight: options.RelationWeight,
		maxRouteNodes: options.MaxRouteNodes, maxRelationSeeds: options.MaxRelationSeeds,
		maxRelationsPerSeed: options.MaxRelationsPerSeed,
	}, nil
}

func (planner *Planner) Discover(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.DatabaseID) == "" {
		return Result{}, discoveryError(result.CodeValidation, "discovery requires database ID")
	}
	if request.Limit < 1 || request.Limit > maxCandidates {
		return Result{}, discoveryError(result.CodeValidation, "discovery LIMIT must be between 1 and 1000")
	}
	paths, err := normalizePaths(request.RoutePaths)
	if err != nil {
		return Result{}, err
	}
	if len(paths) == 0 && strings.TrimSpace(request.Query) == "" && len(request.QueryTerms) == 0 {
		return Result{}, discoveryError(result.CodeValidation, "discovery requires a Route path or query")
	}

	values := make(map[string]*Candidate)
	state := routeState{}
	if len(paths) > 0 {
		if err := planner.collectRoutes(ctx, request.DatabaseID, paths, values, &state); err != nil {
			return Result{}, err
		}
	}
	truncated := state.truncated
	if strings.TrimSpace(request.Query) != "" || len(request.QueryTerms) > 0 {
		matches, err := planner.source.Match(
			ctx, request.DatabaseID, request.Query, request.QueryTerms, maxCandidates,
		)
		if err != nil {
			return Result{}, stableError(err)
		}
		truncated = truncated || matches.Truncated
		for _, match := range matches.Matches {
			candidate, err := addCandidate(values, match.Locator, request.DatabaseID)
			if err != nil {
				return Result{}, err
			}
			candidate.SearchScore = max(candidate.SearchScore, match.Score)
		}
	}

	seeds := planner.sorted(values)
	seedLimit := min(len(seeds), planner.maxRelationSeeds)
	budgetExhausted := state.exhausted
	if len(seeds) > planner.maxRelationSeeds {
		budgetExhausted = true
		truncated = true
	}
	for _, seed := range seeds[:seedLimit] {
		related, relationTruncated, err := planner.source.Relations(
			ctx, seed.Locator, planner.maxRelationsPerSeed,
		)
		if err != nil {
			return Result{}, stableError(err)
		}
		truncated = truncated || relationTruncated
		for _, locator := range related {
			if sameLocatorRow(locator, seed.Locator) {
				continue
			}
			candidate, err := addCandidate(values, locator, request.DatabaseID)
			if err != nil {
				return Result{}, err
			}
			candidate.RelationScore = 1
		}
	}

	candidates := planner.sorted(values)
	if len(candidates) > request.Limit {
		candidates = candidates[:request.Limit]
		truncated = true
	}
	return Result{
		Candidates: candidates, Truncated: truncated,
		BudgetExhausted: budgetExhausted, RouteNodes: state.nodes,
	}, nil
}

type routeState struct {
	nodes     int
	exhausted bool
	truncated bool
}

func (planner *Planner) collectRoutes(
	ctx context.Context,
	databaseID string,
	paths []string,
	values map[string]*Candidate,
	state *routeState,
) error {
	root, err := planner.source.Root(ctx, databaseID)
	if err != nil {
		return stableError(err)
	}
	if root.DatabaseID != databaseID || root.Kind != router.KindRoot {
		return discoveryError(result.CodeInternal, "discovery source returned an invalid Router root")
	}
	state.nodes = 1
	for _, path := range paths {
		current := root
		for _, segment := range pathSegments(path) {
			children, err := planner.source.Children(ctx, current.ID)
			if err != nil {
				return stableError(err)
			}
			next, found := routeChild(children, segment, databaseID)
			if !found {
				current = router.Node{}
				break
			}
			if state.nodes >= planner.maxRouteNodes {
				state.exhausted, state.truncated = true, true
				return nil
			}
			state.nodes++
			current = next
		}
		if current.Kind != router.KindLeaf {
			continue
		}
		locators, leafTruncated, err := planner.source.Leaf(
			ctx, current.ID, maxLeafCandidates,
		)
		if err != nil {
			return stableError(err)
		}
		state.truncated = state.truncated || leafTruncated
		for _, locator := range locators {
			candidate, err := addCandidate(values, search.Locator{
				DatabaseID: locator.DatabaseID, TableID: locator.TableID,
				RowID: locator.RowID, Revision: locator.Revision,
			}, databaseID)
			if err != nil {
				return err
			}
			candidate.RouteScore = 1
		}
	}
	return nil
}

func (planner *Planner) sorted(values map[string]*Candidate) []Candidate {
	resultValues := make([]Candidate, 0, len(values))
	for _, candidate := range values {
		value := *candidate
		value.Score = rounded(
			planner.routeWeight*value.RouteScore +
				planner.searchWeight*value.SearchScore +
				planner.relationWeight*value.RelationScore,
		)
		resultValues = append(resultValues, value)
	}
	sort.Slice(resultValues, func(left, right int) bool {
		if resultValues[left].Score != resultValues[right].Score {
			return resultValues[left].Score > resultValues[right].Score
		}
		if resultValues[left].TableID != resultValues[right].TableID {
			return resultValues[left].TableID < resultValues[right].TableID
		}
		return resultValues[left].RowID < resultValues[right].RowID
	})
	return resultValues
}

func addCandidate(
	values map[string]*Candidate,
	locator search.Locator,
	databaseID string,
) (*Candidate, error) {
	if locator.DatabaseID != databaseID || locator.TableID == "" ||
		locator.RowID == "" || locator.Revision == 0 {
		return nil, discoveryError(result.CodeInternal, "discovery source returned an invalid locator")
	}
	key := locator.TableID + "\x00" + locator.RowID
	if existing, ok := values[key]; ok {
		if existing.Revision != locator.Revision {
			return nil, discoveryError(result.CodeInternal, "discovery sources returned inconsistent Row revisions")
		}
		return existing, nil
	}
	value := &Candidate{Locator: locator}
	values[key] = value
	return value, nil
}

func normalizePaths(paths []string) ([]string, error) {
	seen := make(map[string]struct{}, len(paths))
	values := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || !strings.HasPrefix(path, "/") {
			return nil, discoveryError(result.CodeValidation, "Route paths must be absolute")
		}
		path = "/" + strings.Join(pathSegments(path), "/")
		if _, exists := seen[path]; exists {
			continue
		}
		seen[path] = struct{}{}
		values = append(values, path)
	}
	return values, nil
}

func pathSegments(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	raw := strings.Split(trimmed, "/")
	values := make([]string, 0, len(raw))
	for _, value := range raw {
		if value = strings.TrimSpace(value); value != "" {
			values = append(values, strings.ToLower(value))
		}
	}
	return values
}

func routeChild(nodes []router.Node, segment, databaseID string) (router.Node, bool) {
	for _, node := range nodes {
		if node.DatabaseID != databaseID {
			continue
		}
		if strings.EqualFold(node.Name, segment) {
			return node, true
		}
		for _, alias := range node.Aliases {
			if strings.EqualFold(alias, segment) {
				return node, true
			}
		}
	}
	return router.Node{}, false
}

func sameLocatorRow(left, right search.Locator) bool {
	return left.DatabaseID == right.DatabaseID &&
		left.TableID == right.TableID && left.RowID == right.RowID
}

func rounded(value float64) float64 { return math.Round(value*1e12) / 1e12 }

func discoveryError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func stableError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return discoveryError(result.CodeCancelled, "discovery was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return discoveryError(result.CodeDeadlineExceeded, "discovery exceeded its deadline")
	default:
		return discoveryError(result.CodeInternal, "discovery source failed")
	}
}
