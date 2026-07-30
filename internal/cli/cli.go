package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillschema"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const helpText = `Memora is an AI-maintained local personal database.

Usage:
  memora <command> [options]

Commands:
  daemon     Manage the local daemon
  doctor     Verify logical database integrity
  exec       Execute MSQL through the local daemon
  help       Show this help
  init       Initialize a local instance
  mutate     Execute a validated Mutation Plan
  parse      Parse an MSQL request through the local daemon
  query      Query MSQL through the local daemon
  schema     Execute a validated Schema Plan
  version    Show build version

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
	})
}

type Dependencies struct {
	HomeDir     func() (string, error)
	LookupEnv   func(string) (string, bool)
	Clock       instance.Clock
	IDs         instance.IDSource
	ExecuteMSQL func(
		context.Context,
		string,
		string,
		[]executor.StatementInput,
	) (result.Envelope, error)
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
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, dependencies)
	case "exec", "query":
		return runExecute(args[0], args[1:], stdout, stderr, dependencies)
	case "init":
		return runInit(args[1:], stdout, stderr, dependencies)
	case "mutate":
		return runMutate(args[1:], stdout, stderr, dependencies)
	case "parse":
		return runParse(args[1:], stdout, stderr, dependencies)
	case "schema":
		return runSchema(args[1:], stdout, stderr, dependencies)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
}

func runSchema(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var planJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--plan":
			if index+1 >= len(args) {
				return usageError(stderr, "--plan requires a JSON object")
			}
			if planJSON != "" {
				return usageError(stderr, "--plan may only be specified once")
			}
			planJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown schema option: %q", args[index]))
		}
	}
	if planJSON == "" {
		return usageError(stderr, "schema requires --plan JSON")
	}
	var plan skillschema.Plan
	decoder := json.NewDecoder(bytes.NewBufferString(planJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--plan must be one strict Schema Plan JSON object")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	tool := skillschema.ToolFunc(func(ctx context.Context, call skillschema.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements)
	})
	report, err := skillschema.New(tool).Run(context.Background(), plan)
	if err != nil {
		if report.Receipt.Version != "" {
			if encodeErr := json.NewEncoder(stdout).Encode(report.Receipt); encodeErr != nil {
				return writeFailure(stderr, encodeErr)
			}
		}
		return commandError(stderr, "execute Schema Plan", err)
	}
	if err := json.NewEncoder(stdout).Encode(report.Receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if !report.Receipt.Verified {
		return ExitFailure
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
		if batch, err := parser.ParseBatch(source); err == nil {
			for _, statement := range batch.Statements {
				if !queryStatement(statement.Kind) {
					return usageError(
						stderr,
						"query only accepts SHOW, DESCRIBE, SELECT, MATCH, or OPEN ROUTE",
					)
				}
			}
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

func runMutate(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var planJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--plan":
			if index+1 >= len(args) {
				return usageError(stderr, "--plan requires a JSON object")
			}
			if planJSON != "" {
				return usageError(stderr, "--plan may only be specified once")
			}
			planJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown mutate option: %q", args[index]))
		}
	}
	if planJSON == "" {
		return usageError(stderr, "mutate requires --plan JSON")
	}
	var plan skillwrite.Plan
	decoder := json.NewDecoder(bytes.NewBufferString(planJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--plan must be one strict Mutation Plan JSON object")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	tool := skillwrite.ToolFunc(func(ctx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements)
	})
	report, err := skillwrite.New(tool).Run(context.Background(), plan)
	if err != nil {
		return commandError(stderr, "execute Mutation Plan", err)
	}
	if err := json.NewEncoder(stdout).Encode(report.Receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if report.Receipt.Status == skillwrite.ReceiptCommittedUnverified {
		return ExitFailure
	}
	return ExitOK
}

func queryStatement(kind string) bool {
	switch kind {
	case "SHOW", "DESCRIBE", "SELECT", "MATCH", "OPEN_ROUTE":
		return true
	default:
		return false
	}
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
	if _, writeErr := fmt.Fprintf(stderr, "memora: %s: %v\n", action, err); writeErr != nil {
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
