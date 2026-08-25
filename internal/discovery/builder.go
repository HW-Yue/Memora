package discovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

var (
	ErrSnapshotMismatch        = errors.New("discovery snapshot mismatch")
	ErrCatalogRevisionMismatch = errors.New("discovery catalog revision mismatch")
)

// Batch is one predictor's contribution. The predictor is not named in the
// output: a caller that knows which predictor answered will start choosing
// between them, and the Router is the only authority.
type Batch struct {
	Snapshot        string
	CatalogRevision string
	Candidates      []Candidate
}

type Builder struct {
	frame     Frame
	byteLimit uint64
	usedBytes uint64
	locations map[string]struct{}
}

// NewBuilder starts a frame bounded by a candidate count and an encoded byte
// size.
//
// Both bounds are enforced, but only the candidate limit is published: how many
// bytes a particular answer happened to use is the sort of usage report the
// frame no longer makes. The byte bound still has to be honoured because the
// statement lets the caller ask for it.
func NewBuilder(snapshot, catalogRevision string, limit, byteLimit uint64) (*Builder, error) {
	if !validOpaque(snapshot) || !validOpaque(catalogRevision) {
		return nil, invalid("snapshot and catalog_revision are required")
	}
	if limit == 0 || limit > maxCandidateLimit {
		return nil, invalid("limit is outside protocol bounds")
	}
	if byteLimit == 0 {
		return nil, invalid("byte limit is outside protocol bounds")
	}
	return &Builder{
		frame: Frame{
			Version: Version, Usage: UsageNavigationOnly, Snapshot: snapshot,
			CatalogRevision: catalogRevision, Limit: limit, Candidates: []Candidate{},
		},
		byteLimit: byteLimit,
		locations: make(map[string]struct{}),
	}, nil
}

// Add takes a predictor's candidates, in the predictor's own ranked order.
//
// The bounds are applied HERE, on arrival, precisely because arrival order is
// rank order: ranking still decides which hits are worth keeping. Sorting first
// and cutting afterwards would let a path that happens to start with "/a"
// displace a far better hit, which is ranking thrown away rather than hidden.
//
// A location repeated across batches is dropped rather than duplicated: two
// predictors finding the same node found one node.
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
	for _, candidate := range batch.Candidates {
		if err := validateCandidate(candidate); err != nil {
			return err
		}
		key := locationKey(candidate)
		if _, exists := builder.locations[key]; exists {
			continue
		}
		size, err := candidateSize(candidate)
		if err != nil {
			return invalid("encode candidate: %v", err)
		}
		if uint64(len(builder.frame.Candidates))+1 > builder.frame.Limit ||
			builder.usedBytes+size > builder.byteLimit {
			builder.frame.Truncated = true
			continue
		}
		builder.usedBytes += size
		builder.locations[key] = struct{}{}
		builder.frame.Candidates = append(builder.frame.Candidates, candidate)
	}
	return nil
}

// Frame returns the finished frame with the kept candidates in the published
// order.
//
// Order is by location, never by rank: a published rank order is an exposed
// score wearing a different hat. Which candidates are here was decided by rank;
// how they are listed is not.
func (builder *Builder) Frame() Frame {
	if builder == nil {
		return Frame{}
	}
	frame := builder.frame
	candidates := make([]Candidate, len(builder.frame.Candidates))
	copy(candidates, builder.frame.Candidates)
	sort.Slice(candidates, func(left, right int) bool {
		return candidateLess(candidates[left], candidates[right])
	})
	frame.Candidates = candidates
	return frame
}

func candidateSize(candidate Candidate) (uint64, error) {
	encoded, err := json.Marshal(candidate)
	if err != nil {
		return 0, err
	}
	return uint64(len(encoded)), nil
}

func locationKey(candidate Candidate) string {
	return candidate.DatabaseID + "\x00" + candidate.TableID + "\x00" + candidate.Path
}
