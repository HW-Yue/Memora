package daemon

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/HW-Yue/Memora/internal/ipc"
)

const maxUnixSocketPath = 103

var ErrSocketInUse = errors.New("daemon socket is already in use")

func SocketPath(dataDir string) (string, error) {
	if !filepath.IsAbs(dataDir) {
		return "", ErrPathNotAbsolute
	}
	digest := sha256.Sum256([]byte(filepath.Clean(dataDir)))
	name := fmt.Sprintf("%x.sock", digest[:12])
	for _, root := range []string{os.TempDir(), "/tmp"} {
		path := filepath.Join(root, "memora-"+strconv.Itoa(os.Getuid()), name)
		if len(path) <= maxUnixSocketPath {
			return path, nil
		}
	}
	return "", fmt.Errorf("no daemon socket path fits the %d-byte limit", maxUnixSocketPath)
}

func listenSocket(dataDir string) (net.Listener, string, error) {
	path, err := SocketPath(dataDir)
	if err != nil {
		return nil, "", err
	}
	if err := secureRuntimeDirectory(filepath.Dir(path)); err != nil {
		return nil, "", err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		if info, statErr := os.Lstat(path); statErr == nil && info.Mode()&os.ModeSocket == 0 {
			return nil, "", errors.New("daemon socket path exists and is not a socket")
		}
		connection, dialErr := net.DialTimeout("unix", path, 100*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, "", ErrSocketInUse
		}
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			return nil, "", fmt.Errorf("remove stale daemon socket: %w", removeErr)
		}
		listener, err = net.Listen("unix", path)
	}
	if err != nil {
		return nil, "", fmt.Errorf("listen on daemon socket: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, "", fmt.Errorf("secure daemon socket: %w", err)
	}
	return listener, path, nil
}

func secureRuntimeDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create daemon socket directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect daemon socket directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("daemon socket directory is not a real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("daemon socket directory is not owned by the current user")
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure daemon socket directory: %w", err)
	}
	return nil
}

func Ping(ctx context.Context, dataDir string) error {
	path, err := SocketPath(dataDir)
	if err != nil {
		return err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	var response struct {
		Message string `json:"message"`
	}
	if err := client.Call(ctx, "ping", nil, &response); err != nil {
		return err
	}
	if response.Message != "pong" {
		return fmt.Errorf("unexpected daemon ping response %q", response.Message)
	}
	return nil
}
