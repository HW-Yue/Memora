//go:build integration

package integration_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/claudeadapter"
	"github.com/HW-Yue/Memora/internal/codexadapter"
	"github.com/HW-Yue/Memora/internal/skillcontract"
	"github.com/HW-Yue/Memora/internal/storygate"
)

func TestF72AINativeStoryProviderAndLicenseGate(t *testing.T) {
	root := repositoryRoot(t)
	if err := storygate.Validate(storygate.ReleaseEvidence()); err != nil {
		t.Fatal(err)
	}

	canonical := filepath.Join(root, "skills", "memora")
	bundle, err := skillcontract.Load(canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(bundle.Contract.ProviderBoundary.Protocols, ","); got != "openai-compatible,anthropic-compatible" {
		t.Fatalf("provider protocols = %q", got)
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
		t.Fatal("host adapters do not share one canonical AI-native contract")
	}

	license, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		t.Fatal(err)
	}
	commercial, err := os.ReadFile(filepath.Join(root, "COMMERCIAL-LICENSE.md"))
	if err != nil {
		t.Fatal(err)
	}
	licenseText := strings.ToLower(strings.Join(strings.Fields(string(license)), " "))
	commercialText := strings.ToLower(strings.Join(strings.Fields(string(commercial)), " "))
	if !strings.Contains(licenseText, "noncommercial") || !strings.Contains(commercialText, "paid commercial license") {
		t.Fatal("personal-free/commercial-paid license boundary is missing")
	}
}
