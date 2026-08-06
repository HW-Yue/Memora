package answerbenchmark

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestCheckpointRoundTripAndTamperRejection(t *testing.T) {
	recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run", SessionID: "run:q1"})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := Checkpoint{Version: CheckpointVersion, Identity: strings.Repeat("a", 64), Cases: []CheckpointCase{{
		Public:  PublicCase{CaseID: "q1", Question: "question", Status: "failed", ErrorCode: "query_failed", QualityStatus: QualityNotScored},
		Private: PrivateCase{Task: answercorpus.BlindTask{CaseID: "q1", Question: "question"}, Error: "provider failed", Evidence: []agent.SelectEvidence{}, Trace: recorder.Snapshot()},
	}}}
	if err := checkpoint.Seal(); err != nil {
		t.Fatalf("Seal() error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := SaveCheckpoint(path, checkpoint); err != nil {
		t.Fatalf("SaveCheckpoint() error = %v", err)
	}
	loaded, err := LoadCheckpoint(path)
	if err != nil || loaded.Hash != checkpoint.Hash {
		t.Fatalf("LoadCheckpoint() = %#v, %v", loaded, err)
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(encoded, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCheckpoint(path); err == nil {
		t.Fatal("LoadCheckpoint(tampered) unexpectedly succeeded")
	}
}

func TestCheckpointIdentityBindsRunConfiguration(t *testing.T) {
	manifest := answercorpus.Manifest{CorpusID: "corpus", Revision: 1, Fixture: answercorpus.Fixture{SnapshotID: "snapshot", SnapshotSHA256: strings.Repeat("a", 64)}}
	first := CheckpointIdentity(manifest, RunConfig{RunID: "run", ProviderID: "deepseek", Model: "deepseek-v4-flash", ArmID: ArmAtlasLexicalPrefetch, PromptID: "prompt", CodeRevision: "revision"})
	second := CheckpointIdentity(manifest, RunConfig{RunID: "run", ProviderID: "deepseek", Model: "deepseek-v4-flash", ArmID: ArmAtlasLexical, PromptID: "prompt", CodeRevision: "revision"})
	third := CheckpointIdentity(manifest, RunConfig{RunID: "run", ProviderID: "deepseek", Model: "deepseek-v4-flash", ArmID: ArmAtlasLexicalPrefetch, PromptID: "prompt", CodeRevision: "revision", MaxProviderCalls: 6, MaxToolCalls: 5})
	if first == second || first == third || len(first) != 64 {
		t.Fatalf("identities = %q/%q/%q", first, second, third)
	}
}
