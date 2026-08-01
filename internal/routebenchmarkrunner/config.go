package routebenchmarkrunner

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillcontract"
)

const DriverConfigVersion = "memora.route-benchmark-driver-config/v1"

type DriverConfig struct {
	Version  string                 `json:"version"`
	Profiles []CommandProfileConfig `json:"profiles"`
}

type CommandProfileConfig struct {
	Host       hostcontract.HostProfile `json:"host"`
	Executable string                   `json:"executable"`
	Arguments  []string                 `json:"arguments"`
}

func LoadDriverConfig(path string, contract hostcontract.Contract) (DriverConfig, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return DriverConfig{}, runnerError(result.CodeInternal, "read driver config: %v", err)
	}
	var config DriverConfig
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return DriverConfig{}, runnerError(result.CodeValidation, "decode driver config: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return DriverConfig{}, runnerError(result.CodeValidation, "decode driver config: trailing JSON value")
	}
	if err := config.Validate(contract); err != nil {
		return DriverConfig{}, err
	}
	return config, nil
}

func (config DriverConfig) Validate(contract hostcontract.Contract) error {
	if config.Version != DriverConfigVersion || len(config.Profiles) < 3 {
		return runnerError(result.CodeValidation, "driver config version and at least three profiles are required")
	}
	invocations := make([]hostcontract.Invocation, 0, len(config.Profiles))
	seen := map[string]bool{}
	for index, profile := range config.Profiles {
		if profile.Arguments == nil {
			return runnerError(result.CodeValidation, "driver profile %d arguments must be an explicit list", index)
		}
		if _, err := NewCommandDriver(profile.Executable, profile.Arguments); err != nil {
			return err
		}
		key := profileKey(profile.Host)
		if seen[key] {
			return runnerError(result.CodeValidation, "driver profile %d is duplicated", index)
		}
		seen[key] = true
		invocations = append(invocations, hostcontract.Invocation{
			Version:    hostcontract.InvocationVersion,
			TaskDigest: strings.Repeat("a", 64), SkillDigest: strings.Repeat("b", 64),
			ProtocolDigest: strings.Repeat("c", 64), Host: profile.Host,
		})
	}
	if err := contract.ValidateMatrix(invocations); err != nil {
		return err
	}
	return nil
}

func (config DriverConfig) BuildProfiles() ([]Profile, error) {
	profiles := make([]Profile, 0, len(config.Profiles))
	for _, configured := range config.Profiles {
		driver, err := NewCommandDriver(configured.Executable, configured.Arguments)
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, Profile{Host: configured.Host, Driver: driver})
	}
	return profiles, nil
}

func CanonicalDigests(directory string) (string, string, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", "", runnerError(result.CodeValidation, "canonical Skill directory must be absolute and normalized")
	}
	bundle, err := skillcontract.Load(directory)
	if err != nil {
		return "", "", err
	}
	if err := bundle.Validate(); err != nil {
		return "", "", err
	}
	markdown, err := os.ReadFile(filepath.Join(directory, "SKILL.md"))
	if err != nil {
		return "", "", runnerError(result.CodeInternal, "read Canonical Skill: %v", err)
	}
	protocol, err := os.ReadFile(filepath.Join(directory, "contract.json"))
	if err != nil {
		return "", "", runnerError(result.CodeInternal, "read Skill protocol: %v", err)
	}
	return digestBytes(markdown), digestBytes(protocol), nil
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}
