package launchagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/instance"
)

const ReceiptVersion = "memora.launch-agent-receipt/v1"

type Config struct {
	HomeDir    string
	DataDir    string
	Executable string
	UID        int
}

type Descriptor struct {
	Label         string
	DomainTarget  string
	ServiceTarget string
	PlistPath     string
	StdoutPath    string
	StderrPath    string
}

type Receipt struct {
	Version       string `json:"version"`
	Status        string `json:"status"`
	Label         string `json:"label"`
	DomainTarget  string `json:"domain_target"`
	ServiceTarget string `json:"service_target"`
	PlistPath     string `json:"plist_path"`
	Installed     bool   `json:"installed"`
	Loaded        bool   `json:"loaded"`
}

type Runner interface {
	Run(context.Context, ...string) ([]byte, error)
}

type CommandRunner struct{}

func (CommandRunner) Run(ctx context.Context, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "launchctl", arguments...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("launchctl %s: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

type Manager struct{ runner Runner }

func New(runner Runner) *Manager {
	if runner == nil {
		runner = CommandRunner{}
	}
	return &Manager{runner: runner}
}

func Manage(ctx context.Context, action string, config Config) (Receipt, error) {
	manager := New(nil)
	switch action {
	case "install":
		return manager.Install(ctx, config)
	case "status":
		return manager.Status(ctx, config)
	case "uninstall":
		return manager.Uninstall(ctx, config)
	default:
		return Receipt{}, fmt.Errorf("unknown launch agent action %q", action)
	}
}

func Describe(config Config) (Descriptor, error) {
	if !validPath(config.HomeDir) || !validPath(config.DataDir) || config.UID < 0 {
		return Descriptor{}, errors.New("launch agent identity paths must be absolute and normalized")
	}
	digest := sha256.Sum256([]byte(config.DataDir))
	label := fmt.Sprintf("io.memora.daemon.%x", digest[:8])
	domain := "gui/" + strconv.Itoa(config.UID)
	logRoot := filepath.Join(config.HomeDir, "Library", "Logs", "Memora")
	return Descriptor{
		Label: label, DomainTarget: domain, ServiceTarget: domain + "/" + label,
		PlistPath:  filepath.Join(config.HomeDir, "Library", "LaunchAgents", label+".plist"),
		StdoutPath: filepath.Join(logRoot, label+".stdout.log"),
		StderrPath: filepath.Join(logRoot, label+".stderr.log"),
	}, nil
}

func (manager *Manager) Install(ctx context.Context, config Config) (Receipt, error) {
	descriptor, err := Describe(config)
	if err != nil {
		return Receipt{}, err
	}
	if err := validateInstall(config); err != nil {
		return Receipt{}, err
	}
	if err := secureDirectory(filepath.Dir(descriptor.PlistPath), 0o700); err != nil {
		return Receipt{}, err
	}
	if err := secureDirectory(filepath.Dir(descriptor.StdoutPath), 0o700); err != nil {
		return Receipt{}, err
	}
	old, oldMode, existed, err := readExisting(descriptor.PlistPath)
	if err != nil {
		return Receipt{}, err
	}
	if output, bootoutErr := manager.runner.Run(ctx, "bootout", descriptor.ServiceTarget); bootoutErr != nil && !notLoaded(output) {
		return Receipt{}, bootoutErr
	}
	encoded := render(config, descriptor)
	if err := writeAtomic(descriptor.PlistPath, encoded, 0o644); err != nil {
		return Receipt{}, err
	}
	if _, err := manager.runner.Run(ctx, "bootstrap", descriptor.DomainTarget, descriptor.PlistPath); err != nil {
		rollbackErr := rollbackFile(descriptor.PlistPath, old, oldMode, existed)
		if rollbackErr == nil && existed {
			_, rollbackErr = manager.runner.Run(context.WithoutCancel(ctx), "bootstrap", descriptor.DomainTarget, descriptor.PlistPath)
		}
		return Receipt{}, errors.Join(err, rollbackErr)
	}
	return receipt("installed", descriptor, true, true), nil
}

func (manager *Manager) Status(ctx context.Context, config Config) (Receipt, error) {
	descriptor, err := Describe(config)
	if err != nil {
		return Receipt{}, err
	}
	installed := false
	if info, statErr := os.Lstat(descriptor.PlistPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return Receipt{}, errors.New("launch agent plist is not a regular file")
		}
		installed = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Receipt{}, statErr
	}
	loaded := false
	if output, printErr := manager.runner.Run(ctx, "print", descriptor.ServiceTarget); printErr == nil {
		loaded = true
	} else if !notLoaded(output) {
		return Receipt{}, printErr
	}
	return receipt("status", descriptor, installed, loaded), nil
}

func (manager *Manager) Uninstall(ctx context.Context, config Config) (Receipt, error) {
	descriptor, err := Describe(config)
	if err != nil {
		return Receipt{}, err
	}
	if output, bootoutErr := manager.runner.Run(ctx, "bootout", descriptor.ServiceTarget); bootoutErr != nil && !notLoaded(output) {
		return Receipt{}, bootoutErr
	}
	if info, statErr := os.Lstat(descriptor.PlistPath); statErr == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return Receipt{}, errors.New("launch agent plist is not a regular file")
		}
		if err := os.Remove(descriptor.PlistPath); err != nil {
			return Receipt{}, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return Receipt{}, statErr
	}
	return receipt("uninstalled", descriptor, false, false), nil
}

func receipt(status string, descriptor Descriptor, installed, loaded bool) Receipt {
	return Receipt{Version: ReceiptVersion, Status: status, Label: descriptor.Label,
		DomainTarget: descriptor.DomainTarget, ServiceTarget: descriptor.ServiceTarget,
		PlistPath: descriptor.PlistPath, Installed: installed, Loaded: loaded}
}

func validateInstall(config Config) error {
	if !validPath(config.Executable) {
		return errors.New("launch agent executable must be absolute and normalized")
	}
	info, err := os.Lstat(config.Executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return errors.New("launch agent executable is not an executable regular file")
	}
	if _, err := instance.Read(config.DataDir); err != nil {
		return fmt.Errorf("read launch agent Instance: %w", err)
	}
	return nil
}

func render(config Config, descriptor Descriptor) []byte {
	var output bytes.Buffer
	output.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n")
	output.WriteString("<!DOCTYPE plist PUBLIC \"-//Apple//DTD PLIST 1.0//EN\" \"http://www.apple.com/DTDs/PropertyList-1.0.dtd\">\n")
	output.WriteString("<plist version=\"1.0\">\n<dict>\n")
	writeKeyString(&output, "Label", descriptor.Label)
	writeKeyString(&output, "Program", config.Executable)
	output.WriteString("  <key>ProgramArguments</key>\n  <array>\n")
	for _, argument := range []string{config.Executable, "daemon", "run", "--data-dir", config.DataDir} {
		output.WriteString("    <string>")
		escape(&output, argument)
		output.WriteString("</string>\n")
	}
	output.WriteString("  </array>\n")
	output.WriteString("  <key>KeepAlive</key>\n  <dict>\n    <key>SuccessfulExit</key>\n    <false/>\n  </dict>\n")
	writeKeyString(&output, "ProcessType", "Background")
	output.WriteString("  <key>ThrottleInterval</key>\n  <integer>10</integer>\n")
	output.WriteString("  <key>Umask</key>\n  <integer>63</integer>\n")
	writeKeyString(&output, "StandardOutPath", descriptor.StdoutPath)
	writeKeyString(&output, "StandardErrorPath", descriptor.StderrPath)
	output.WriteString("</dict>\n</plist>\n")
	return output.Bytes()
}

func writeKeyString(output *bytes.Buffer, key, value string) {
	output.WriteString("  <key>")
	escape(output, key)
	output.WriteString("</key>\n  <string>")
	escape(output, value)
	output.WriteString("</string>\n")
}

func escape(output io.Writer, value string) {
	for _, character := range value {
		switch character {
		case '&':
			_, _ = io.WriteString(output, "&amp;")
		case '<':
			_, _ = io.WriteString(output, "&lt;")
		case '>':
			_, _ = io.WriteString(output, "&gt;")
		case '"':
			_, _ = io.WriteString(output, "&quot;")
		case '\'':
			_, _ = io.WriteString(output, "&apos;")
		default:
			_, _ = io.WriteString(output, string(character))
		}
	}
}

func secureDirectory(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("launch agent directory is not a real directory")
	}
	return os.Chmod(path, mode)
}

func readExisting(path string) ([]byte, os.FileMode, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, 0, false, errors.New("launch agent plist is not a regular file")
	}
	encoded, err := os.ReadFile(path)
	return encoded, info.Mode().Perm(), true, err
}

func writeAtomic(path string, encoded []byte, mode os.FileMode) error {
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return errors.New("launch agent plist is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".memora-launch-agent-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func rollbackFile(path string, old []byte, mode os.FileMode, existed bool) error {
	if existed {
		return writeAtomic(path, old, mode)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func validPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func notLoaded(output []byte) bool {
	message := strings.ToLower(string(output))
	return strings.Contains(message, "could not find service") || strings.Contains(message, "no such process") ||
		strings.Contains(message, "service not found")
}
