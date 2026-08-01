package discovery_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/HW-Yue/Memora/internal/discovery"
)

func TestBuilderRejectsMixedSnapshotAndCatalogRevision(t *testing.T) {
	t.Parallel()
	builder, err := discovery.NewBuilder("sha256:view-a", "sha256:catalog-a", discovery.Budget{
		CandidateLimit: 8, UTF8ByteLimit: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := discovery.Batch{
		Snapshot: "sha256:view-a", CatalogRevision: "sha256:catalog-a",
		Predictor: "catalog/v1", Status: discovery.PredictorSucceeded,
		ScoreKind: discovery.ScoreNone, Reason: "authorized compact catalog",
	}
	if err := builder.Add(base); err != nil {
		t.Fatal(err)
	}
	wrongSnapshot := base
	wrongSnapshot.Predictor = "lexical/v1"
	wrongSnapshot.Snapshot = "sha256:view-b"
	if err := builder.Add(wrongSnapshot); !errors.Is(err, discovery.ErrSnapshotMismatch) {
		t.Fatalf("mixed snapshot error = %v", err)
	}
	wrongCatalog := base
	wrongCatalog.Predictor = "semantic-exact/v1"
	wrongCatalog.CatalogRevision = "sha256:catalog-b"
	if err := builder.Add(wrongCatalog); !errors.Is(err, discovery.ErrCatalogRevisionMismatch) {
		t.Fatalf("mixed catalog error = %v", err)
	}
}

func TestBuilderEnforcesOneGlobalCandidateAndByteBudget(t *testing.T) {
	t.Parallel()
	score := 3.0
	first := discovery.Batch{
		Snapshot: "sha256:view", CatalogRevision: "sha256:catalog", Predictor: "lexical/v1",
		Status: discovery.PredictorSucceeded, ScoreKind: discovery.ScoreMatchCount, Reason: "term locations",
		Candidates: []discovery.CandidateInput{
			{DatabaseID: "db_work", TableID: "tbl_notes", Reason: "three matching route terms", Score: &score},
			{DatabaseID: "db_work", TableID: "tbl_tasks", Reason: "three matching route terms", Score: &score},
		},
	}
	second := discovery.Batch{
		Snapshot: "sha256:view", CatalogRevision: "sha256:catalog", Predictor: "semantic-exact/v1",
		Status: discovery.PredictorSucceeded, ScoreKind: discovery.ScoreDotProduct, Reason: "exact CPU match",
		Candidates: []discovery.CandidateInput{
			{DatabaseID: "db_work", TableID: "tbl_people", RouteID: "r_people", RouteRevision: 2, Reason: "closest route"},
		},
	}
	exactScore := 0.75
	second.Candidates[0].Score = &exactScore

	builder, err := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 2, UTF8ByteLimit: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := builder.Add(second); err != nil {
		t.Fatal(err)
	}
	frame := builder.Frame()
	if len(frame.Candidates) != 2 || frame.Budget.CandidatesUsed != 2 || !frame.Truncated {
		t.Fatalf("global candidate budget frame = %#v", frame)
	}
	if len(frame.Predictors) != 2 || !frame.Predictors[1].Truncated || frame.Predictors[1].CandidateCount != 0 {
		t.Fatalf("second predictor receipt = %#v", frame.Predictors)
	}

	probe, err := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 8, UTF8ByteLimit: 4096,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := probe.Add(first); err != nil {
		t.Fatal(err)
	}
	firstBytes := probe.Frame().Budget.UTF8BytesUsed
	byteLimited, err := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 8, UTF8ByteLimit: firstBytes,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := byteLimited.Add(first); err != nil {
		t.Fatal(err)
	}
	if err := byteLimited.Add(second); err != nil {
		t.Fatal(err)
	}
	byteFrame := byteLimited.Frame()
	if len(byteFrame.Candidates) != 2 || !byteFrame.Truncated || byteFrame.Budget.UTF8BytesUsed != firstBytes {
		t.Fatalf("global byte budget frame = %#v", byteFrame)
	}
}

func TestUnavailablePredictorIsReceiptNotQueryFailure(t *testing.T) {
	t.Parallel()
	builder, err := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 8, UTF8ByteLimit: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	err = builder.Add(discovery.Batch{
		Snapshot: "sha256:view", CatalogRevision: "sha256:catalog", Predictor: "semantic-exact/v1",
		Status: discovery.PredictorUnavailable, ScoreKind: discovery.ScoreDotProduct,
		Reason: "compatible route encoder generation is not installed",
	})
	if err != nil {
		t.Fatal(err)
	}
	frame := builder.Frame()
	if len(frame.Candidates) != 0 || len(frame.Predictors) != 1 ||
		frame.Predictors[0].Status != discovery.PredictorUnavailable || frame.Truncated {
		t.Fatalf("unavailable predictor frame = %#v", frame)
	}
	if err := frame.Validate(); err != nil {
		t.Fatalf("Frame.Validate() error = %v", err)
	}
}

func TestFrameRejectsInvalidProvenanceLocationsAndScores(t *testing.T) {
	t.Parallel()
	tests := map[string]discovery.Batch{
		"missing provenance": {
			Snapshot: "sha256:view", CatalogRevision: "sha256:catalog", Status: discovery.PredictorSucceeded,
			ScoreKind: discovery.ScoreNone, Reason: "catalog",
		},
		"route skips table": validBatch(discovery.CandidateInput{
			DatabaseID: "db_work", RouteID: "r_notes", RouteRevision: 1, Reason: "bad hierarchy",
		}),
		"nan score": validBatch(discovery.CandidateInput{
			DatabaseID: "db_work", TableID: "tbl_notes", Reason: "bad score", Score: floatPointer(math.NaN()),
		}),
	}
	for name, batch := range tests {
		name, batch := name, batch
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			builder, err := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
				CandidateLimit: 8, UTF8ByteLimit: 4096,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := builder.Add(batch); !errors.Is(err, discovery.ErrInvalidFrame) {
				t.Fatalf("Add() error = %v", err)
			}
		})
	}

	duplicate := validBatch(discovery.CandidateInput{
		DatabaseID: "db_work", TableID: "tbl_notes", Reason: "same place", Score: floatPointer(2),
	})
	duplicate.Candidates = append(duplicate.Candidates, duplicate.Candidates[0])
	builder, _ := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 8, UTF8ByteLimit: 4096,
	})
	if err := builder.Add(duplicate); !errors.Is(err, discovery.ErrInvalidFrame) {
		t.Fatalf("duplicate Add() error = %v", err)
	}
}

func TestDiscoveryFrameWireRoundTripAndUnknownFieldCompatibility(t *testing.T) {
	t.Parallel()
	builder, _ := discovery.NewBuilder("sha256:view", "sha256:catalog", discovery.Budget{
		CandidateLimit: 4, UTF8ByteLimit: 2048,
	})
	if err := builder.Add(validBatch(discovery.CandidateInput{
		DatabaseID: "db_work", TableID: "tbl_notes", Reason: "matched title term", Score: floatPointer(2),
	})); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(builder.Frame())
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip discovery.Frame
	if err := json.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if len(roundTrip.Candidates) != 1 || roundTrip.Usage != discovery.UsageNavigationOnly {
		t.Fatalf("round trip = %#v", roundTrip)
	}

	withUnknown := append([]byte(nil), encoded[:len(encoded)-1]...)
	withUnknown = append(withUnknown, []byte(`,"future":{"enabled":true}}`)...)
	if err := json.Unmarshal(withUnknown, &roundTrip); err != nil {
		t.Fatalf("unknown field Unmarshal() error = %v", err)
	}

	unknownEnum := append([]byte(nil), encoded...)
	unknownEnum = []byte(string(unknownEnum[:]))
	unknownEnum = replaceOnce(unknownEnum, `"score_kind":"match_count"`, `"score_kind":"mystery"`)
	if err := json.Unmarshal(unknownEnum, &roundTrip); !errors.Is(err, discovery.ErrInvalidFrame) {
		t.Fatalf("unknown enum error = %v", err)
	}
}

func validBatch(candidate discovery.CandidateInput) discovery.Batch {
	return discovery.Batch{
		Snapshot: "sha256:view", CatalogRevision: "sha256:catalog", Predictor: "lexical/v1",
		Status: discovery.PredictorSucceeded, ScoreKind: discovery.ScoreMatchCount,
		Reason: "term locations", Candidates: []discovery.CandidateInput{candidate},
	}
}

func floatPointer(value float64) *float64 { return &value }

func replaceOnce(input []byte, old, replacement string) []byte {
	for index := 0; index+len(old) <= len(input); index++ {
		if string(input[index:index+len(old)]) == old {
			output := append([]byte(nil), input[:index]...)
			output = append(output, replacement...)
			return append(output, input[index+len(old):]...)
		}
	}
	return input
}
