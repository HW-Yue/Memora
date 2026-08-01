package discovery

import (
	"errors"
	"fmt"
)

var (
	ErrSnapshotMismatch        = errors.New("discovery snapshot mismatch")
	ErrCatalogRevisionMismatch = errors.New("discovery catalog revision mismatch")
)

type CandidateInput struct {
	DatabaseID    string
	TableID       string
	RouteID       string
	RouteRevision uint64
	Reason        string
	Score         *float64
}

type Batch struct {
	Snapshot        string
	CatalogRevision string
	Predictor       string
	Status          PredictorStatus
	ScoreKind       ScoreKind
	Reason          string
	Candidates      []CandidateInput
}

type Builder struct {
	frame     Frame
	exhausted bool
	locations map[string]struct{}
}

func NewBuilder(snapshot, catalogRevision string, budget Budget) (*Builder, error) {
	budget.CandidatesUsed = 0
	budget.UTF8BytesUsed = 0
	frame := Frame{
		Version: Version, Usage: UsageNavigationOnly, Snapshot: snapshot,
		CatalogRevision: catalogRevision, Budget: budget,
		Predictors: []PredictorReceipt{}, Candidates: []Candidate{},
	}
	if !validOpaque(snapshot) || !validOpaque(catalogRevision) {
		return nil, invalid("snapshot and catalog_revision are required")
	}
	if err := validateBudget(budget); err != nil {
		return nil, err
	}
	return &Builder{frame: frame, locations: make(map[string]struct{})}, nil
}

func (builder *Builder) Add(batch Batch) error {
	if builder == nil {
		return invalid("builder is nil")
	}
	if batch.Snapshot != builder.frame.Snapshot {
		return fmt.Errorf("%w: got %q", ErrSnapshotMismatch, batch.Snapshot)
	}
	if batch.CatalogRevision != builder.frame.CatalogRevision {
		return fmt.Errorf("%w: got %q", ErrCatalogRevisionMismatch, batch.CatalogRevision)
	}
	receipt := PredictorReceipt{
		Predictor: batch.Predictor, Status: batch.Status, ScoreKind: batch.ScoreKind, Reason: batch.Reason,
	}
	if err := validateReceipt(receipt); err != nil {
		return err
	}
	for _, existing := range builder.frame.Predictors {
		if existing.Predictor == receipt.Predictor {
			return invalid("predictor %q already has a receipt", receipt.Predictor)
		}
	}
	if batch.Status == PredictorUnavailable {
		if len(batch.Candidates) != 0 {
			return invalid("unavailable predictor cannot return candidates")
		}
		builder.frame.Predictors = append(builder.frame.Predictors, receipt)
		return nil
	}

	candidates := make([]Candidate, len(batch.Candidates))
	batchLocations := make(map[string]struct{}, len(batch.Candidates))
	for index, input := range batch.Candidates {
		candidates[index] = Candidate{
			DatabaseID: input.DatabaseID, TableID: input.TableID, RouteID: input.RouteID,
			RouteRevision: input.RouteRevision, Predictor: batch.Predictor, Reason: input.Reason,
			ScoreKind: batch.ScoreKind, Score: cloneScore(input.Score),
		}
		if err := validateCandidate(candidates[index]); err != nil {
			return err
		}
		key := locationKey(candidates[index])
		if _, exists := builder.locations[key]; exists {
			return invalid("predictor %q repeats a candidate location", batch.Predictor)
		}
		if _, exists := batchLocations[key]; exists {
			return invalid("predictor %q repeats a candidate location", batch.Predictor)
		}
		batchLocations[key] = struct{}{}
	}

	for _, candidate := range candidates {
		if builder.exhausted {
			receipt.Truncated = true
			continue
		}
		size, err := candidateSize(candidate)
		if err != nil {
			return err
		}
		budget := builder.frame.Budget
		if budget.CandidatesUsed+1 > budget.CandidateLimit || budget.UTF8BytesUsed+size > budget.UTF8ByteLimit {
			builder.exhausted = true
			builder.frame.Truncated = true
			receipt.Truncated = true
			continue
		}
		builder.frame.Candidates = append(builder.frame.Candidates, candidate)
		builder.locations[locationKey(candidate)] = struct{}{}
		builder.frame.Budget.CandidatesUsed++
		builder.frame.Budget.UTF8BytesUsed += size
		receipt.CandidateCount++
	}
	builder.frame.Predictors = append(builder.frame.Predictors, receipt)
	return nil
}

func (builder *Builder) Frame() Frame {
	if builder == nil {
		return Frame{}
	}
	frame := builder.frame
	frame.Predictors = append([]PredictorReceipt(nil), builder.frame.Predictors...)
	frame.Candidates = make([]Candidate, len(builder.frame.Candidates))
	for index, candidate := range builder.frame.Candidates {
		frame.Candidates[index] = candidate
		frame.Candidates[index].Score = cloneScore(candidate.Score)
	}
	return frame
}

func locationKey(candidate Candidate) string {
	return candidate.Predictor + "\x00" + candidate.DatabaseID + "\x00" + candidate.TableID + "\x00" + candidate.RouteID
}

func cloneScore(value *float64) *float64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
