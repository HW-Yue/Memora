package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDefaults(t *testing.T) {
	t.Parallel()

	got, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{InstanceName: "default", LogLevel: "info"}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadPrecedence(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	writeConfig(t, configPath, `{
  "instance": "from_file",
  "data_dir": "/from/file",
  "log_level": "warn"
}`)
	environment := map[string]string{
		"MEMORA_INSTANCE":  "from_env",
		"MEMORA_DATA_DIR":  "/from/env",
		"MEMORA_LOG_LEVEL": "error",
	}
	cliInstance := "from_cli"
	cliDataDir := "/from/cli"
	cliLogLevel := "debug"
	got, err := Load(LoadOptions{
		ConfigFile: configPath,
		LookupEnv:  mapLookup(environment),
		Overrides: Overrides{
			InstanceName: &cliInstance,
			DataDir:      &cliDataDir,
			LogLevel:     &cliLogLevel,
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := Config{InstanceName: cliInstance, DataDir: cliDataDir, LogLevel: cliLogLevel}
	if got != want {
		t.Fatalf("Load() = %#v, want %#v", got, want)
	}
}

func TestLoadRejectsUnknownAndSecretFields(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"api_key", "model_api_key", "unknown"} {
		t.Run(field, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.json")
			writeConfig(t, path, `{"`+field+`":"secret"}`)
			_, err := Load(LoadOptions{ConfigFile: path})
			if err == nil || !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("Load() error = %v, want unknown field", err)
			}
		})
	}
}

func TestLoadIgnoresHostSecrets(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		"OPENAI_API_KEY":    "openai-secret",
		"ANTHROPIC_API_KEY": "anthropic-secret",
		"MEMORA_API_KEY":    "memora-secret",
		"MEMORA_INSTANCE":   "work",
	}
	got, err := Load(LoadOptions{LookupEnv: mapLookup(environment)})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	for _, secret := range []string{"openai-secret", "anthropic-secret", "memora-secret"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("serialized config leaked secret %q: %s", secret, encoded)
		}
	}
	if got.InstanceName != "work" {
		t.Fatalf("InstanceName = %q, want work", got.InstanceName)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want error
	}{
		{name: "relative data dir", env: map[string]string{"MEMORA_DATA_DIR": "relative"}, want: ErrDataDirNotAbsolute},
		{name: "invalid instance", env: map[string]string{"MEMORA_INSTANCE": "../escape"}, want: ErrInvalidInstanceName},
		{name: "invalid log level", env: map[string]string{"MEMORA_LOG_LEVEL": "verbose"}, want: ErrInvalidLogLevel},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(LoadOptions{LookupEnv: mapLookup(tt.env)})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Load() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDefaultFile(t *testing.T) {
	t.Parallel()

	home := filepath.Join(string(filepath.Separator), "Users", "memora-test")
	got, err := DefaultFile(home)
	if err != nil {
		t.Fatalf("DefaultFile() error = %v", err)
	}
	want := filepath.Join(home, "Library", "Application Support", "Memora", "config.json")
	if got != want {
		t.Fatalf("DefaultFile() = %q, want %q", got, want)
	}
	if _, err := DefaultFile("relative"); !errors.Is(err, ErrHomeNotAbsolute) {
		t.Fatalf("DefaultFile(relative) error = %v, want %v", err, ErrHomeNotAbsolute)
	}
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeConfig(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
