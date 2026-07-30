package runtimegate

import "sort"

type Decision string

const (
	DecisionDefer             Decision = "defer"
	DecisionEvaluateCandidate Decision = "evaluate_candidate"
)

type Evidence struct {
	Capabilities              []string
	Hosts                     []string
	BenchmarkKinds            int
	StandaloneDemandValidated bool
	ProviderSecuritySpecified bool
	BudgetModelValidated      bool
}

type Assessment struct {
	Decision           Decision `json:"decision"`
	CoverageGaps       []string `json:"coverage_gaps"`
	ReopenRequirements []string `json:"reopen_requirements"`
}

func RequiredCapabilities() []string {
	return []string{
		"assimilation", "benchmark", "conflict", "conversation", "feedback", "installation",
		"maintenance", "query", "schema", "write",
	}
}

func Evaluate(evidence Evidence) Assessment {
	found := make(map[string]bool, len(evidence.Capabilities))
	for _, capability := range evidence.Capabilities {
		found[capability] = true
	}
	var gaps []string
	for _, capability := range RequiredCapabilities() {
		if !found[capability] {
			gaps = append(gaps, "missing skill-first capability: "+capability)
		}
	}
	hosts := map[string]bool{}
	for _, host := range evidence.Hosts {
		hosts[host] = true
	}
	if !hosts["codex"] || !hosts["claude-code"] {
		gaps = append(gaps, "both external hosts are not covered")
	}
	if evidence.BenchmarkKinds < 5 {
		gaps = append(gaps, "AI-native benchmark does not cover all five journeys")
	}
	sort.Strings(gaps)
	reopen := []string{}
	if !evidence.StandaloneDemandValidated {
		reopen = append(reopen, "validate a standalone use case that external hosts cannot satisfy")
	}
	if !evidence.ProviderSecuritySpecified {
		reopen = append(reopen, "freeze Provider credential, privacy, and offline behavior")
	}
	if !evidence.BudgetModelValidated {
		reopen = append(reopen, "validate token, latency, cost, step, and mutation budgets")
	}
	decision := DecisionDefer
	if len(gaps) == 0 && len(reopen) == 0 {
		decision = DecisionEvaluateCandidate
	}
	return Assessment{Decision: decision, CoverageGaps: gaps, ReopenRequirements: reopen}
}
