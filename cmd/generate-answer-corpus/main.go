package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(arguments []string) error {
	if len(arguments) != 1 || !filepath.IsAbs(arguments[0]) || filepath.Clean(arguments[0]) != arguments[0] {
		return fmt.Errorf("usage: generate-answer-corpus ABSOLUTE_NORMALIZED_OUTPUT_DIRECTORY")
	}
	bundle, err := answercorpus.Generate()
	if err != nil {
		return fmt.Errorf("generate answer corpus: %w", err)
	}
	manifest, err := answercorpus.EncodeManifest(bundle.Manifest)
	if err != nil {
		return fmt.Errorf("encode answer manifest: %w", err)
	}
	groundTruth, err := answercorpus.EncodeGroundTruth(bundle.GroundTruth)
	if err != nil {
		return fmt.Errorf("encode answer ground truth: %w", err)
	}
	if err := os.MkdirAll(arguments[0], 0o755); err != nil {
		return fmt.Errorf("create answer corpus directory: %w", err)
	}
	for _, artifact := range []struct {
		name    string
		encoded []byte
	}{
		{name: "manifest.json", encoded: manifest},
		{name: "ground-truth.json", encoded: groundTruth},
	} {
		if err := writeAtomic(arguments[0], artifact.name, artifact.encoded); err != nil {
			return err
		}
	}
	return syncDirectory(arguments[0])
}

func writeAtomic(directory, name string, encoded []byte) error {
	temporary, err := os.CreateTemp(directory, "."+name+"-*")
	if err != nil {
		return fmt.Errorf("create %s staging file: %w", name, err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("set %s permissions: %w", name, err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write %s: %w", name, err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync %s: %w", name, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Rename(temporaryPath, filepath.Join(directory, name)); err != nil {
		return fmt.Errorf("publish %s: %w", name, err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open answer corpus directory: %w", err)
	}
	defer func() { _ = directory.Close() }()
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync answer corpus directory: %w", err)
	}
	return nil
}
