package search_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/search"
)

func TestPlannerNormalizesChannelsFusesWeightsAndUsesStableTieBreak(t *testing.T) {
	t.Parallel()

	source := fakeSource{
		agent: map[string][]search.Locator{
			"database": {
				locator("row_a", 2), locator("row_b", 1),
			},
			"architecture": {
				locator("row_a", 2), locator("row_c", 1),
			},
		},
		mechanical: map[string][]search.Locator{
			"data": {
				locator("row_b", 1), locator("row_c", 1),
			},
		},
	}
	planner, err := search.New(source, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	result, err := planner.Match(context.Background(), search.Request{
		DatabaseID:      "db_work",
		AgentTerms:      []string{"database", "architecture", "database"},
		MechanicalTerms: []string{"data"},
		Limit:           2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || !result.Truncated ||
		result.Matches[0].RowID != "row_a" ||
		result.Matches[0].AgentScore != 1 || result.Matches[0].Score != 0.8 ||
		result.Matches[1].RowID != "row_b" ||
		result.Matches[1].AgentScore != 0.5 ||
		result.Matches[1].MechanicalScore != 1 ||
		result.Matches[1].Score != 0.6 {
		t.Fatalf("Match() = %#v", result)
	}
}

func TestPlannerSupportsWeightOverrideAndReturnsOnlyLocators(t *testing.T) {
	t.Parallel()

	source := fakeSource{
		agent: map[string][]search.Locator{
			"semantic": {locator("row_b", 1)},
		},
		mechanical: map[string][]search.Locator{
			"literal": {locator("row_a", 1)},
		},
	}
	otherTable := locator("row_outside", 1)
	otherTable.TableID = "tbl_other"
	source.mechanical["literal"] = append(source.mechanical["literal"], otherTable)
	planner, err := search.New(source, search.Options{
		AgentWeight: 0.2, MechanicalWeight: 0.8,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := planner.Match(context.Background(), search.Request{
		DatabaseID: "db_work", AgentTerms: []string{"semantic"},
		MechanicalTerms: []string{"literal"}, TableID: "tbl_notes", Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Matches) != 2 || result.Matches[0].RowID != "row_a" ||
		result.Matches[0].Score != 0.8 || result.Matches[1].RowID != "row_b" {
		t.Fatalf("weighted matches = %#v", result)
	}
	if result.Matches[0].DatabaseID == "" || result.Matches[0].TableID == "" {
		t.Fatalf("match lacks stable locator: %#v", result.Matches[0])
	}
}

func TestPlannerRejectsInvalidWeightsAndBounds(t *testing.T) {
	t.Parallel()

	for _, options := range []search.Options{
		{AgentWeight: -0.1, MechanicalWeight: 1.1},
		{AgentWeight: 0.8, MechanicalWeight: 0.3},
	} {
		if _, err := search.New(fakeSource{}, options); err == nil {
			t.Fatalf("New(%#v) succeeded", options)
		}
	}
	planner, err := search.New(fakeSource{}, search.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []search.Request{
		{DatabaseID: "db_work", AgentTerms: []string{"term"}, Limit: 0},
		{DatabaseID: "db_work", AgentTerms: []string{"term"}, Limit: 1001},
		{DatabaseID: "", AgentTerms: []string{"term"}, Limit: 1},
		{DatabaseID: "db_work", Limit: 1},
	} {
		if _, err := planner.Match(context.Background(), request); err == nil {
			t.Fatalf("Match(%#v) succeeded", request)
		}
	}
}

type fakeSource struct {
	agent      map[string][]search.Locator
	mechanical map[string][]search.Locator
}

func (source fakeSource) LookupAgent(
	_ context.Context,
	_ string,
	term string,
	_ int,
) ([]search.Locator, error) {
	return append([]search.Locator{}, source.agent[term]...), nil
}

func (source fakeSource) LookupMechanical(
	_ context.Context,
	_ string,
	term string,
	_ int,
) ([]search.Locator, error) {
	return append([]search.Locator{}, source.mechanical[term]...), nil
}

func locator(rowID string, revision uint64) search.Locator {
	return search.Locator{
		DatabaseID: "db_work", TableID: "tbl_notes", RowID: rowID, Revision: revision,
	}
}
