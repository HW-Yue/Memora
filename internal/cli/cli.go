package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/instance"
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
  help       Show this help
  init       Initialize a local instance
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
	HomeDir   func() (string, error)
	LookupEnv func(string) (string, bool)
	Clock     instance.Clock
	IDs       instance.IDSource
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
	case "init":
		return runInit(args[1:], stdout, stderr, dependencies)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
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
