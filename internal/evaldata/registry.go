package evaldata

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxRegistryBytes = 4 << 20

var (
	idPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	revisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func LoadRegistry(path string) (Registry, string, error) {
	if !absoluteNormalized(path) {
		return Registry{}, "", errors.New("registry path must be absolute and normalized")
	}
	file, err := os.Open(path)
	if err != nil {
		return Registry{}, "", fmt.Errorf("open registry: %w", err)
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maxRegistryBytes+1))
	if err != nil {
		return Registry{}, "", fmt.Errorf("read registry: %w", err)
	}
	if len(encoded) == 0 || len(encoded) > maxRegistryBytes {
		return Registry{}, "", errors.New("registry size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var registry Registry
	if err := decoder.Decode(&registry); err != nil {
		return Registry{}, "", fmt.Errorf("decode registry: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return Registry{}, "", errors.New("registry has trailing content")
	}
	if err := validateRegistry(registry); err != nil {
		return Registry{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return registry, hex.EncodeToString(digest[:]), nil
}

func validateRegistry(registry Registry) error {
	if registry.Version != RegistryVersion || len(registry.Datasets) == 0 {
		return errors.New("registry version or datasets are invalid")
	}
	ids := make(map[string]struct{}, len(registry.Datasets))
	for _, dataset := range registry.Datasets {
		if !idPattern.MatchString(dataset.ID) || strings.TrimSpace(dataset.Title) == "" ||
			strings.TrimSpace(dataset.Role) == "" || strings.TrimSpace(dataset.License.SPDX) == "" ||
			!secureURL(dataset.License.URL) {
			return fmt.Errorf("dataset %q metadata is invalid", dataset.ID)
		}
		if _, exists := ids[dataset.ID]; exists {
			return fmt.Errorf("dataset %q is duplicated", dataset.ID)
		}
		ids[dataset.ID] = struct{}{}
		switch dataset.Source.Kind {
		case "git":
			if !secureURL(dataset.Source.URL) || !revisionPattern.MatchString(dataset.Source.Revision) ||
				len(dataset.Source.Artifacts) != 0 {
				return fmt.Errorf("dataset %q Git source is invalid", dataset.ID)
			}
		case "http":
			if dataset.Source.URL != "" || dataset.Source.Revision != "" || len(dataset.Source.Artifacts) == 0 {
				return fmt.Errorf("dataset %q HTTP source is invalid", dataset.ID)
			}
			paths := make(map[string]struct{}, len(dataset.Source.Artifacts))
			for _, artifact := range dataset.Source.Artifacts {
				if !safeRelative(artifact.Path) || !secureURL(artifact.URL) || artifact.Bytes <= 0 ||
					!digestPattern.MatchString(artifact.SHA256) {
					return fmt.Errorf("dataset %q artifact %q is invalid", dataset.ID, artifact.Path)
				}
				if _, exists := paths[artifact.Path]; exists {
					return fmt.Errorf("dataset %q artifact %q is duplicated", dataset.ID, artifact.Path)
				}
				paths[artifact.Path] = struct{}{}
			}
		default:
			return fmt.Errorf("dataset %q source kind is invalid", dataset.ID)
		}
	}
	return nil
}

func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path &&
		path != "." && path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator))
}

func secureURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Host == "" || parsed.RawQuery != "" && parsed.Scheme == "git" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	host := parsed.Hostname()
	return parsed.Scheme == "http" && (host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
}

func absoluteNormalized(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
