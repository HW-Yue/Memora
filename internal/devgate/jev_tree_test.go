package devgate

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The jev tree walk is a retrieval path: one requirement in, a set of landings
// out. It is the part of the design that cannot be exercised by unit-testing a
// function — the decisions come from a hosted model and the layer data comes from
// a real database — so `--record`/`--replay` exist to make whole runs
// deterministic. The fixtures under `testdata/jev-tree/` are real runs whose
// names were replaced with placeholders (the shape is what is under test, not
// who the user interned for), plus one constructed fixture for the rule the
// model does not reliably produce on demand.
func TestJevTreeWalksFromARequirementToLandings(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the Skill's scripts cannot be exercised here")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "skills", "memora", "scripts", "jev_tree.py")

	replay := func(t *testing.T, fixture string) map[string]any {
		t.Helper()
		command := exec.Command(python, script, "--replay",
			filepath.Join(root, "internal", "devgate", "testdata", "jev-tree", fixture))
		command.Dir = root
		// No database and no provider: a replay that reached either would fail
		// here rather than pass quietly.
		command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
			"MEMORA_CLI=" + filepath.Join(t.TempDir(), "memora-that-does-not-exist")}
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s: %v\n%s", fixture, err, output)
		}
		decoded := map[string]any{}
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatalf("%s: output is not JSON: %v\n%s", fixture, err, output)
		}
		return decoded
	}
	paths := func(result map[string]any) []string {
		landings, _ := result["landings"].([]any)
		got := make([]string, 0, len(landings))
		for _, landing := range landings {
			entry, _ := landing.(map[string]any)
			got = append(got, entry["path"].(string))
		}
		return got
	}

	t.Run("two internships are two landings", func(t *testing.T) {
		result := replay(t, "two-internships.json")
		got := paths(result)
		want := []string{"/internship/ACME", "/internship/GLOBEX"}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("landings = %v, want %v", got, want)
		}
		// Every landing is a position with what navigation needs: a leaf, the
		// Row it holds, and that Row's revision. No fact, no score.
		for _, landing := range result["landings"].([]any) {
			entry := landing.(map[string]any)
			if entry["termination"] != "leaf" || entry["row_id"] == nil || entry["revision"] == nil {
				t.Fatalf("a landing must be a navigable position: %v", entry)
			}
		}
		if result["incomplete"] != false || result["decisions"] != float64(3) {
			t.Fatalf("decisions = %v, incomplete = %v", result["decisions"], result["incomplete"])
		}
	})

	t.Run("a single-target requirement is one landing", func(t *testing.T) {
		result := replay(t, "one-internship.json")
		if got := paths(result); len(got) != 1 || got[0] != "/internship/ACME" {
			t.Fatalf("landings = %v", got)
		}
	})

	t.Run("a requirement nothing covers stops without inventing one", func(t *testing.T) {
		result := replay(t, "nothing-matches.json")
		if got := paths(result); len(got) != 0 {
			t.Fatalf("landings = %v, want none", got)
		}
		if stopped, _ := result["stopped"].(string); stopped == "" {
			t.Fatalf("a stopped walk must say why: %v", result)
		}
	})

	t.Run("several databases are flattened into one table question", func(t *testing.T) {
		// The answer itself is whatever the model said that run; what is pinned
		// here is the shape: the Database layer may return more than one, and the
		// Table question then spans them in a single request rather than asking
		// each database in turn.
		result := replay(t, "across-libraries.json")
		if len(paths(result)) == 0 {
			t.Fatalf("a cross-library requirement must land somewhere: %v", result)
		}
		recording := map[string]any{}
		body, err := os.ReadFile(filepath.Join(root, "internal", "devgate", "testdata", "jev-tree", "across-libraries.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(body, &recording); err != nil {
			t.Fatal(err)
		}
		scopes := map[string]bool{}
		for key := range recording["engine"].(map[string]any) {
			if strings.Contains(key, "CATALOG ATLAS") {
				scopes[key] = true
			}
		}
		if len(scopes) < 2 {
			t.Fatalf("the recording must show the Atlas read per Database: %v", scopes)
		}
		flattened := false
		for key := range recording["jev"].(map[string]any) {
			if strings.Contains(key, "|| ") && strings.Contains(key, "me.") && strings.Contains(key, "memora.") {
				flattened = true
			}
		}
		if !flattened {
			t.Fatal("a table question spanning databases must name the database in each option")
		}
	})

	t.Run("undecided enumerates the layer instead of dropping it", func(t *testing.T) {
		// Constructed: one layer's answer is "answered, but did not separate".
		// The rule is to enumerate it — the model did not make a filter worth
		// trusting, and dropping the layer would silently lose everything behind
		// it. Both children are therefore taken and the answer says it is partial.
		result := replay(t, "undecided-enumerates.json")
		got := paths(result)
		if strings.Join(got, ",") != "/internship/ACME,/internship/GLOBEX" {
			t.Fatalf("an undecided layer must be enumerated: %v", got)
		}
		if result["incomplete"] != true {
			t.Fatalf("an enumerated layer makes the answer partial: %v", result)
		}
		uncertain, _ := result["incomplete_at"].([]any)
		if len(uncertain) == 0 || uncertain[0] != "internship" {
			t.Fatalf("incomplete_at must name the layer: %v", result["incomplete_at"])
		}
	})

	t.Run("no probability leaves the walk, and every statement is a read", func(t *testing.T) {
		for _, fixture := range []string{"two-internships.json", "one-internship.json",
			"nothing-matches.json", "across-libraries.json", "undecided-enumerates.json"} {
			body, err := os.ReadFile(filepath.Join(root, "internal", "devgate", "testdata", "jev-tree", fixture))
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"noul", "confidence", "probabilit"} {
				if strings.Contains(string(body), forbidden) {
					t.Fatalf("%s carries %q: a probability that leaves the model call is a score a caller can threshold",
						fixture, forbidden)
				}
			}
			// Every statement the walk stood on is a read on the specified faces;
			// this path may never write.
			for _, statement := range []string{"INSERT", "UPDATE", "DELETE ", "CREATE", "ALTER", "MERGE", "SPLIT"} {
				if strings.Contains(string(body), `"source": "`+statement) {
					t.Fatalf("%s records a write (%s): the walk is read-only", fixture, statement)
				}
			}
		}
	})
}
