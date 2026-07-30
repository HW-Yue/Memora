package releasepublish

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/release"
)

var stableVersionPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

type BuildOptions struct {
	RepositoryRoot string
	OutputDir      string
	Version        string
	Commit         string
	SourceEpoch    int64
	GoCommand      string
}

type Publication struct {
	Release release.Manifest
	Bundle  BundleManifest
}

func Build(ctx context.Context, options BuildOptions) (Publication, error) {
	normalized, err := validateBuildOptions(options)
	if err != nil {
		return Publication{}, err
	}
	if _, err := os.Lstat(normalized.OutputDir); err == nil {
		return Publication{}, errors.New("publication output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Publication{}, fmt.Errorf("inspect publication output: %w", err)
	}
	parent := filepath.Dir(normalized.OutputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Publication{}, fmt.Errorf("create publication parent: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".memora-publication-*")
	if err != nil {
		return Publication{}, fmt.Errorf("create publication staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	if _, err := release.Build(ctx, release.Options{
		RepositoryRoot: normalized.RepositoryRoot,
		OutputDir:      filepath.Join(staging, "release"),
		Version:        normalized.Version,
		Commit:         normalized.Commit,
		SourceEpoch:    normalized.SourceEpoch,
		GoCommand:      normalized.GoCommand,
	}); err != nil {
		return Publication{}, fmt.Errorf("build publication release: %w", err)
	}
	if _, err := buildBundle(normalized, staging); err != nil {
		return Publication{}, err
	}
	publication, err := Verify(staging, normalized.Version)
	if err != nil {
		return Publication{}, fmt.Errorf("verify staged publication: %w", err)
	}
	if err := os.Rename(staging, normalized.OutputDir); err != nil {
		return Publication{}, fmt.Errorf("publish publication directory: %w", err)
	}
	return publication, nil
}

func Verify(directory, expectedVersion string) (Publication, error) {
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return Publication{}, errors.New("publication directory must be absolute and normalized")
	}
	if !stableVersionPattern.MatchString(expectedVersion) {
		return Publication{}, errors.New("publication version must be a stable semantic version")
	}
	bundleName := bundleArchiveName(expectedVersion)
	expected := map[string]bool{
		"release":              false,
		bundleName:             false,
		bundleName + ".sha256": false,
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return Publication{}, fmt.Errorf("read publication directory: %w", err)
	}
	if len(entries) != len(expected) {
		return Publication{}, errors.New("publication has an unexpected file set")
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return Publication{}, errors.New("publication has an unexpected file set")
		}
		info, err := entry.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return Publication{}, errors.New("publication contains an invalid file")
		}
		if entry.Name() == "release" {
			if !info.IsDir() {
				return Publication{}, errors.New("publication release entry is not a directory")
			}
		} else if !info.Mode().IsRegular() {
			return Publication{}, errors.New("publication asset is not a regular file")
		}
		expected[entry.Name()] = true
	}
	releaseManifest, err := release.Verify(filepath.Join(directory, "release"))
	if err != nil {
		return Publication{}, fmt.Errorf("verify publication release: %w", err)
	}
	if releaseManifest.ProductVersion != expectedVersion {
		return Publication{}, errors.New("publication release version does not match")
	}
	bundleManifest, err := verifyBundle(directory, expectedVersion, releaseManifest)
	if err != nil {
		return Publication{}, err
	}
	return Publication{Release: releaseManifest, Bundle: bundleManifest}, nil
}

func validateBuildOptions(options BuildOptions) (BuildOptions, error) {
	options.Version = strings.TrimPrefix(strings.TrimSpace(options.Version), "v")
	options.Commit = strings.ToLower(strings.TrimSpace(options.Commit))
	if !stableVersionPattern.MatchString(options.Version) {
		return BuildOptions{}, errors.New("publication version must be a stable semantic version")
	}
	if !validObjectID(options.Commit) {
		return BuildOptions{}, errors.New("publication commit must be a Git object ID")
	}
	if options.SourceEpoch <= 0 || time.Unix(options.SourceEpoch, 0).Year() < 2000 {
		return BuildOptions{}, errors.New("publication source date epoch is invalid")
	}
	if options.GoCommand == "" {
		options.GoCommand = "go"
	}
	for label, value := range map[string]string{
		"repository root": options.RepositoryRoot,
		"output":          options.OutputDir,
	} {
		if !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return BuildOptions{}, fmt.Errorf("publication %s must be absolute and normalized", label)
		}
	}
	return options, nil
}
