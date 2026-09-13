package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/HW-Yue/Memora/internal/adminapi"
	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/mcpadapter"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/readquery"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const helpText = `Memora is an AI-maintained local personal database on SQLite.

Usage:
  memora <command> [options]

Commands:
  admin      Start a temporary local read-only Admin console
  daemon     Manage the local daemon (start | stop | status | run)
  doctor     Check database integrity and index health
  exec       Execute MSQL through the local daemon
  help       Show this help
  init       Initialize a local instance
  mcp        Serve MCP over newline-delimited stdio
  parse      Parse an MSQL request through the local daemon
  query      Query MSQL through the local daemon
  reindex    Rebuild lexical postings and the vector index
  version    Show build version

Vector search uses an OpenAI-compatible embeddings API:
  MEMORA_EMBEDDING_API_KEY (or OPENAI_API_KEY), MEMORA_EMBEDDING_BASE_URL,
  MEMORA_EMBEDDING_MODEL, MEMORA_EMBEDDING_DIMENSIONS

Run 'memora help' for usage.
`

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

type versionOutput struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

func Run(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	return RunWithDependencies(args, stdout, stderr, build, Dependencies{
		HomeDir: os.UserHomeDir,
		Stdin:   os.Stdin,
		UserID:  os.Getuid,
	})
}

type Dependencies struct {
	HomeDir     func() (string, error)
	LookupEnv   func(string) (string, bool)
	Stdin       io.Reader
	UserID      func() int
	Clock       instance.Clock
	IDs         instance.IDSource
	ExecuteMSQL func(
		context.Context,
		string,
		string,
		[]executor.StatementInput,
	) (result.Envelope, error)
	ServeAdmin  func(context.Context, adminapi.Config, func(adminapi.Descriptor) error) error
	OpenBrowser func(string) error
	Executable  func() (string, error)
}

func RunWithDependencies(args []string, stdout, stderr io.Writer, build BuildInfo, dependencies Dependencies) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, helpText)
	}

	switch args[0] {
	case "help", "-h", "--help":
		if len(args) != 1 {
			return usageError(stderr, "help does not accept arguments")
		}
		return writeText(stdout, stderr, helpText)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr, dependencies)
	case "admin":
		return runAdmin(args[1:], stdout, stderr, dependencies)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, dependencies)
	case "exec", "query":
		return runExecute(args[0], args[1:], stdout, stderr, dependencies)
	case "init":
		return runInit(args[1:], stdout, stderr, dependencies)
	case "mcp":
		return runMCP(args[1:], stdout, stderr, build, dependencies)
	case "reindex":
		return runReindex(args[1:], stdout, stderr, dependencies)
	case "parse":
		return runParse(args[1:], stdout, stderr, dependencies)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func runExecute(
	command string,
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var source, inputJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--input":
			if index+1 >= len(args) {
				return usageError(stderr, "--input requires a JSON object")
			}
			if inputJSON != "" {
				return usageError(stderr, "--input may only be specified once")
			}
			inputJSON = args[index+1]
			index++
		default:
			if source != "" {
				return usageError(stderr, command+" accepts exactly one MSQL source argument")
			}
			source = args[index]
		}
	}
	if source == "" {
		return usageError(stderr, command+" requires an MSQL source argument")
	}
	if command == "query" {
		if _, err := readquery.Validate(source); err != nil {
			return usageError(
				stderr,
				"query only accepts SHOW, DESCRIBE, SELECT, OPEN ROUTE, or read-only PLAN statements",
			)
		}
	}
	statements := []executor.StatementInput{}
	if inputJSON != "" {
		var input executor.StatementInput
		decoder := json.NewDecoder(bytes.NewBufferString(inputJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil ||
			decoder.Decode(&struct{}{}) != io.EOF {
			return usageError(stderr, "--input must be one strict StatementInput JSON object")
		}
		statements = append(statements, input)
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	envelope, err := execute(context.Background(), dataDir, source, statements)
	if err != nil {
		return commandError(stderr, command+" MSQL", err)
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return writeFailure(stderr, err)
	}
	if !envelope.OK {
		return ExitFailure
	}
	return ExitOK
}

func runAdmin(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var scopes []string
	noOpen := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--scope":
			if index+1 >= len(args) {
				return usageError(stderr, "--scope requires a Database name or ID")
			}
			scopes = append(scopes, args[index+1])
			index++
		case "--no-open":
			if noOpen {
				return usageError(stderr, "--no-open may only be specified once")
			}
			noOpen = true
		default:
			return usageError(stderr, fmt.Sprintf("unknown admin option: %q", args[index]))
		}
	}
	if len(scopes) != 0 {
		authorization := security.Authorization{
			Version:             security.AuthorizationVersion,
			Actor:               "user:admin",
			AuthorizedDatabases: scopes,
		}
		if err := authorization.Validate(); err != nil {
			return usageError(stderr, "admin scope is invalid")
		}
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	serve := dependencies.ServeAdmin
	if serve == nil {
		serve = serveAdmin
	}
	openBrowser := dependencies.OpenBrowser
	if openBrowser == nil {
		openBrowser = openSystemBrowser
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := serve(ctx, adminapi.Config{
		DataDir: dataDir,
		Scopes:  append([]string(nil), scopes...),
		Execute: execute,
	}, func(descriptor adminapi.Descriptor) error {
		if err := json.NewEncoder(stdout).Encode(descriptor); err != nil {
			return err
		}
		if noOpen {
			return nil
		}
		return openBrowser(descriptor.URL)
	})
	if err != nil {
		return commandError(stderr, "serve Admin API", err)
	}
	return ExitOK
}

func openSystemBrowser(target string) error {
	if err := osexec.Command("open", target).Run(); err != nil {
		return fmt.Errorf("open system browser: %w", err)
	}
	return nil
}

func serveAdmin(
	ctx context.Context,
	config adminapi.Config,
	ready func(adminapi.Descriptor) error,
) error {
	gateway, err := adminapi.Start(ctx, config)
	if err != nil {
		return err
	}
	if err := ready(gateway.Descriptor()); err != nil {
		closeErr := gateway.Close()
		waitErr := gateway.Wait()
		return errors.Join(err, closeErr, waitErr)
	}
	return gateway.Wait()
}

func runDoctor(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	report, err := daemon.Doctor(context.Background(), dataDir)
	if err != nil {
		return commandError(stderr, "inspect database integrity", err)
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return writeFailure(stderr, err)
	}
	if report.Status != "healthy" {
		return ExitFailure
	}
	return ExitOK
}

func runReindex(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if err := daemon.Reindex(context.Background(), dataDir); err != nil {
		return commandError(stderr, "reindex", err)
	}
	return writeText(stdout, stderr, "Memora reindex scheduled; vectors fill in the background\n")
}

func runParse(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var source string
	for index := 0; index < len(args); index++ {
		if args[index] == "--data-dir" {
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
			continue
		}
		if source != "" {
			return usageError(stderr, "parse accepts exactly one MSQL source argument")
		}
		source = args[index]
	}
	if source == "" {
		return usageError(stderr, "parse requires an MSQL source argument")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	response, err := daemon.Parse(context.Background(), dataDir, source)
	if err != nil {
		return commandError(stderr, "parse MSQL", err)
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		return writeFailure(stderr, err)
	}
	if !response.OK {
		return ExitFailure
	}
	return ExitOK
}

func runMCP(args []string, stdout, stderr io.Writer, build BuildInfo, dependencies Dependencies) int {
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	input := dependencies.Stdin
	if input == nil {
		input = os.Stdin
	}
	execute := dependencies.ExecuteMSQL
	server := mcpadapter.New(mcpadapter.Config{DataDir: dataDir, Version: build.Version, Execute: execute})
	if err := server.Serve(context.Background(), input, stdout); err != nil {
		return commandError(stderr, "serve MCP stdio", err)
	}
	return ExitOK
}

func runDaemon(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, "daemon requires start, run, status, ping, or stop")
	}
	action := args[0]
	dataDir, code := daemonDataDir(args[1:], stderr, dependencies)
	if code != ExitOK {
		return code
	}
	switch action {
	case "start":
		if _, err := instance.Read(dataDir); err != nil {
			return commandError(stderr, "open instance", err)
		}
		executable, err := os.Executable()
		if err != nil {
			return commandError(stderr, "resolve executable", err)
		}
		state, err := daemon.Start(context.Background(), executable, dataDir)
		if err != nil {
			return commandError(stderr, "start daemon", err)
		}
		return writeText(stdout, stderr, fmt.Sprintf("Memora daemon started with PID %d\n", state.PID))
	case "run":
		if _, err := instance.Read(dataDir); err != nil {
			return commandError(stderr, "open instance", err)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := daemon.Run(ctx, dataDir, nil); err != nil {
			return commandError(stderr, "run daemon", err)
		}
		return ExitOK
	case "status":
		state, err := daemon.Inspect(dataDir)
		if err != nil {
			return commandError(stderr, "inspect daemon", err)
		}
		if state.Running {
			return writeText(stdout, stderr, fmt.Sprintf("Memora daemon is running with PID %d\n", state.PID))
		}
		return writeText(stdout, stderr, "Memora daemon is stopped\n")
	case "ping":
		if err := daemon.Ping(context.Background(), dataDir); err != nil {
			return commandError(stderr, "ping daemon", err)
		}
		return writeText(stdout, stderr, "pong\n")
	case "stop":
		if err := daemon.Stop(context.Background(), dataDir); err != nil {
			return commandError(stderr, "stop daemon", err)
		}
		return writeText(stdout, stderr, "Memora daemon stopped\n")
	default:
		return usageError(stderr, fmt.Sprintf("unknown daemon action: %q", action))
	}
}

func daemonDataDir(args []string, stderr io.Writer, dependencies Dependencies) (string, int) {
	var dataDirOverride *string
	for index := 0; index < len(args); index++ {
		if args[index] != "--data-dir" {
			return "", usageError(stderr, fmt.Sprintf("unknown daemon option: %q", args[index]))
		}
		index++
		if index >= len(args) {
			return "", usageError(stderr, "--data-dir requires a path")
		}
		value := args[index]
		if !filepath.IsAbs(value) {
			return "", usageError(stderr, "--data-dir must be an absolute path")
		}
		dataDirOverride = &value
	}
	homeDir := dependencies.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return "", commandError(stderr, "resolve user home", err)
	}
	configFile := ""
	defaultConfig, err := config.DefaultFile(home)
	if err != nil {
		return "", commandError(stderr, "resolve config file", err)
	}
	if _, statErr := os.Stat(defaultConfig); statErr == nil {
		configFile = defaultConfig
	} else if !os.IsNotExist(statErr) {
		return "", commandError(stderr, "inspect config file", statErr)
	}
	loaded, err := config.Load(config.LoadOptions{
		ConfigFile: configFile,
		LookupEnv:  dependencies.LookupEnv,
		Overrides:  config.Overrides{DataDir: dataDirOverride},
	})
	if err != nil {
		return "", commandError(stderr, "load config", err)
	}
	locations, err := instance.DefaultLocations(home, loaded.InstanceName, loaded.DataDir)
	if err != nil {
		return "", commandError(stderr, "resolve daemon data directory", err)
	}
	return locations.DataDir, ExitOK
}

func runInit(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var instanceOverride *string
	var dataDirOverride *string
	var logLevelOverride *string
	var configFileOverride *string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			index++
			if index >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			value := args[index]
			dataDirOverride = &value
			if !filepath.IsAbs(value) {
				return usageError(stderr, "--data-dir must be an absolute path")
			}
		case "--instance":
			index++
			if index >= len(args) {
				return usageError(stderr, "--instance requires a name")
			}
			value := args[index]
			instanceOverride = &value
		case "--log-level":
			index++
			if index >= len(args) {
				return usageError(stderr, "--log-level requires a value")
			}
			value := args[index]
			logLevelOverride = &value
		case "--config":
			index++
			if index >= len(args) {
				return usageError(stderr, "--config requires a path")
			}
			value := args[index]
			if !filepath.IsAbs(value) {
				return usageError(stderr, "--config must be an absolute path")
			}
			configFileOverride = &value
		default:
			return usageError(stderr, fmt.Sprintf("unknown option for init: %q", args[index]))
		}
	}

	homeDir := dependencies.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return commandError(stderr, "resolve user home", err)
	}
	configFile := ""
	if configFileOverride != nil {
		configFile = *configFileOverride
	} else {
		defaultConfig, configErr := config.DefaultFile(home)
		if configErr != nil {
			return commandError(stderr, "resolve config file", configErr)
		}
		if _, statErr := os.Stat(defaultConfig); statErr == nil {
			configFile = defaultConfig
		} else if !os.IsNotExist(statErr) {
			return commandError(stderr, "inspect config file", statErr)
		}
	}
	loaded, err := config.Load(config.LoadOptions{
		ConfigFile: configFile,
		LookupEnv:  dependencies.LookupEnv,
		Overrides: config.Overrides{
			InstanceName: instanceOverride,
			DataDir:      dataDirOverride,
			LogLevel:     logLevelOverride,
		},
	})
	if err != nil {
		return commandError(stderr, "load config", err)
	}
	locations, err := instance.DefaultLocations(home, loaded.InstanceName, loaded.DataDir)
	if err != nil {
		return usageError(stderr, err.Error())
	}
	result, err := instance.Initialize(context.Background(), locations.DataDir, instance.Options{
		Clock: dependencies.Clock,
		IDs:   dependencies.IDs,
	})
	if err != nil {
		return commandError(stderr, "initialize instance", err)
	}
	if result.Created {
		return writeText(stdout, stderr, fmt.Sprintf(
			"Initialized Memora instance %s at %s\n",
			result.Metadata.InstanceID,
			locations.DataDir,
		))
	}
	return writeText(stdout, stderr, fmt.Sprintf(
		"Memora instance %s already initialized at %s\n",
		result.Metadata.InstanceID,
		locations.DataDir,
	))
}

func runVersion(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, fmt.Sprintf("memora %s (%s)\n", build.Version, build.Commit))
	}
	if len(args) != 1 || args[0] != "--json" {
		option := args[0]
		return usageError(stderr, fmt.Sprintf("unknown option for version: %q", option))
	}

	result := versionOutput{
		Name:    "memora",
		Version: build.Version,
		Commit:  build.Commit,
		BuiltAt: build.BuiltAt,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func usageError(stderr io.Writer, message string) int {
	if _, err := fmt.Fprintf(stderr, "memora: %s\n", message); err != nil {
		return ExitFailure
	}
	return ExitUsage
}

func commandError(stderr io.Writer, action string, err error) int {
	message := security.Redact(err.Error())
	if _, writeErr := fmt.Fprintf(stderr, "memora: %s: %s\n", action, message); writeErr != nil {
		return ExitFailure
	}
	return ExitFailure
}

func writeText(stdout, stderr io.Writer, value string) int {
	if _, err := io.WriteString(stdout, value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func writeFailure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "memora: write output: %v\n", err)
	return ExitFailure
}
