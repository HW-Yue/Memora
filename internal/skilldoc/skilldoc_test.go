// Package skilldoc keeps the Skill's own examples honest.
//
// The Skill is the first thing a fresh agent reads, and its examples are copied
// verbatim: when one was wrong (a `RECALL` example that passed a `:limit`
// parameter while writing a literal `LIMIT 5`), the engine refused it and the
// agent spent two calls learning something the document was supposed to teach.
// The engine cannot prevent that — the document is the error.
//
// So every `memora` command in SKILL.md's shell examples is checked here, and the
// check is a coverage one: a command that does not fit a shape this test knows
// fails, instead of being silently skipped. What it verifies is static — the MSQL
// parses, and each statement binds exactly the named parameters it is given
// (nothing unused, nothing missing), with array elements bound per statement
// index. It does not execute them: most examples name tables and Rows that exist
// only in the reader's instance.
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

// Every markdown file the Skill carries: the spine and the references it points
// at. An example that moves into a reference must stay checked, or the move
// trades a verified example for an unverified one.
const skillRoot = "../../skills/memora"

var (
	// Every fenced block, whatever its language tag: an example that lands in a
	// bare fence is still an example, and scanning only ```sh would let a 33rd
	// command escape the check by being fenced differently.
	fencePattern     = regexp.MustCompile("(?s)```[a-zA-Z]*\n(.*?)```")
	parameterPattern = regexp.MustCompile(`:([A-Za-z_][A-Za-z0-9_]*)`)
)

type command struct {
	text  string
	lines []string
}

// commands returns every `memora` command in the shell examples. A command
// starts at a line whose trimmed text begins with `memora ` and continues while
// its single quotes are unbalanced, which is how the multi-line `--plan` JSON
// examples stay in one piece.
func skillFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(skillRoot, "*.md"))
	if err != nil {
		t.Fatalf("list the Skill: %v", err)
	}
	references, err := filepath.Glob(filepath.Join(skillRoot, "references", "*.md"))
	if err != nil {
		t.Fatalf("list the Skill references: %v", err)
	}
	files = append(files, references...)
	if len(files) < 5 {
		t.Fatalf("only %d Skill files found; the extractor is wrong", len(files))
	}
	return files
}

func commands(t *testing.T) []command {
	t.Helper()
	found := []command{}
	content := []byte{}
	for _, file := range skillFiles(t) {
		body, err := os.ReadFile(filepath.Clean(file))
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		content = append(content, body...)
		content = append(content, '\n')
	}
	for _, fence := range fencePattern.FindAllStringSubmatch(string(content), -1) {
		current := []string{}
		for _, line := range strings.Split(fence[1], "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "memora ") {
				if len(current) > 0 {
					found = append(found, command{text: strings.Join(current, "\n"), lines: current})
				}
				current = []string{line}
				continue
			}
			if len(current) > 0 {
				current = append(current, line)
			}
		}
		if len(current) > 0 {
			found = append(found, command{text: strings.Join(current, "\n"), lines: current})
		}
	}
	if len(found) < 30 {
		t.Fatalf("only %d memora commands found; the extractor is wrong", len(found))
	}
	return found
}

// afterFlag returns the single-quoted blob that follows flag, which is how the
// examples carry their JSON: the JSON itself uses double quotes, so the next
// single quote closes it.
func afterFlag(text, flag string) string {
	start := strings.Index(text, flag)
	if start < 0 {
		return ""
	}
	rest := text[start+len(flag):]
	end := strings.IndexByte(rest, '\'')
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// sourceArg returns the last double-quoted string in the command — the MSQL
// source. It is taken from the end because the JSON before it is full of double
// quotes, and the source itself may contain single-quoted literals.
func sourceArg(text string) string {
	last := strings.LastIndexByte(text, '"')
	if last < 0 {
		return ""
	}
	first := strings.LastIndexByte(text[:last], '"')
	if first < 0 {
		return ""
	}
	return text[first+1 : last]
}

type inputShape struct {
	named    map[string]any
	elements []map[string]any
	array    bool
}

func decodeNamed(t *testing.T, payload string, line string) map[string]any {
	t.Helper()
	decoded := struct {
		Parameters struct {
			Named map[string]any `json:"named"`
		} `json:"parameters"`
	}{}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Errorf("input is not decodable JSON: %v\n%s", err, line)
		return nil
	}
	return decoded.Parameters.Named
}

func parseInput(t *testing.T, raw string, line string) inputShape {
	t.Helper()
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "[") {
		return inputShape{named: decodeNamed(t, trimmed, line)}
	}
	elements := []json.RawMessage{}
	if err := json.Unmarshal([]byte(trimmed), &elements); err != nil {
		t.Errorf("input array is not decodable JSON: %v\n%s", err, line)
		return inputShape{array: true}
	}
	named := make([]map[string]any, 0, len(elements))
	for _, element := range elements {
		named = append(named, decodeNamed(t, string(element), line))
	}
	return inputShape{elements: named, array: true}
}

func bindingsMatch(t *testing.T, named map[string]any, source string, line string) {
	t.Helper()
	used := map[string]bool{}
	for _, match := range parameterPattern.FindAllStringSubmatch(source, -1) {
		used[match[1]] = true
	}
	for name := range named {
		if !used[name] {
			t.Errorf("example passes :%s but its statement never uses it:\n%s", name, line)
		}
	}
	for name := range used {
		if _, ok := named[name]; !ok {
			t.Errorf("example uses :%s but its input does not pass it:\n%s", name, line)
		}
	}
}

func statements(source string) []string {
	parts := []string{}
	for _, part := range strings.Split(source, ";") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return parts
}

// TestEverySkillCommandIsACoveredShape is the coverage guard: a new example in a
// shape this test does not understand fails here rather than passing unnoticed.
func TestEverySkillCommandIsACoveredShape(t *testing.T) {
	t.Parallel()
	shapes := map[string]int{}
	for _, item := range commands(t) {
		words := strings.Fields(item.lines[0])
		head := ""
		if len(words) > 1 {
			head = words[1]
		}
		switch {
		case strings.Contains(item.text, "--input '") && strings.Contains(item.text, "..."):
			// A sketch: the JSON is visibly elided, so there is nothing to bind. The
			// ellipsis is the licence — an example may elide, but only where the
			// reader can see that it did.
			shapes["sketch"]++
		case strings.Contains(item.text, "--input '"):
			if sourceArg(item.text) == "" {
				t.Errorf("command has an input but no quoted statement:\n%s", item.lines[0])
				continue
			}
			shapes["input"]++
		case strings.Contains(item.text, "--plan '"):
			shapes["plan"]++
		case sourceArg(item.text) != "":
			shapes["bare-statement"]++
		case head == "doctor" || head == "version" || head == "help" ||
			head == "init" || head == "daemon" || head == "admin":
			// Operational commands: no MSQL source to bind, only a subcommand that
			// the CLI has to know. `doctor` and `version` are the spine's own.
			shapes["shell"]++
		default:
			t.Errorf("command fits no known shape and is not checked:\n%s", item.lines[0])
		}
	}
	for _, required := range []string{"input", "sketch", "plan", "bare-statement", "shell"} {
		if shapes[required] == 0 {
			t.Errorf("no example of shape %q was found; the extractor or the Skill changed", required)
		}
	}
}

// TestEverySkillStatementParsesAndBindsItsOwnParameters is the RED that the
// broken RECALL example would have failed.
func TestEverySkillStatementParsesAndBindsItsOwnParameters(t *testing.T) {
	t.Parallel()
	for _, item := range commands(t) {
		line := item.lines[0]
		switch {
		case strings.Contains(item.text, "--input '") && strings.Contains(item.text, "..."):
			// Visibly elided (the coverage test is where that is licensed).
			continue
		case strings.Contains(item.text, "--input '"):
			shape := parseInput(t, afterFlag(item.text, "--input '"), line)
			list := statements(sourceArg(item.text))
			if shape.array && len(shape.elements) != 0 && len(shape.elements) != len(list) {
				t.Errorf("input array has %d elements for %d statements:\n%s",
					len(shape.elements), len(list), line)
				continue
			}
			for index, statement := range list {
				if _, err := parser.Parse(statement); err != nil {
					t.Errorf("example does not parse: %v\n%s", err, statement)
					continue
				}
				named := shape.named
				if shape.array && index < len(shape.elements) {
					named = shape.elements[index]
				}
				bindingsMatch(t, named, statement, line)
			}
		case strings.Contains(item.text, "--plan '"):
			decoded := map[string]any{}
			if err := json.Unmarshal([]byte(afterFlag(item.text, "--plan '")), &decoded); err != nil {
				t.Errorf("plan is not decodable JSON: %v\n%s", err, line)
				continue
			}
			if version, _ := decoded["version"].(string); version == "" {
				t.Errorf("plan has no version:\n%s", line)
			}
		case sourceArg(item.text) != "":
			for _, statement := range statements(sourceArg(item.text)) {
				if _, err := parser.Parse(statement); err != nil {
					t.Errorf("example does not parse: %v\n%s", err, statement)
				}
			}
		}
	}
}
