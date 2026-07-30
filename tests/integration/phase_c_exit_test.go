//go:build integration

package integration_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/benchmark"
	"github.com/HW-Yue/Memora/internal/claudeadapter"
	"github.com/HW-Yue/Memora/internal/codexadapter"
	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/runtimegate"
	"github.com/HW-Yue/Memora/internal/skillcontract"
)

func TestPhaseCExitSkillFirstHostsTakeoverConflictAndNoModelKey(t *testing.T) {
	root := repositoryRoot(t)
	canonical := filepath.Join(root, "skills", "memora")
	bundle, err := skillcontract.Load(canonical)
	if err != nil || bundle.Validate() != nil {
		t.Fatalf("canonical Skill = %#v, %v", bundle.Contract, err)
	}
	for _, token := range []string{"ask the user", "never stored as a Row", "memora feedback", "memora assimilate"} {
		if !strings.Contains(strings.ToLower(bundle.Markdown), strings.ToLower(token)) {
			t.Errorf("canonical Skill omits Phase C boundary %q", token)
		}
	}
	codex, err := codexadapter.Build(canonical)
	if err != nil {
		t.Fatal(err)
	}
	claude, err := claudeadapter.Build(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if codex.Manifest.CanonicalDigest != claude.Manifest.CanonicalDigest || codex.Manifest.ProtocolDigest != claude.Manifest.ProtocolDigest {
		t.Fatal("Codex and Claude Code do not share one Skill/protocol")
	}
	suite, err := benchmark.Load(filepath.Join(root, "benchmarks", "ai-native-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	foundColdStart, foundHostSwitch := false, false
	for _, scenario := range suite.Scenarios {
		foundColdStart = foundColdStart || scenario.Kind == benchmark.KindColdStart
		foundHostSwitch = foundHostSwitch || scenario.Kind == benchmark.KindHostSwitch
	}
	if !foundColdStart || !foundHostSwitch {
		t.Fatal("Phase C benchmark omits takeover or host switching")
	}
	assessment := runtimegate.Evaluate(runtimegate.Evidence{
		Capabilities: runtimegate.RequiredCapabilities(), Hosts: []string{"codex", "claude-code"}, BenchmarkKinds: 5,
	})
	if assessment.Decision != runtimegate.DecisionDefer || len(assessment.CoverageGaps) != 0 {
		t.Fatalf("embedded runtime gate = %#v", assessment)
	}
	encoded, err := json.Marshal(config.Config{InstanceName: "default", LogLevel: "info"})
	if err != nil || strings.Contains(strings.ToLower(string(encoded)), "key") || strings.Contains(strings.ToLower(string(encoded)), "model") {
		t.Fatalf("v0 config leaks model credential surface: %s, %v", encoded, err)
	}
	unsafeConfig := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(unsafeConfig, []byte(`{"instance":"default","log_level":"info","api_key":"secret"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(config.LoadOptions{ConfigFile: unsafeConfig}); err == nil {
		t.Fatal("v0 config accepted an API key")
	}
}
