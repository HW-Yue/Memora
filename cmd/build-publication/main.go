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
	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var options releasepublish.BuildOptions
	var signingKey, signerKeyID string
	flag.StringVar(&options.RepositoryRoot, "repository", "", "absolute repository snapshot root")
	flag.StringVar(&options.OutputDir, "output", "", "absolute new publication directory")
	flag.StringVar(&options.Version, "version", "", "stable semantic product version")
	flag.StringVar(&options.Commit, "commit", "", "Git commit object ID")
	flag.Int64Var(&options.SourceEpoch, "source-date-epoch", 0, "reproducible build timestamp")
	flag.StringVar(&options.GoCommand, "go", "go", "Go command")
	flag.StringVar(&signingKey, "signing-key", "", "absolute private Ed25519 key file")
	flag.StringVar(&signerKeyID, "signer-key-id", "", "stable release signer key ID")
	flag.Parse()
	if flag.NArg() != 0 {
		fatal("positional arguments are not supported")
	}
	if options.RepositoryRoot == "" {
		current, err := os.Getwd()
		if err != nil {
			fatal(fmt.Sprintf("resolve repository: %v", err))
		}
		options.RepositoryRoot = current
	}
	options.RepositoryRoot = absolute(options.RepositoryRoot)
	options.OutputDir = absolute(options.OutputDir)
	if signingKey == "" || signerKeyID == "" {
		fatal("--signing-key and --signer-key-id are required")
	}
	keyPath := absolute(signingKey)
	privateKey, err := readPrivateKey(keyPath)
	if err != nil {
		fatal(err.Error())
	}
	options.Signer = &release.Signer{KeyID: signerKeyID, PrivateKey: privateKey}
	publication, err := releasepublish.Build(context.Background(), options)
	if err != nil {
		fatal(err.Error())
	}
	fmt.Printf(
		"built Memora %s publication for %s with %d Skill files at %s\n",
		publication.Release.ProductVersion,
		publication.Release.Commit,
		len(publication.Bundle.Files),
		options.OutputDir,
	)
}

func readPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !filepath.IsAbs(path) || !info.Mode().IsRegular() ||
		info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || info.Size() > 1024 {
		return nil, fmt.Errorf("signing key must be an absolute private regular file with mode 0600 or stricter")
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

func absolute(value string) string {
	if value == "" {
		return ""
	}
	result, err := filepath.Abs(value)
	if err != nil {
		return value
	}
	return filepath.Clean(result)
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "build-publication:", message)
	os.Exit(1)
}
