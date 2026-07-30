package storygate_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/storygate"
)

func TestRuntimeGateRequiresEveryStoryAndRouteRediscovery(t *testing.T) {
	report := completeReport("codex")
	if err := storygate.Validate(report); err != nil {
		t.Fatal(err)
	}
	report.Stories[3].RediscoveredFromRoot = false
	if err := storygate.Validate(report); err == nil || !strings.Contains(err.Error(), "bypassed") {
		t.Fatalf("missing rediscovery error = %v", err)
	}
}

func TestRuntimeGateRequiresMatchingCodexAndClaudeProtocol(t *testing.T) {
	codex, claude := completeReport("codex"), completeReport("claude")
	if err := storygate.ValidatePair(codex, claude); err != nil {
		t.Fatal(err)
	}
	claude.ProtocolDigest = strings.Repeat("b", 64)
	if err := storygate.ValidatePair(codex, claude); err == nil {
		t.Fatal("mismatched protocol passed")
	}
}

func completeReport(host string) storygate.Report {
	digest := strings.Repeat("a", 64)
	report := storygate.Report{
		Version: storygate.ReportVersion, Status: "passed", Host: host,
		BinarySHA256: digest, CanonicalDigest: digest, ProtocolDigest: digest,
	}
	for index, story := range storygate.RequiredStories() {
		id := "step-" + story
		report.Steps = append(report.Steps, storygate.Step{ID: id, Surface: "public-cli", OutputSHA256: digest})
		report.Stories = append(report.Stories, storygate.StoryResult{
			Story: story, Status: "passed", Steps: []string{id},
			RediscoveredFromRoot: index >= 3,
		})
	}
	return report
}
