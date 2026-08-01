package launchagent_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/launchagent"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestInstallPublishesDeterministicUserLaunchAgent(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	runner := &fakeRunner{}
	receipt, err := launchagent.New(runner).Install(context.Background(), config)
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	encoded, err := os.ReadFile(receipt.PlistPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, want := range []string{
		config.Executable, "<string>daemon</string>", "<string>run</string>", config.DataDir,
		"<key>SuccessfulExit</key>", "<false/>", "<string>Background</string>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist does not contain %q:\n%s", want, text)
		}
	}
	wantCalls := [][]string{
		{"bootout", receipt.ServiceTarget},
		{"bootstrap", receipt.DomainTarget, receipt.PlistPath},
	}
	if !reflect.DeepEqual(runner.calls, wantCalls) {
		t.Fatalf("calls = %#v, want %#v", runner.calls, wantCalls)
	}
	info, _ := os.Stat(receipt.PlistPath)
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("plist mode = %o", info.Mode().Perm())
	}
	if runtime.GOOS == "darwin" {
		if output, err := exec.Command("plutil", "-lint", receipt.PlistPath).CombinedOutput(); err != nil {
			t.Fatalf("plutil: %v: %s", err, output)
		}
	}
}

func TestDescribeSeparatesInstances(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	first, _ := launchagent.Describe(config)
	config.DataDir += "-other"
	second, _ := launchagent.Describe(config)
	if first.Label == second.Label || first.ServiceTarget == second.ServiceTarget {
		t.Fatalf("descriptors collide: %#v, %#v", first, second)
	}
}

func TestInstallBootstrapFailureDoesNotLeaveNewPlist(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	runner := &fakeRunner{failAction: "bootstrap"}
	_, err := launchagent.New(runner).Install(context.Background(), config)
	if err == nil {
		t.Fatal("Install() error = nil")
	}
	descriptor, _ := launchagent.Describe(config)
	if _, statErr := os.Lstat(descriptor.PlistPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("plist remains after failure: %v", statErr)
	}
}

func TestUninstallDoesNotRequireExistingExecutableOrInstance(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	runner := &fakeRunner{}
	manager := launchagent.New(runner)
	receipt, err := manager.Install(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(config.Executable); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(config.DataDir, config.DataDir+"-gone"); err != nil {
		t.Fatal(err)
	}
	removed, err := manager.Uninstall(context.Background(), config)
	if err != nil {
		t.Fatalf("Uninstall() error = %v", err)
	}
	if removed.Status != "uninstalled" || removed.PlistPath != receipt.PlistPath {
		t.Fatalf("receipt = %#v", removed)
	}
	if _, statErr := os.Lstat(receipt.PlistPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("plist remains: %v", statErr)
	}
}

func TestReinstallFailureRestoresPreviousPlist(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	descriptor, _ := launchagent.Describe(config)
	if err := os.MkdirAll(filepath.Dir(descriptor.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	old := []byte("previous launch agent")
	if err := os.WriteFile(descriptor.PlistPath, old, 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{failAction: "bootstrap", failOnce: true}
	if _, err := launchagent.New(runner).Install(context.Background(), config); err == nil {
		t.Fatal("Install() error = nil")
	}
	restored, err := os.ReadFile(descriptor.PlistPath)
	if err != nil || string(restored) != string(old) {
		t.Fatalf("restored = %q, error = %v", restored, err)
	}
	info, _ := os.Stat(descriptor.PlistPath)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("restored mode = %o", info.Mode().Perm())
	}
}

func TestInstallRejectsExistingPlistSymlinkBeforeLaunchctl(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	descriptor, _ := launchagent.Describe(config)
	if err := os.MkdirAll(filepath.Dir(descriptor.PlistPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(config.Executable, descriptor.PlistPath); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	if _, err := launchagent.New(runner).Install(context.Background(), config); err == nil || len(runner.calls) != 0 {
		t.Fatalf("Install() error = %v, calls = %#v", err, runner.calls)
	}
}

func TestStatusTreatsKnownNotLoadedResultAsStopped(t *testing.T) {
	t.Parallel()

	config := fixture(t)
	runner := &fakeRunner{failAction: "print", failOutput: "Could not find service"}
	receipt, err := launchagent.New(runner).Status(context.Background(), config)
	if err != nil {
		t.Fatalf("Status() error = %v", err)
	}
	if receipt.Installed || receipt.Loaded {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func fixture(t *testing.T) launchagent.Config {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dataDir := filepath.Join(root, "instance")
	executable := filepath.Join(root, "bin", "memora")
	if err := os.MkdirAll(filepath.Dir(executable), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(executable, []byte("binary"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := instance.Initialize(context.Background(), dataDir, instance.Options{
		IDs: testkit.NewFakeIDs("018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"),
	}); err != nil {
		t.Fatal(err)
	}
	return launchagent.Config{HomeDir: home, DataDir: dataDir, Executable: executable, UID: 501}
}

type fakeRunner struct {
	calls      [][]string
	failAction string
	failOutput string
	failOnce   bool
	failed     bool
}

func (runner *fakeRunner) Run(_ context.Context, arguments ...string) ([]byte, error) {
	runner.calls = append(runner.calls, append([]string(nil), arguments...))
	if len(arguments) > 0 && arguments[0] == runner.failAction && (!runner.failOnce || !runner.failed) {
		runner.failed = true
		output := runner.failOutput
		if output == "" {
			output = "fault"
		}
		return []byte(output), errors.New("launchctl fault")
	}
	return nil, nil
}
