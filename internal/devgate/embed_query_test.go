package devgate

import (
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/recall"
)

// The vector arm of a recall needs a query embedding, and the engine never makes
// one — that is the host's job. `embed_query.py` is the Skill-side half of that
// path, so its encoding has to be the engine's encoding, byte for byte: a vector
// that is nearly right is refused at the statement, and the host would be back to
// keywords without knowing why. `--encode` runs the encoder with no provider.
func TestEmbedQueryEncodesLikeTheEngine(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the Skill's scripts cannot be exercised here")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "skills", "memora", "scripts", "embed_query.py")
	cases := [][]float32{
		{0.5, -1, 0},
		{0.1, -2.5, 3.1415927, 1e-8},
		// Negative zero is a real byte pattern, so it is named rather than
		// written as a literal staticcheck rejects as a no-op.
		{1024, float32(math.Copysign(0, -1)), 7},
	}
	for _, vector := range cases {
		parts := make([]string, 0, len(vector))
		for _, value := range vector {
			parts = append(parts, strings.TrimSpace(formatFloat(value)))
		}
		command := exec.Command(python, script, "--encode", strings.Join(parts, ","))
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%v: %v", vector, err)
		}
		decoded := struct {
			Vector     string `json:"vector"`
			Dimensions int    `json:"dimensions"`
		}{}
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatalf("%v: output is not JSON: %v\n%s", vector, err, output)
		}
		want, err := recall.EncodeVector(vector)
		if err != nil {
			t.Fatal(err)
		}
		if decoded.Vector != want {
			t.Fatalf("%v: the Skill encodes %q, the engine expects %q", vector, decoded.Vector, want)
		}
		if decoded.Dimensions != len(vector) {
			t.Fatalf("%v: dimensions = %d", vector, decoded.Dimensions)
		}
	}
}

// A host with no provider gets no vector arm, and that has to be said out loud:
// the recall it runs will answer with `arms: ["keyword"]`, and an answer that
// calls that a fused recall is the silent degradation this script exists to end.
func TestEmbedQuerySaysWhenThereIsNoProvider(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the Skill's scripts cannot be exercised here")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "skills", "memora", "scripts", "embed_query.py")
	command := exec.Command(python, script)
	command.Dir = root
	// A home with no shell profile, so the documented profile fallback finds
	// nothing and this really is the unconfigured case.
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	command.Stdin = strings.NewReader(`{"text":"实习"}`)
	output, err := command.Output()
	var exit *exec.ExitError
	if err == nil || !asExitError(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("want exit 2 for an unconfigured host, got %v\n%s", err, output)
	}
	decoded := struct {
		Error string `json:"error"`
	}{}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output)
	}
	for _, name := range []string{"MEMORA_EMBEDDING_BASE_URL", "MEMORA_EMBEDDING_MODEL",
		"MEMORA_EMBEDDING_DIMENSIONS", "MEMORA_EMBEDDING_API_KEY", "keyword"} {
		if !strings.Contains(decoded.Error, name) {
			t.Fatalf("the refusal must name %s: %q", name, decoded.Error)
		}
	}
}

func asExitError(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

func formatFloat(value float32) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
