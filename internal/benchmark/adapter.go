package benchmark

import (
	"context"

	"github.com/HW-Yue/Memora/internal/result"
)

type ScriptedAdapter struct {
	name     string
	outcomes map[string]Outcome
}

func NewScriptedAdapter(name string, outcomes map[string]Outcome) *ScriptedAdapter {
	return &ScriptedAdapter{name: name, outcomes: outcomes}
}

func (adapter *ScriptedAdapter) Name() string { return adapter.name }

func (adapter *ScriptedAdapter) Run(_ context.Context, scenario Scenario) (Outcome, error) {
	outcome, ok := adapter.outcomes[scenario.ID]
	if !ok {
		return Outcome{}, benchmarkError(result.CodeNotFound, "adapter %q has no outcome for scenario %q", adapter.name, scenario.ID)
	}
	return outcome, nil
}

func BaselineNames() []string {
	return []string{"no-memory", "markdown-search", "vector", "memora"}
}
