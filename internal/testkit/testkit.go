package testkit

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var ErrIDsExhausted = errors.New("fake ID source exhausted")

type Sandbox struct {
	Root string
}

func NewSandbox(t testing.TB) Sandbox {
	t.Helper()
	return Sandbox{Root: t.TempDir()}
}

func (s Sandbox) Resolve(name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("sandbox path must be relative: %q", name)
	}
	path := filepath.Join(s.Root, filepath.Clean(name))
	if err := s.Validate(path); err != nil {
		return "", err
	}
	return path, nil
}

func (s Sandbox) Validate(path string) error {
	root, err := filepath.Abs(s.Root)
	if err != nil {
		return fmt.Errorf("resolve sandbox root: %w", err)
	}
	target, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve sandbox target: %w", err)
	}
	relative, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("compare sandbox path: %w", err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes sandbox %q", target, root)
	}

	resolvedRoot, err := resolveExistingPath(root)
	if err != nil {
		return fmt.Errorf("resolve sandbox root links: %w", err)
	}
	resolvedTarget, err := resolveExistingPath(target)
	if err != nil {
		return fmt.Errorf("resolve sandbox target links: %w", err)
	}
	resolvedRelative, err := filepath.Rel(resolvedRoot, resolvedTarget)
	if err != nil {
		return fmt.Errorf("compare resolved sandbox path: %w", err)
	}
	if resolvedRelative == ".." || strings.HasPrefix(resolvedRelative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes sandbox %q through a symbolic link", target, root)
	}
	return nil
}

func resolveExistingPath(path string) (string, error) {
	current := path
	var missing []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return filepath.Abs(resolved)
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

type FakeClock struct {
	mu  sync.RWMutex
	now time.Time
}

func NewFakeClock(now time.Time) *FakeClock {
	return &FakeClock{now: now}
}

func (c *FakeClock) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.now
}

func (c *FakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(duration)
}

type FakeIDs struct {
	mu     sync.Mutex
	values []string
	next   int
}

func NewFakeIDs(values ...string) *FakeIDs {
	copied := append([]string(nil), values...)
	return &FakeIDs{values: copied}
}

func (ids *FakeIDs) Next() (string, error) {
	ids.mu.Lock()
	defer ids.mu.Unlock()
	if ids.next >= len(ids.values) {
		return "", ErrIDsExhausted
	}
	value := ids.values[ids.next]
	ids.next++
	return value, nil
}

type faultRule struct {
	nth int
	err error
}

type Faults struct {
	mu    sync.Mutex
	rules map[string]faultRule
	hits  map[string]int
}

func NewFaults() *Faults {
	return &Faults{
		rules: make(map[string]faultRule),
		hits:  make(map[string]int),
	}
}

func (f *Faults) FailOn(name string, nth int, err error) {
	if name == "" || nth < 1 || err == nil {
		panic("testkit: invalid fault rule")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules[name] = faultRule{nth: nth, err: err}
}

func (f *Faults) Hit(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits[name]++
	rule, ok := f.rules[name]
	if ok && f.hits[name] == rule.nth {
		return rule.err
	}
	return nil
}

func (f *Faults) Hits(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[name]
}

func CompareGolden(path string, got []byte, update bool) error {
	if update {
		if err := os.WriteFile(path, got, 0o600); err != nil {
			return fmt.Errorf("update golden %q: %w", path, err)
		}
		return nil
	}
	want, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read golden %q: %w", path, err)
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("golden mismatch for %q: got %d bytes, want %d", path, len(got), len(want))
	}
	return nil
}

func ReadFixture(t testing.TB, name string) []byte {
	t.Helper()
	if filepath.IsAbs(name) || name == ".." || strings.HasPrefix(filepath.Clean(name), ".."+string(filepath.Separator)) {
		t.Fatalf("fixture path escapes testdata: %q", name)
	}
	_, caller, _, ok := runtime.Caller(1)
	if !ok {
		t.Fatal("resolve fixture caller")
	}
	path := filepath.Join(filepath.Dir(caller), "testdata", filepath.Clean(name))
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %q: %v", name, err)
	}
	return content
}
