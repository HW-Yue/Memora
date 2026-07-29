package daemon

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketPathIsShortStableAndInstanceSpecific(t *testing.T) {
	t.Parallel()

	longDataDir := filepath.Join("/", strings.Repeat("long-directory-", 20), "instance")
	first, err := SocketPath(longDataDir)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	second, err := SocketPath(longDataDir)
	if err != nil {
		t.Fatalf("SocketPath() second error = %v", err)
	}
	other, err := SocketPath(longDataDir + "-other")
	if err != nil {
		t.Fatalf("SocketPath() other error = %v", err)
	}
	if first != second {
		t.Fatalf("SocketPath() is unstable: %q != %q", first, second)
	}
	if first == other {
		t.Fatalf("different instances share socket path %q", first)
	}
	if len(first) > maxUnixSocketPath {
		t.Fatalf("socket path length = %d, want <= %d: %q", len(first), maxUnixSocketPath, first)
	}
}

func TestListenSocketRejectsLiveSocket(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	listener, path, err := listenSocket(dataDir)
	if err != nil {
		t.Fatalf("listenSocket() error = %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(path)
	}()
	if _, _, err := listenSocket(dataDir); !errors.Is(err, ErrSocketInUse) {
		t.Fatalf("second listenSocket() error = %v, want %v", err, ErrSocketInUse)
	}
}

func TestListenSocketReplacesStaleSocket(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	path, err := SocketPath(dataDir)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	if err := secureRuntimeDirectory(filepath.Dir(path)); err != nil {
		t.Fatalf("secureRuntimeDirectory() error = %v", err)
	}
	stale, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	listener, actual, err := listenSocket(dataDir)
	if err != nil {
		t.Fatalf("listenSocket() error = %v", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(actual)
	}()
	connection, err := net.Dial("unix", actual)
	if err != nil {
		t.Fatalf("Dial() replacement error = %v", err)
	}
	_ = connection.Close()
}

func TestListenSocketPreservesUnexpectedFile(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	path, err := SocketPath(dataDir)
	if err != nil {
		t.Fatalf("SocketPath() error = %v", err)
	}
	if err := secureRuntimeDirectory(filepath.Dir(path)); err != nil {
		t.Fatalf("secureRuntimeDirectory() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("do not remove"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	defer os.Remove(path)
	if _, _, err := listenSocket(dataDir); err == nil {
		t.Fatal("listenSocket() replaced a non-socket file")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "do not remove" {
		t.Fatalf("unexpected file content = %q", content)
	}
}
