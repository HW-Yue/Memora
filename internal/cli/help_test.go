package cli

import (
	"bytes"
	"strings"
	"testing"
)

// A host's first move against an unfamiliar command is `--help`, and this CLI
// used to answer it with a parse error (`memora query --help` tried to parse
// `--help` as a statement) or with "unknown option". A fresh agent that probed
// before building a Mutation Plan learned nothing and then improvised. Every
// command now answers the conventional flag, and `help <command>` does the same.
func TestEveryCommandAnswersHelp(t *testing.T) {
	commands := []string{
		"query", "exec", "mutate", "schema", "parse", "doctor",
		"daemon", "admin", "init", "instance", "mcp", "version",
	}
	for _, command := range commands {
		for _, flag := range []string{"--help", "-h"} {
			stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
			code := RunWithDependencies(
				[]string{command, flag}, stdout, stderr, BuildInfo{}, Dependencies{})
			if code != ExitOK {
				t.Errorf("%s %s: exit %d, stderr %q", command, flag, code, stderr.String())
				continue
			}
			if !strings.Contains(stdout.String(), "memora "+command) {
				t.Errorf("%s %s: usage does not name the command: %q", command, flag, stdout.String())
			}
		}
	}
}

func TestHelpTakesAnOptionalCommand(t *testing.T) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	if code := RunWithDependencies([]string{"help", "mutate"}, stdout, stderr, BuildInfo{}, Dependencies{}); code != ExitOK {
		t.Fatalf("help mutate: exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "memora mutate") {
		t.Fatalf("help mutate printed: %q", stdout.String())
	}
	stdout, stderr = &bytes.Buffer{}, &bytes.Buffer{}
	if code := RunWithDependencies([]string{"help", "nonsense"}, stdout, stderr, BuildInfo{}, Dependencies{}); code != ExitUsage {
		t.Fatalf("help nonsense must be a usage error, exit %d", code)
	}
	if !strings.Contains(stderr.String(), "nonsense") {
		t.Fatalf("the refusal must name what it did not know: %q", stderr.String())
	}
}
