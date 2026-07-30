package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/nativemigration"
	"github.com/HW-Yue/Memora/internal/nativemutation"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"golang.org/x/sys/unix"
)

var (
	ErrAlreadyRunning  = errors.New("daemon is already running")
	ErrCorruptPID      = errors.New("daemon PID file is corrupt")
	ErrPathNotAbsolute = errors.New("daemon data directory is not absolute")
)

type Paths struct {
	LockFile string
	PIDFile  string
}

func RuntimePaths(dataDir string) (Paths, error) {
	if !filepath.IsAbs(dataDir) {
		return Paths{}, ErrPathNotAbsolute
	}
	runtimeDir := filepath.Join(dataDir, "system")
	return Paths{
		LockFile: filepath.Join(runtimeDir, "daemon.lock"),
		PIDFile:  filepath.Join(runtimeDir, "daemon.pid"),
	}, nil
}

type State struct {
	Running bool
	PID     int
}

type Lease struct {
	mu     sync.Mutex
	file   *os.File
	paths  Paths
	pid    int
	closed bool
}

func Acquire(dataDir string) (*Lease, error) {
	return acquire(dataDir, true)
}

func AcquireMaintenance(dataDir string) (*Lease, error) {
	return acquire(dataDir, false)
}

func acquire(dataDir string, daemonPID bool) (*Lease, error) {
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.LockFile), 0o700); err != nil {
		return nil, fmt.Errorf("create daemon runtime directory: %w", err)
	}
	file, err := os.OpenFile(paths.LockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("secure daemon lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("acquire daemon lock: %w", err)
	}
	lease := &Lease{file: file, paths: paths}
	if daemonPID {
		lease.pid = os.Getpid()
		if err := writePID(paths.PIDFile, lease.pid); err != nil {
			_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
			_ = file.Close()
			return nil, err
		}
	}
	return lease, nil
}

func Inspect(dataDir string) (State, error) {
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		return State{}, err
	}
	if err := os.MkdirAll(filepath.Dir(paths.LockFile), 0o700); err != nil {
		return State{}, fmt.Errorf("create daemon runtime directory: %w", err)
	}
	file, err := os.OpenFile(paths.LockFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return State{}, fmt.Errorf("open daemon lock: %w", err)
	}
	defer func() { _ = file.Close() }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
		_ = os.Remove(paths.PIDFile)
		_ = unix.Flock(int(file.Fd()), unix.LOCK_UN)
		return State{}, nil
	} else if !errors.Is(err, unix.EWOULDBLOCK) {
		return State{}, fmt.Errorf("inspect daemon lock: %w", err)
	}
	pid, err := readPID(paths.PIDFile)
	if err != nil {
		return State{}, err
	}
	return State{Running: true, PID: pid}, nil
}

func Run(ctx context.Context, dataDir string, ready chan<- State) error {
	lease, err := Acquire(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = lease.Close() }()
	listener, socketPath, err := listenSocket(dataDir)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	migration, err := nativemigration.OpenDefault(ctx, dataDir)
	if err != nil {
		return err
	}
	nativeFile := migration.File
	auxiliaryStore, err := nativekvstore.Open(filepath.Join(dataDir, "system", "auxiliary.memora"))
	if err != nil {
		_ = nativeFile.Close()
		return err
	}
	securityStore, err := nativekvstore.Open(filepath.Join(dataDir, "system", "security.memora"))
	if err != nil {
		_ = nativeFile.Close()
		_ = auxiliaryStore.Close()
		return err
	}
	dictionary := nativecatalog.NewService(nativecatalog.New(nativeFile), nativecatalog.ServiceOptions{})
	rowRepository := nativerow.New(nativeFile)
	routeRepository := nativerouter.New(nativeFile)
	configuration, err := nativeconfig.New(nativeFile)
	if err != nil {
		_ = nativeFile.Close()
		_ = auxiliaryStore.Close()
		_ = securityStore.Close()
		return err
	}
	rowService := nativerow.NewService(rowRepository, dictionary, nativerow.ServiceOptions{})
	rows := nativemutation.NewService(
		rowService, dictionary, rowRepository, routeRepository,
		nativemutation.New(nativeFile, rowRepository, routeRepository),
		configuration,
	)
	handler := newNativeDatabaseHandler(
		ctx, dictionary, rows, auxiliaryStore, security.New(securityStore, security.Options{}),
		func(context.Context) ([]byte, error) { return nativesnapshot.NewNative(nativeFile).Export() },
	)
	server := ipc.NewServer(handler)
	if ready != nil {
		select {
		case ready <- State{Running: true, PID: lease.pid}:
		case <-ctx.Done():
			_ = server.Close()
			_ = handler.Close()
			return errors.Join(nativeFile.Close(), auxiliaryStore.Close(), securityStore.Close())
		}
	}
	serveErr := server.Serve(ctx, listener)
	serverErr := server.Close()
	handlerErr := handler.Close()
	storeErr := nativeFile.Close()
	auxiliaryStoreErr := auxiliaryStore.Close()
	securityStoreErr := securityStore.Close()
	return errors.Join(serveErr, serverErr, handlerErr, storeErr, auxiliaryStoreErr, securityStoreErr)
}

func (lease *Lease) Close() error {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	if lease.pid > 0 {
		current, err := readPID(lease.paths.PIDFile)
		if err == nil && current == lease.pid {
			_ = os.Remove(lease.paths.PIDFile)
		}
	}
	unlockErr := unix.Flock(int(lease.file.Fd()), unix.LOCK_UN)
	closeErr := lease.file.Close()
	if unlockErr != nil {
		return fmt.Errorf("release daemon lock: %w", unlockErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close daemon lock: %w", closeErr)
	}
	return nil
}

func writePID(path string, pid int) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".daemon.pid-")
	if err != nil {
		return fmt.Errorf("create temporary daemon PID: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure temporary daemon PID: %w", err)
	}
	if _, err := fmt.Fprintf(temporary, "%d\n", pid); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write temporary daemon PID: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync temporary daemon PID: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary daemon PID: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish daemon PID: %w", err)
	}
	return nil
}

func readPID(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, ErrCorruptPID
		}
		return 0, fmt.Errorf("read daemon PID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid < 1 {
		return 0, ErrCorruptPID
	}
	return pid, nil
}
