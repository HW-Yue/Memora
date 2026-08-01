package main

import (
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
	var directory, keyID, publicKeyFile string
	flag.StringVar(&directory, "directory", "", "absolute release directory")
	flag.StringVar(&keyID, "trusted-key-id", "", "trusted release signer key ID")
	flag.StringVar(&publicKeyFile, "trusted-public-key", "", "absolute base64 Ed25519 public key file")
	flag.Parse()
	if flag.NArg() != 0 || directory == "" || keyID == "" || publicKeyFile == "" ||
		!filepath.IsAbs(directory) || !filepath.IsAbs(publicKeyFile) {
		fmt.Fprintln(os.Stderr, "verify-release: --directory, --trusted-key-id, and --trusted-public-key absolute paths are required")
		os.Exit(2)
	}
	encoded, err := os.ReadFile(filepath.Clean(publicKeyFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-release: read public key: %v\n", err)
		os.Exit(1)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		fmt.Fprintln(os.Stderr, "verify-release: trusted public key is invalid")
		os.Exit(1)
	}
	manifest, err := release.VerifySigned(filepath.Clean(directory), map[string]ed25519.PublicKey{keyID: decoded})
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-release: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("verified signed Memora %s release (%s)\n", manifest.ProductVersion, manifest.Commit)
}
