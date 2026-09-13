package nativeconfig

import (
	"fmt"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

const (
	RoutePolicyKey         = "route_policy"
	PolicySnapshotKey      = "memora.configuration.route_policy"
	DefaultBranchFanout    = router.DefaultBranchFanout
	minimumBranchFanout    = 2
	maximumBranchFanout    = router.MaxConfigurableBranchFanout
	routePolicyBootReason  = "materialize database Route policy defaults"
	routePolicyBootstrapBy = "engine:bootstrap"
)

// RoutePolicy holds the semantic structure limits a Database enforces on its
// Router tree. BranchFanout is the maximum number of live children one root or
// branch may carry; it is not the SHOW ROUTES read page budget.
type RoutePolicy struct {
	BranchFanout int `json:"branch_fanout"`
}

type PolicyRevision struct {
	Version          string      `json:"version"`
	Key              string      `json:"key"`
	Revision         uint64      `json:"revision"`
	Policy           RoutePolicy `json:"policy"`
	Actor            string      `json:"actor"`
	Reason           string      `json:"reason"`
	RestoredRevision uint64      `json:"restored_revision,omitempty"`
	RecordedAt       time.Time   `json:"recorded_at"`
}

func DefaultRoutePolicy() RoutePolicy {
	return RoutePolicy{BranchFanout: DefaultBranchFanout}
}

func ValidateRoutePolicy(value RoutePolicy) error {
	if value.BranchFanout < minimumBranchFanout || value.BranchFanout > maximumBranchFanout {
		return configError(result.CodeConstraint, fmt.Sprintf(
			"branch_fanout must be between %d and %d", minimumBranchFanout, maximumBranchFanout,
		))
	}
	return nil
}

// ValidateFanoutStep bounds how far one mutation may raise branch_fanout.
// Widening the semantic tree must stay a repeated, justified decision, so an
// Agent cannot answer one crowded parent by jumping to the ceiling. Lowering
// is unrestricted.
func ValidateFanoutStep(current, next int) error {
	if next <= current {
		return nil
	}
	allowed := router.NextBranchFanout(current, maximumBranchFanout)
	if next > allowed {
		return configError(result.CodeConstraint, fmt.Sprintf(
			"branch_fanout may rise by at most %d per change: this database allows %d and may move to %d next, not %d",
			router.MaxBranchFanoutIncrease, current, allowed, next,
		))
	}
	return nil
}
