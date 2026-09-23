package devgate

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The jev path's one decision is the set cut: given per-candidate answers and the
// floor probe from the same request, which candidates is the layer's answer?
// Getting it wrong is not a crash — it is a second internship silently missing
// from the answer, which is exactly the defect this rule was written for. The
// fixtures pair two answers recorded from real `jev-1.13.0` requests with the two
// edges of the rule, and `--replay` runs the cut without a network or a key.
func TestJevSetCutDecidesFromTheRequestsOwnAnswers(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the jev script cannot be exercised here")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "skills", "memora", "scripts", "jev_select.py")
	cases := []struct {
		fixture  string
		relevant []string
		decision string
	}{
		// Recorded: "全部实习经历，两家公司的实习都要" — two children answered, a
		// third rejected, and the floor below all of them.
		{"both-internships.json", []string{"OPPO", "悠悠有品"}, "separated"},
		// Recorded: "只想了解自己在 OPPO 的实习经历" — same options, one answer.
		{"one-internship.json", []string{"OPPO"}, "separated"},
		// The floor probe wins: nothing in this layer answers the intent.
		{"nothing-matches.json", []string{}, "empty"},
		// Every candidate answered, none separated: the caller enumerates.
		{"no-clear-gap.json", []string{}, "undecided"},
	}
	for _, testCase := range cases {
		fixture := filepath.Join(root, "internal", "devgate", "testdata", "jev", testCase.fixture)
		command := exec.Command(python, script, "--replay", fixture)
		command.Dir = root
		output, err := command.Output()
		if err != nil {
			t.Fatalf("%s: %v", testCase.fixture, err)
		}
		decoded := struct {
			Mode     string   `json:"mode"`
			Relevant []string `json:"relevant"`
			Decision string   `json:"decision"`
		}{}
		if err := json.Unmarshal(output, &decoded); err != nil {
			t.Fatalf("%s: output is not JSON: %v\n%s", testCase.fixture, err, output)
		}
		if decoded.Mode != "set" || decoded.Decision != testCase.decision ||
			strings.Join(decoded.Relevant, ",") != strings.Join(testCase.relevant, ",") {
			t.Fatalf("%s: got %s/%v, want set/%s/%v",
				testCase.fixture, decoded.Decision, decoded.Relevant, testCase.decision, testCase.relevant)
		}
		// A probability is a relevance score, and a score that leaves this
		// process is one a caller can threshold, rank or store. The answer is a
		// set and a decision, and nothing else.
		for _, forbidden := range []string{"noul", "confidence", "probabilit"} {
			if strings.Contains(string(output), forbidden) {
				t.Fatalf("%s: the answer carries %q, which must stay inside the script:\n%s",
					testCase.fixture, forbidden, output)
			}
		}
	}
}

// The walk stopped handing jev the name in the purpose's place, and this is
// where that could quietly be undone: the script is the last thing between the
// caller and the provider, and it used to substitute the name for a missing
// purpose. A layer whose labels were never written then looked exactly like a
// described one — that is how 79% of the tree rotted without anything saying so.
// `--dry-run` prints the request that would be sent and needs no key or network.
func TestJevNeverInventsADescriptionFromTheName(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the jev script cannot be exercised here")
	}
	root := repoRoot(t)
	script := filepath.Join(root, "skills", "memora", "scripts", "jev_select.py")
	request := `{"mode":"set","intent":"memora 项目当前的缺口有哪些","options":[` +
		`{"name":"工作方式"},` +
		`{"name":"当前状态","purpose":""},` +
		`{"name":"当前缺口","purpose":"这个库里还没做完的事"}]}`
	command := exec.Command(python, script, "--dry-run")
	command.Dir = root
	command.Stdin = strings.NewReader(request)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	decoded := struct {
		Request struct {
			State struct {
				Candidates []struct {
					Name    string `json:"name"`
					Purpose string `json:"purpose"`
				} `json:"candidates"`
			} `json:"state"`
		} `json:"request"`
	}{}
	if err := json.Unmarshal(output, &decoded); err != nil {
		t.Fatalf("dry run output is not JSON: %v\n%s", err, output)
	}
	candidates := decoded.Request.State.Candidates
	if len(candidates) != 3 {
		t.Fatalf("got %d candidates, want 3:\n%s", len(candidates), output)
	}
	for index, want := range []string{"", "", "这个库里还没做完的事"} {
		if candidates[index].Purpose != want {
			t.Fatalf("candidate %d (%s) carries purpose %q, want %q: a name must never stand in for a missing description",
				index, candidates[index].Name, candidates[index].Purpose, want)
		}
	}
}
