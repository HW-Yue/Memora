package search

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

const maxCandidates = 1000

type Locator struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Revision   uint64 `json:"revision"`
}

type Source interface {
	LookupAgent(context.Context, string, string, int) ([]Locator, error)
	LookupMechanical(context.Context, string, string, int) ([]Locator, error)
}

type Options struct {
	AgentWeight      float64
	MechanicalWeight float64
}

type Request struct {
	DatabaseID      string
	AgentTerms      []string
	MechanicalTerms []string
	Limit           int
}

type Match struct {
	Locator
	Score           float64 `json:"score"`
	AgentScore      float64 `json:"agent_score"`
	MechanicalScore float64 `json:"mechanical_score"`
}

type Result struct {
	Matches   []Match `json:"matches"`
	Truncated bool    `json:"truncated"`
}

type Planner struct {
	source           Source
	agentWeight      float64
	mechanicalWeight float64
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(source Source, options Options) (*Planner, error) {
	if options.AgentWeight == 0 && options.MechanicalWeight == 0 {
		options.AgentWeight = 0.8
		options.MechanicalWeight = 0.2
	}
	if math.IsNaN(options.AgentWeight) || math.IsNaN(options.MechanicalWeight) ||
		math.IsInf(options.AgentWeight, 0) || math.IsInf(options.MechanicalWeight, 0) ||
		options.AgentWeight < 0 || options.MechanicalWeight < 0 ||
		math.Abs(options.AgentWeight+options.MechanicalWeight-1) > 1e-9 {
		return nil, searchError(result.CodeValidation, "search weights must be finite, non-negative, and sum to 1")
	}
	if source == nil {
		return nil, searchError(result.CodeInternal, "search source is not configured")
	}
	return &Planner{
		source: source, agentWeight: options.AgentWeight,
		mechanicalWeight: options.MechanicalWeight,
	}, nil
}

func (planner *Planner) Match(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.DatabaseID) == "" {
		return Result{}, searchError(result.CodeValidation, "MATCH requires database ID")
	}
	if request.Limit < 1 || request.Limit > maxCandidates {
		return Result{}, searchError(result.CodeValidation, "MATCH LIMIT must be between 1 and 1000")
	}
	agentTerms, err := normalizedTerms(request.AgentTerms)
	if err != nil {
		return Result{}, err
	}
	mechanicalTerms, err := normalizedTerms(request.MechanicalTerms)
	if err != nil {
		return Result{}, err
	}
	if len(agentTerms) == 0 && len(mechanicalTerms) == 0 {
		return Result{}, searchError(result.CodeValidation, "MATCH requires at least one query term")
	}
	scores := make(map[string]*candidate)
	if err := planner.collect(ctx, request.DatabaseID, agentTerms, true, scores); err != nil {
		return Result{}, err
	}
	if err := planner.collect(ctx, request.DatabaseID, mechanicalTerms, false, scores); err != nil {
		return Result{}, err
	}
	maxAgent, maxMechanical := 0, 0
	for _, value := range scores {
		maxAgent = max(maxAgent, value.agentHits)
		maxMechanical = max(maxMechanical, value.mechanicalHits)
	}
	matches := make([]Match, 0, len(scores))
	for _, value := range scores {
		match := Match{Locator: value.locator}
		if maxAgent > 0 {
			match.AgentScore = float64(value.agentHits) / float64(maxAgent)
		}
		if maxMechanical > 0 {
			match.MechanicalScore = float64(value.mechanicalHits) / float64(maxMechanical)
		}
		match.Score = rounded(
			planner.agentWeight*match.AgentScore +
				planner.mechanicalWeight*match.MechanicalScore,
		)
		matches = append(matches, match)
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].Score != matches[right].Score {
			return matches[left].Score > matches[right].Score
		}
		if matches[left].DatabaseID != matches[right].DatabaseID {
			return matches[left].DatabaseID < matches[right].DatabaseID
		}
		if matches[left].TableID != matches[right].TableID {
			return matches[left].TableID < matches[right].TableID
		}
		return matches[left].RowID < matches[right].RowID
	})
	truncated := len(matches) > request.Limit
	if truncated {
		matches = matches[:request.Limit]
	}
	return Result{Matches: matches, Truncated: truncated}, nil
}

type candidate struct {
	locator        Locator
	agentHits      int
	mechanicalHits int
}

func (planner *Planner) collect(
	ctx context.Context,
	databaseID string,
	terms []string,
	agent bool,
	scores map[string]*candidate,
) error {
	for _, term := range terms {
		var locators []Locator
		var err error
		if agent {
			locators, err = planner.source.LookupAgent(ctx, databaseID, term, maxCandidates)
		} else {
			locators, err = planner.source.LookupMechanical(ctx, databaseID, term, maxCandidates)
		}
		if err != nil {
			return stableError(err)
		}
		for _, locator := range locators {
			if locator.DatabaseID != databaseID || locator.TableID == "" ||
				locator.RowID == "" || locator.Revision == 0 {
				return searchError(result.CodeInternal, "search source returned an invalid locator")
			}
			key := locator.DatabaseID + "\x00" + locator.TableID + "\x00" + locator.RowID
			value, exists := scores[key]
			if !exists {
				value = &candidate{locator: locator}
				scores[key] = value
			} else if value.locator.Revision != locator.Revision {
				return searchError(result.CodeInternal, "search channels returned inconsistent Row revisions")
			}
			if agent {
				value.agentHits++
			} else {
				value.mechanicalHits++
			}
		}
	}
	return nil
}

func normalizedTerms(terms []string) ([]string, error) {
	seen := make(map[string]struct{}, len(terms))
	values := make([]string, 0, len(terms))
	for _, term := range terms {
		normalized := strings.ToLower(strings.Join(strings.Fields(term), " "))
		if normalized == "" {
			return nil, searchError(result.CodeValidation, "MATCH term is empty")
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		values = append(values, normalized)
	}
	return values, nil
}

func rounded(value float64) float64 { return math.Round(value*1e12) / 1e12 }

func searchError(code result.Code, message string) error {
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
		return searchError(result.CodeCancelled, "MATCH was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return searchError(result.CodeDeadlineExceeded, "MATCH exceeded its deadline")
	default:
		return searchError(result.CodeInternal, "MATCH source failed")
	}
}
