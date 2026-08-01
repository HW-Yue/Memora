package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/release"
)

func main() {
	var options release.Options
	var signingKey, signerKeyID string
	flag.StringVar(&options.RepositoryRoot, "repository", "", "absolute repository root")
	flag.StringVar(&options.OutputDir, "output", "", "absolute new output directory")
	flag.StringVar(&options.Version, "version", "", "semantic product version")
	flag.StringVar(&options.Commit, "commit", "", "Git commit object ID")
	flag.Int64Var(&options.SourceEpoch, "source-date-epoch", 0, "reproducible build timestamp")
	flag.StringVar(&options.GoCommand, "go", "go", "Go command")
	flag.StringVar(&signingKey, "signing-key", "", "absolute private Ed25519 key file")
	flag.StringVar(&signerKeyID, "signer-key-id", "", "stable release signer key ID")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "build-release: positional arguments are not supported")
		os.Exit(2)
	}
	if options.RepositoryRoot == "" {
		current, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "build-release: resolve repository: %v\n", err)
			os.Exit(1)
		}
		options.RepositoryRoot = current
	}
	if absolute, err := filepath.Abs(options.RepositoryRoot); err == nil {
		options.RepositoryRoot = absolute
	}
	if options.OutputDir != "" {
		if absolute, err := filepath.Abs(options.OutputDir); err == nil {
			options.OutputDir = absolute
		}
	}
	if signingKey != "" || signerKeyID != "" {
		if signingKey == "" || signerKeyID == "" {
			fmt.Fprintln(os.Stderr, "build-release: --signing-key and --signer-key-id are required together")
			os.Exit(2)
		}
		absolute, err := filepath.Abs(signingKey)
		if err != nil {
			fmt.Fprintf(os.Stderr, "build-release: resolve signing key: %v\n", err)
			os.Exit(1)
		}
		privateKey, err := readPrivateKey(filepath.Clean(absolute))
		if err != nil {
			fmt.Fprintf(os.Stderr, "build-release: %v\n", err)
			os.Exit(1)
		}
		options.Signer = &release.Signer{KeyID: signerKeyID, PrivateKey: privateKey}
	}
	manifest, err := release.Build(context.Background(), options)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build-release: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"built Memora %s (%s) release with %d artifacts at %s\n",
		manifest.ProductVersion, manifest.Commit, len(manifest.Artifacts), options.OutputDir,
	)
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 1024 {
		return nil, fmt.Errorf("signing key must be a private regular file with mode 0600 or stricter")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil {
		return nil, fmt.Errorf("decode signing key: %w", err)
	}
	if len(decoded) == ed25519.SeedSize {
		return ed25519.NewKeyFromSeed(decoded), nil
	}
	if len(decoded) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("signing key must contain a 32-byte seed or 64-byte private key")
	}
	return ed25519.PrivateKey(decoded), nil
}
