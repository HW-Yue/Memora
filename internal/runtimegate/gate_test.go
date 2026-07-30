package runtimegate_test

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/runtimegate"
)

func TestCurrentSkillFirstCoverageDefersEmbeddedRuntimeWithoutProductDemand(t *testing.T) {
	t.Parallel()

	assessment := runtimegate.Evaluate(runtimegate.Evidence{
		Capabilities: runtimegate.RequiredCapabilities(), Hosts: []string{"codex", "claude-code"},
		BenchmarkKinds: 5, StandaloneDemandValidated: false,
		ProviderSecuritySpecified: false, BudgetModelValidated: false,
	})
	if assessment.Decision != runtimegate.DecisionDefer || len(assessment.CoverageGaps) != 0 {
		t.Fatalf("assessment = %#v", assessment)
	}
	if len(assessment.ReopenRequirements) != 3 {
		t.Fatalf("reopen requirements = %#v", assessment.ReopenRequirements)
	}
}

func TestRuntimeCannotEnterCandidateEvaluationWithCoverageOrSafetyGaps(t *testing.T) {
	t.Parallel()

	evidence := runtimegate.Evidence{
		Capabilities: []string{"query", "write"}, Hosts: []string{"codex"}, BenchmarkKinds: 4,
		StandaloneDemandValidated: true, ProviderSecuritySpecified: true, BudgetModelValidated: true,
	}
	assessment := runtimegate.Evaluate(evidence)
	if assessment.Decision != runtimegate.DecisionDefer || len(assessment.CoverageGaps) == 0 {
		t.Fatalf("incomplete assessment = %#v", assessment)
	}
	evidence.Capabilities = runtimegate.RequiredCapabilities()
	evidence.Hosts = []string{"codex", "claude-code"}
	evidence.BenchmarkKinds = 5
	assessment = runtimegate.Evaluate(evidence)
	if assessment.Decision != runtimegate.DecisionEvaluateCandidate {
		t.Fatalf("complete future assessment = %#v", assessment)
	}
}
