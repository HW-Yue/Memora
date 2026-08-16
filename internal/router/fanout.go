package router

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
)

// DefaultBranchFanout is the structural fan-out a Database starts with. It is
// a startup default the owning Agent may revise, not a semantic constant.
const DefaultBranchFanout = 12

// MaxBranchFanoutIncrease bounds how far one mutation may raise a Database's
// structural fan-out. Widening the semantic tree is a judgement the owning
// Agent has to make repeatedly rather than once: each raise costs a separate
// decision, a separate reason and a separate revision, so a crowded parent
// cannot be resolved by jumping straight to the ceiling. Lowering the limit is
// not bounded; only growth is deliberated.
const MaxBranchFanoutIncrease = 4

// MaxConfigurableBranchFanout is the ceiling a Database's fan-out may reach.
// It is a sanity bound on the configuration value, not a default and not a
// target: reaching it takes many separate, individually justified raises.
const MaxConfigurableBranchFanout = 100

// NextBranchFanout reports the highest fan-out one mutation may move to from
// current, clamped to ceiling. Callers surface it so the Agent is told the
// value it may actually ask for instead of discovering the bound by failing.
func NextBranchFanout(current, ceiling int) int {
	next := current + MaxBranchFanoutIncrease
	if next > ceiling {
		return ceiling
	}
	return next
}

// RaiseBranchFanoutStatement is the MSQL an Agent runs when it decides the
// Database itself should carry a wider semantic fan-out.
const RaiseBranchFanoutStatement = "ALTER CONFIGURATION ROUTE_POLICY SET BRANCH_FANOUT :fanout"

// RestructureSubtreeStatement is the MSQL an Agent runs when it decides the
// crowded parent should be regrouped instead.
const RestructureSubtreeStatement = "PLAN ROUTE MUTATION ON :table PROPOSAL :proposal"

// BranchOverflowError reports that a write would push one root or branch past
// the Database's structural fan-out limit. Exceeding the limit is never a
// warning and never silently paginated: the write fails, and the Agent chooses
// between restructuring the subtree and raising this Database's limit.
type BranchOverflowError struct {
	ParentRouteID string
	LiveChildren  int
	BranchFanout  int
	// NextBranchFanout is the highest limit one raise may move to from here.
	// Zero means the caller did not resolve a ceiling; the message then omits
	// the concrete value rather than inventing one.
	NextBranchFanout int
}

func (err *BranchOverflowError) Error() string {
	raise := RaiseBranchFanoutStatement
	if err.NextBranchFanout > err.BranchFanout {
		raise = fmt.Sprintf(
			"%s (at most %d next; a raise may add %d at a time)",
			RaiseBranchFanoutStatement, err.NextBranchFanout, MaxBranchFanoutIncrease,
		)
	}
	return fmt.Sprintf(
		"Route branch fan-out limit reached: %s already carries %d live children and this database allows %d; "+
			"either restructure the subtree or raise the limit with %s",
		err.ParentRouteID, err.LiveChildren, err.BranchFanout, raise,
	)
}

func (err *BranchOverflowError) StableCode() string { return string(result.CodeConstraint) }

// ErrorDetails renders the two mutually exclusive remedies as a structured
// envelope. The engine never picks one; it only guarantees both are executable.
func (err *BranchOverflowError) ErrorDetails() map[string]any {
	return map[string]any{
		"reason":          "route_branch_fanout_exceeded",
		"parent_route_id": err.ParentRouteID,
		"live_children":   err.LiveChildren,
		"branch_fanout":   err.BranchFanout,
		"remedies": []any{
			map[string]any{
				"kind":      "restructure_subtree",
				"statement": RestructureSubtreeStatement,
				"summary":   "merge, split, or push nodes down so the new node joins an existing group",
			},
			map[string]any{
				"kind":      "raise_branch_fanout",
				"statement": RaiseBranchFanoutStatement,
				"summary":   "raise this database's structural fan-out limit with expected revision, actor, and reason",
			},
		},
	}
}

// CheckBranchFanout fails when adding `adding` children to a parent that
// already carries `live` of them would exceed the limit. Lowering the limit
// never invalidates existing nodes: only growth is refused.
func CheckBranchFanout(parentID string, live, adding, limit int) error {
	if limit < 1 || adding < 1 || live+adding <= limit {
		return nil
	}
	return &BranchOverflowError{
		ParentRouteID: parentID, LiveChildren: live, BranchFanout: limit,
		NextBranchFanout: NextBranchFanout(limit, MaxConfigurableBranchFanout),
	}
}
