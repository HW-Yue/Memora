package skillwrite

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/router"
)

// A Mutation Plan is the host's chance to have a write checked before any tool
// call, so it has to accept every form the engine itself sanctions. It once
// refused the one form the Skill tells a host to prefer, and a fresh agent that
// hit the contradiction sent its INSERT through `memora exec` instead — with no
// preflight at all.
func insertPlan() Plan {
	return Plan{
		Version:             PlanVersion,
		ID:                  "plan-9",
		Decision:            DecisionInsert,
		Database:            "work",
		Table:               "notes",
		Actor:               "agent:host",
		SourceEventID:       "conversation:event-9",
		Reason:              "record the decision",
		AuthorizedDatabases: []string{"work"},
		Preflight:           []Check{{ID: "census", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: pointer(0)}},
		Steps: []Step{{
			ID:     "insert",
			Kind:   "INSERT",
			Target: "work.notes",
			MSQL:   "INSERT INTO work.notes (title, summary) VALUES (:title, :summary)",
			Input: executor.StatementInput{
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1,
					MaxAffectedRows:       1,
					Actor:                 "agent:host",
					Source:                "conversation:event-9",
					Reason:                "record the decision",
					RouteLeafIDs:          []string{"route_leaf"},
				},
			},
		}},
		Verify: []Check{{ID: "read-back", MSQL: "SELECT row_id FROM work.notes LIMIT 1", ExpectRows: pointer(1)}},
	}
}

func updatePlan() Plan {
	plan := insertPlan()
	plan.Decision = DecisionRevise
	plan.Steps[0].Kind = "UPDATE"
	plan.Steps[0].MSQL = "UPDATE work.notes SET summary = :summary WHERE row_id = :row"
	plan.Steps[0].Input.Mutation.ExpectedRevision = 1
	return plan
}

func pointer(value int) *int { return &value }

func pathSegments() []router.PathSegment {
	return []router.PathSegment{
		{Name: "architecture", Kind: router.KindBranch, Purpose: "Architecture decisions"},
		{Name: "sqlite", Kind: router.KindLeaf, Purpose: "Why SQLite"},
	}
}

func withRoutePath(segments []router.PathSegment) Plan {
	plan := insertPlan()
	plan.Steps[0].Input.Mutation.RouteLeafIDs = nil
	plan.Steps[0].Input.Mutation.RoutePath = segments
	return plan
}

func TestAPlanCanMountAnInsertByRoutePath(t *testing.T) {
	if err := withRoutePath(pathSegments()).Validate(); err != nil {
		t.Fatalf("a route_path INSERT was refused: %v", err)
	}
}

func TestAPlanRefusesBothMountFormsAtOnce(t *testing.T) {
	plan := withRoutePath(pathSegments())
	plan.Steps[0].Input.Mutation.RouteLeafIDs = []string{"route_leaf"}
	err := plan.Validate()
	if err == nil {
		t.Fatal("a plan mounting by route_path and route_leaf_ids at once was accepted")
	}
	if !strings.Contains(err.Error(), "route_path") {
		t.Fatalf("the refusal does not name route_path: %v", err)
	}
}

func TestAPlanRefusesAnInsertWithNoMountAtAll(t *testing.T) {
	plan := insertPlan()
	plan.Steps[0].Input.Mutation.RouteLeafIDs = nil
	if err := plan.Validate(); err == nil {
		t.Fatal("an INSERT with neither mount form was accepted")
	}
}

func TestAPlanRefusesARoutePathThatIsNotARowPosition(t *testing.T) {
	last := pathSegments()
	last[len(last)-1].Kind = router.KindBranch
	if err := withRoutePath(last).Validate(); err == nil {
		t.Fatal("a route_path whose last segment is a branch was accepted")
	}
	interior := pathSegments()
	interior[0].Kind = router.KindLeaf
	if err := withRoutePath(interior).Validate(); err == nil {
		t.Fatal("a route_path with a leaf above the last segment was accepted")
	}
	blank := pathSegments()
	blank[0].Name = "  "
	if err := withRoutePath(blank).Validate(); err == nil {
		t.Fatal("a route_path with a blank name was accepted")
	}
	purposeless := pathSegments()
	purposeless[1].Purpose = ""
	if err := withRoutePath(purposeless).Validate(); err == nil {
		t.Fatal("a route_path with no purpose was accepted")
	}
	rootless := pathSegments()
	rootless[0].Kind = router.KindRoot
	if err := withRoutePath(rootless).Validate(); err == nil {
		t.Fatal("a route_path that declares its own root was accepted")
	}
	if err := withRoutePath(nil).Validate(); err == nil {
		t.Fatal("an INSERT with an empty route_path and no snapshot was accepted")
	}
}

func TestARowOccupiesExactlyOneLeaf(t *testing.T) {
	two := insertPlan()
	two.Steps[0].Input.Mutation.RouteLeafIDs = []string{"route_one", "route_two"}
	if err := two.Validate(); err == nil {
		t.Fatal("an INSERT naming two Route leaves was accepted")
	}
	empty := insertPlan()
	empty.Steps[0].Input.Mutation.RouteLeafIDs = []string{}
	if err := empty.Validate(); err == nil {
		t.Fatal("an INSERT with an empty Route snapshot was accepted")
	}
	if err := updatePlan().Validate(); err != nil {
		t.Fatalf("a REVISE naming exactly one leaf was refused: %v", err)
	}
}

func TestOnlyAnInsertMountsByRoutePath(t *testing.T) {
	plan := updatePlan()
	plan.Steps[0].Input.Mutation.RouteLeafIDs = nil
	plan.Steps[0].Input.Mutation.RoutePath = pathSegments()
	if err := plan.Validate(); err == nil {
		t.Fatal("an UPDATE carrying route_path was accepted")
	}
}
