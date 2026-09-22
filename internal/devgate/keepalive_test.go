package devgate

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A walk's provider connection has to be reused. A fresh TLS handshake measured
// 696-804 ms per decision against 244-523 ms on a reused connection, and a walk
// makes one decision per layer that has more than one child — so the reuse is
// most of the time a walk takes. It is also the kind of behaviour that stops
// happening silently: a stray close, or a request the server cannot keep a
// connection open for, leaves every functional test passing while the walk gets
// twice as slow. The check runs a local HTTP/1.1 provider and counts the
// connections it accepted, so the assertion is about the wire, not about intent.
func TestJevKeepsOneProviderConnectionForTheWalk(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH; the Skill's scripts cannot be exercised here")
	}
	root := repoRoot(t)
	command := exec.Command(python,
		filepath.Join(root, "internal", "devgate", "testdata", "jev", "keepalive_check.py"),
		filepath.Join(root, "skills", "memora", "scripts", "jev_select.py"))
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("the provider connection was not reused: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "connections: 1") {
		t.Fatalf("the check did not report one connection:\n%s", output)
	}
}
