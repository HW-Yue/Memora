package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

var (
	ErrHomeNotAbsolute     = errors.New("config home path is not absolute")
	ErrDataDirNotAbsolute  = errors.New("config data directory is not absolute")
	ErrInvalidInstanceName = errors.New("config instance name is invalid")
	ErrInvalidLogLevel     = errors.New("config log level is invalid")
)

type Config struct {
	InstanceName string `json:"instance"`
	DataDir      string `json:"data_dir,omitempty"`
	LogLevel     string `json:"log_level"`
}

type Overrides struct {
	InstanceName *string
	DataDir      *string
	LogLevel     *string
}

type LoadOptions struct {
	ConfigFile string
	LookupEnv  func(string) (string, bool)
	Overrides  Overrides
}

func DefaultFile(home string) (string, error) {
	if !filepath.IsAbs(home) {
		return "", ErrHomeNotAbsolute
	}
	return filepath.Join(home, "Library", "Application Support", "Memora", "config.json"), nil
}

func Load(options LoadOptions) (Config, error) {
	result := Config{
		InstanceName: "default",
		LogLevel:     "info",
	}
	if options.ConfigFile != "" {
		fileConfig, err := readFile(options.ConfigFile)
		if err != nil {
			return Config{}, err
		}
		result = merge(result, fileConfig)
	}

	lookup := options.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	if value, ok := lookup("MEMORA_INSTANCE"); ok {
		result.InstanceName = value
	}
	if value, ok := lookup("MEMORA_DATA_DIR"); ok {
		result.DataDir = value
	}
	if value, ok := lookup("MEMORA_LOG_LEVEL"); ok {
		result.LogLevel = value
	}
	if options.Overrides.InstanceName != nil {
		result.InstanceName = *options.Overrides.InstanceName
	}
	if options.Overrides.DataDir != nil {
		result.DataDir = *options.Overrides.DataDir
	}
	if options.Overrides.LogLevel != nil {
		result.LogLevel = *options.Overrides.LogLevel
	}
	result.LogLevel = strings.ToLower(result.LogLevel)

	if err := validate(result); err != nil {
		return Config{}, err
	}
	return result, nil
}

type fileValues struct {
	InstanceName string `json:"instance"`
	DataDir      string `json:"data_dir"`
	LogLevel     string `json:"log_level"`
}

func readFile(path string) (fileValues, error) {
	file, err := os.Open(path)
	if err != nil {
		return fileValues{}, fmt.Errorf("open config file: %w", err)
	}
	defer func() { _ = file.Close() }()

	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var values fileValues
	if err := decoder.Decode(&values); err != nil {
		return fileValues{}, fmt.Errorf("decode config file: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fileValues{}, errors.New("decode config file: multiple JSON values")
		}
		return fileValues{}, fmt.Errorf("decode config file trailing data: %w", err)
	}
	return values, nil
}

func merge(base Config, values fileValues) Config {
	if values.InstanceName != "" {
		base.InstanceName = values.InstanceName
	}
	if values.DataDir != "" {
		base.DataDir = values.DataDir
	}
	if values.LogLevel != "" {
		base.LogLevel = values.LogLevel
	}
	return base
}

func validate(value Config) error {
	if !validInstanceName(value.InstanceName) {
		return fmt.Errorf("%w: %q", ErrInvalidInstanceName, value.InstanceName)
	}
	if value.DataDir != "" && !filepath.IsAbs(value.DataDir) {
		return fmt.Errorf("%w: %q", ErrDataDirNotAbsolute, value.DataDir)
	}
	switch value.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%w: %q", ErrInvalidLogLevel, value.LogLevel)
	}
	return nil
}

func validInstanceName(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			continue
		}
		switch character {
		case '-', '_', '.':
		default:
			return false
		}
	}
	return true
}
