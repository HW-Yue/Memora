package routemutationplan_test

import (
	"context"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
)

// A SPLIT or MERGE target is a Route that does not exist yet, so it is held to
// the same rule as CREATE ROUTE: the purpose has to describe what will be kept
// there, not repeat the name. Reshaping is exactly when those sentences get
// written, so this is the cheapest moment to refuse a label that says nothing.
// See docs/planning/route-purpose-contract.md.
func TestBuildRefusesATargetWhosePurposeRepeatsItsName(t *testing.T) {
	t.Parallel()
	_, err := routemutationplan.Build(context.Background(), branchSplitFixture(),
		routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"},
		splitProposal([]routemutationplan.TargetProposal{
			{Key: "recent", Name: "recent", Purpose: "  Recent  ", ChildRouteIDs: []string{"route_b"}},
			{Key: "archive", Name: "archive", Purpose: "notes nobody has touched this year", ChildRouteIDs: []string{"route_a"}},
		}))
	if err == nil {
		t.Fatal("a target that names itself twice must be refused at plan time")
	}
	var planError interface{ StableCode() string }
	if !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("the refusal must say what is wrong: %v", err)
	}
	if ok := asStableCode(err, &planError); !ok || planError.StableCode() != string(result.CodeValidation) {
		t.Fatalf("code = %v", err)
	}
}

// The second gate, on the way in to the engine: a plan built elsewhere, or
// built before this rule existed, is refused at apply rather than trusted.
func TestValidateRefusesACreateWhosePurposeRepeatsItsName(t *testing.T) {
	t.Parallel()
	plan, err := routemutationplan.Build(context.Background(), branchSplitFixture(),
		routemutationplan.Scope{DatabaseID: "db_work", TableID: "tbl_notes"},
		splitProposal([]routemutationplan.TargetProposal{
			{Key: "recent", Name: "recent", Purpose: "notes from the last month", ChildRouteIDs: []string{"route_b"}},
			{Key: "archive", Name: "archive", Purpose: "notes nobody has touched this year", ChildRouteIDs: []string{"route_a"}},
		}))
	if err != nil {
		t.Fatal(err)
	}
	for index := range plan.Creates {
		if plan.Creates[index].Name == "recent" {
			plan.Creates[index].Purpose = "ＲＥＣＥＮＴ"
		}
	}
	if err := routemutationplan.Validate(plan); err == nil {
		t.Fatal("a plan whose Create describes nothing must be refused")
	} else if !strings.Contains(err.Error(), "purpose") {
		t.Fatalf("the refusal must say what is wrong: %v", err)
	}
}

func asStableCode(err error, target *interface{ StableCode() string }) bool {
	value, ok := err.(interface{ StableCode() string })
	if ok {
		*target = value
	}
	return ok
}
