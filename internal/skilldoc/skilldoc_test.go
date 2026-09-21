// Package skilldoc keeps the Skill's own examples honest. The Skill is the first
// thing a fresh agent reads, and its examples are copied verbatim: when one of
// them was wrong (a `RECALL` example that passed a `:limit` parameter while
// writing a literal `LIMIT 5`), the engine refused it and the agent lost two
// calls learning something the document was supposed to teach. The engine's
// errors cannot prevent that — the document is the error. So every shell example
// in SKILL.md is executed here against the parser and its own parameters.
package skilldoc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

const skillPath = "../../skills/memora/SKILL.md"

var (
	fencePattern     = regexp.MustCompile("(?s)```sh\n(.*?)```")
	inputPattern     = regexp.MustCompile(`--input\s+'(\{.*?\})'\s+"([^"]*)"`)
	parameterPattern = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
)

type example struct {
	input   string
	source  string
	rawLine string
}

func examples(t *testing.T) []example {
	t.Helper()
	content, err := os.ReadFile(filepath.Clean(skillPath))
	if err != nil {
		t.Fatalf("read the Skill: %v", err)
	}
	found := []example{}
	for _, fence := range fencePattern.FindAllStringSubmatch(string(content), -1) {
		for _, line := range strings.Split(fence[1], "\n") {
			match := inputPattern.FindStringSubmatch(line)
			if match == nil {
				continue
			}
			found = append(found, example{input: match[1], source: match[2], rawLine: line})
		}
	}
	if len(found) < 10 {
		t.Fatalf("only %d examples found in the Skill; the extractor is wrong", len(found))
	}
	return found
}

// TestEverySkillExampleBindsItsOwnParameters is the RED that the broken RECALL
// example would have failed: a named parameter the statement never mentions is a
// validation error at execution time ("named parameter :limit is unused"), and a
// parameter the statement mentions but the input omits is a missing-parameter
// error. Both are the document's fault, and both are invisible until an agent
// runs the example.
func TestEverySkillExampleBindsItsOwnParameters(t *testing.T) {
	t.Parallel()
	for _, item := range examples(t) {
		named := map[string]any{}
		decoded := struct {
			Parameters struct {
				Named map[string]any `json:"named"`
			} `json:"parameters"`
		}{}
		if err := json.Unmarshal([]byte(item.input), &decoded); err != nil {
			t.Errorf("example is not decodable JSON input: %v\n%s", err, item.rawLine)
			continue
		}
		for name := range decoded.Parameters.Named {
			named[name] = nil
		}
		used := map[string]bool{}
		for _, match := range parameterPattern.FindAllStringSubmatch(item.source, -1) {
			used[match[1]] = true
		}
		for name := range named {
			if !used[name] {
				t.Errorf("example passes :%s but the statement never uses it:\n%s", name, item.rawLine)
			}
		}
		for name := range used {
			if _, ok := named[name]; !ok {
				t.Errorf("example uses :%s but the input does not pass it:\n%s", name, item.rawLine)
			}
		}
	}
}

// TestEverySkillStatementParses keeps the examples inside the language the
// engine actually speaks: an example the parser rejects teaches a shape that
// does not exist.
func TestEverySkillStatementParses(t *testing.T) {
	t.Parallel()
	for _, item := range examples(t) {
		for _, statement := range strings.Split(item.source, ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			if _, err := parser.Parse(statement); err != nil {
				t.Errorf("example does not parse: %v\n%s", err, statement)
			}
		}
	}
}
