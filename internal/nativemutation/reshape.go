package nativemutation

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/row"
)

type ReshapePlan struct {
	Sources Plan
	Targets []RowChange
}

// Split commits one superseded source and at least two AI-authored targets.
// The engine validates the shape but never infers semantic boundaries or routes.
func (coordinator *Coordinator) Split(plan ReshapePlan) error {
	return coordinator.reshape(plan, history.OperationSplit, 1, 2)
}

// Merge commits at least two superseded sources and one AI-authored target.
func (coordinator *Coordinator) Merge(plan ReshapePlan) error {
	return coordinator.reshape(plan, history.OperationMerge, 2, 1)
}

func (coordinator *Coordinator) reshape(plan ReshapePlan, operation history.Operation, minimumSources, targetCount int) error {
	if len(plan.Sources.Changes) < minimumSources || len(plan.Targets) != targetCount {
		return fmt.Errorf("%s requires at least %d sources and exactly %d targets", operation, minimumSources, targetCount)
	}
	changes := append([]RowChange(nil), plan.Sources.Changes...)
	for index := range changes {
		if changes[index].Initial || changes[index].Row.State != row.StateSuperseded {
			return fmt.Errorf("%s source must be a superseded revision", operation)
		}
		changes[index].Operation = operation
	}
	for _, target := range plan.Targets {
		if target.Row.State != row.StateLive {
			return fmt.Errorf("%s target must be live", operation)
		}
		target.Operation = operation
		changes = append(changes, target)
	}
	plan.Sources.Changes = changes
	return coordinator.Commit(plan.Sources)
}
