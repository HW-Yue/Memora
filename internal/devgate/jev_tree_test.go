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
		// Stopping is a list of what was never walked, with the reason — not a
		// string, and not a landing: a branch nobody looked at must not be handed
		// back in the same array as a position.
		stopped, _ := result["stopped"].([]any)
		if len(stopped) == 0 {
			t.Fatalf("a stopped walk must say what it never reached: %v", result)
		}
		entry, _ := stopped[0].(map[string]any)
		if entry["reason"] == nil || entry["reason"] == "" {
			t.Fatalf("a stopped branch must carry its reason: %v", stopped[0])
		}
		if result["incomplete"] != true {
			t.Fatalf("a stopped walk is not a whole answer: %v", result)
		}
	})

	t.Run("a landing is a leaf path plus the handle a back-table read needs", func(t *testing.T) {
		// The owner's contract: the walk locates. The path is what the agent judges
		// against, and the database, table and row id are what it writes its own
		// `SELECT … WHERE row_id = :row` with — a path alone cannot be turned into
		// that query, and a row id alone does not say which table to read.
		result := replay(t, "two-internships.json")
		for _, landing := range result["landings"].([]any) {
			entry := landing.(map[string]any)
			for _, field := range []string{"database", "table", "path", "leaf_route_id", "row_id", "revision"} {
				if entry[field] == nil || entry[field] == "" {
					t.Fatalf("a landing must carry %s: %v", field, entry)
				}
			}
			if entry["termination"] != "leaf" {
				t.Fatalf("a landing is a leaf: %v", entry)
			}
		}
		if _, present := result["stopped"]; present {
			t.Fatalf("a walk that finished has stopped nothing: %v", result["stopped"])
		}
	})

	t.Run("no branch is ever reported as a landing", func(t *testing.T) {
		// The regression this rule exists for: the walk used to cut its frontier by
		// arrival order and then write every branch it cut into `landings` with a
		// `budget:` termination — so "could not reach the answer" and "searched, not
		// there" came back in the same array, distinguishable only by one string.
		for _, fixture := range []string{"two-internships.json", "one-internship.json",
			"across-libraries.json", "undecided-enumerates.json", "purpose-repeats-name.json"} {
			result := replay(t, fixture)
			for _, landing := range result["landings"].([]any) {
				entry := landing.(map[string]any)
				if entry["termination"] != "leaf" || entry["row_id"] == nil {
					t.Fatalf("%s: %v was handed back as a position", fixture, entry)
				}
			}
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
		// The layer name carries its Database: a bare "internship" (or "root") names
		// one layer in each Database, and which one was enumerated is the whole
		// point of reporting it.
		if len(uncertain) == 0 || uncertain[0] != "me:internship" {
			t.Fatalf("incomplete_at must name the layer with its Database: %v", result["incomplete_at"])
		}
	})

	t.Run("the landings come back as the reads they imply", func(t *testing.T) {
		// The widest measured requirement landed 35 rows across 6 tables. Grouped,
		// that is six statements with a `WHERE row_id IN (…)` — one per table —
		// against thirty-five. The columns are reported rather than assumed, because
		// the engine owns the row's shape (ADR-0014) and the script does not.
		result := replay(t, "across-libraries.json")
		reads, _ := result["reads"].([]any)
		landings, _ := result["landings"].([]any)
		if len(reads) != 2 {
			t.Fatalf("two tables carry landings, so there are two reads: %v", reads)
		}
		total := 0
		for _, read := range reads {
			entry := read.(map[string]any)
			columns, _ := entry["columns"].([]any)
			if len(columns) == 0 || columns[0] != "title" {
				t.Fatalf("a read must carry the table's columns: %v", entry)
			}
			rows, _ := entry["rows"].([]any)
			total += len(rows)
			for _, row := range rows {
				position := row.(map[string]any)
				if position["row_id"] == nil || position["path"] == nil {
					t.Fatalf("a read row must say what to read: %v", position)
				}
			}
		}
		if total != len(landings) {
			t.Fatalf("every landing must appear exactly once in the reads: %d vs %d", total, len(landings))
		}
	})

	t.Run("the owner's words for a place are offered beside its description", func(t *testing.T) {
		// Aliases are short terms, not the synopsis: they ride along with the
		// description because they are exactly the vocabulary a layer decision needs,
		// and until this change nothing read them at all.
		result := replay(t, "across-libraries.json")
		found := false
		for _, entry := range result["evidence"].([]any) {
			record := entry.(map[string]any)
			if record["layer"] != "databases" {
				continue
			}
			for _, option := range record["options"].([]any) {
				offered := option.(map[string]any)
				if offered["name"] != "me" {
					continue
				}
				text, _ := offered["purpose"].(string)
				if !strings.Contains(text, "personal") || !strings.Contains(text, "私人事实") {
					t.Fatalf("an alias must reach the candidate text: %q", text)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("the me database was not offered: %v", result["evidence"])
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

// A Route whose `purpose` only repeats its `name` carries no description, and
// the walk used to hide that: `purpose or name` handed the name to jev as if it
// were a description, so "nobody wrote one" and "somebody wrote the name" looked
// identical in the pipeline — which is why 79% of the tree could rot unnoticed
// (docs/decisions.md「语义树的标签质量是可测量的检索损伤」). The fixture is
// two-internships with four descriptions taken away in the four ways they go
// missing; the recording keys are `intent || names`, so the answers still
// replay.
func TestJevTreeSaysWhenALayerHasNoUsableDescription(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the Skill's scripts cannot be exercised here")
	}
	root := repoRoot(t)
	command := exec.Command(python, filepath.Join(root, "skills", "memora", "scripts", "jev_tree.py"),
		"--replay", filepath.Join(root, "internal", "devgate", "testdata", "jev-tree", "purpose-repeats-name.json"))
	command.Dir = root
	command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(),
		"MEMORA_CLI=" + filepath.Join(t.TempDir(), "memora-that-does-not-exist")}
	output, err := command.Output()
	if err != nil {
		t.Fatalf("replay: %v\n%s", err, output)
	}
	result := map[string]any{}
	if err := json.Unmarshal(output, &result); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, output)
	}

	// The walk still lands: a missing description degrades the question, it does
	// not stop the descent.
	landings, _ := result["landings"].([]any)
	if len(landings) != 2 {
		t.Fatalf("landings = %v, want the two leaves", result["landings"])
	}

	layers := map[string][]string{}
	for _, entry := range result["evidence"].([]any) {
		record, _ := entry.(map[string]any)
		undescribed, present := record["undescribed"]
		if !present {
			continue
		}
		names := []string{}
		for _, name := range undescribed.([]any) {
			names = append(names, name.(string))
		}
		layers[record["layer"].(string)] = names
	}
	// Four ways a description goes missing, all four seen as missing: an exact
	// repeat, nothing written at all, a full-width repeat, and a repeat with
	// different case and padding.
	if got := strings.Join(layers["tables of me"], ","); got != "experiences,profile" {
		t.Fatalf("tables of me undescribed = %q, want the repeat and the blank", got)
	}
	if got := strings.Join(layers["me:internship"], ","); got != "ACME,GLOBEX" {
		t.Fatalf("me:internship undescribed = %q, want both leaves", got)
	}
	// The Database layer has real descriptions, so it must not be named: a
	// report that flagged everything would be as useless as one that flagged
	// nothing.
	if _, flagged := layers["databases"]; flagged {
		t.Fatalf("a described layer must not be reported: %v", layers)
	}

	// The layers are named in the answer itself, not only buried in evidence:
	// a caller reading the result sees which layers were chosen blind.
	named := []string{}
	for _, layer := range result["undescribed_at"].([]any) {
		named = append(named, layer.(string))
	}
	if strings.Join(named, ",") != "tables of me,me:internship" {
		t.Fatalf("undescribed_at = %v", result["undescribed_at"])
	}

	// And the name never stands in for the description: the option text the
	// decision was made from shows an empty purpose, not the name again.
	for _, entry := range result["evidence"].([]any) {
		record := entry.(map[string]any)
		for _, option := range record["options"].([]any) {
			offered := option.(map[string]any)
			name, purpose := offered["name"].(string), offered["purpose"].(string)
			if name == "experiences" || name == "profile" || name == "ACME" || name == "GLOBEX" {
				if purpose != "" {
					t.Fatalf("%q was offered with purpose %q: the name must not stand in for a description",
						name, purpose)
				}
			}
		}
	}
}
