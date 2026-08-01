package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var directory string
	var version string
	var keyID string
	var publicKeyFile string
	flag.StringVar(&directory, "directory", "", "absolute publication directory")
	flag.StringVar(&version, "version", "", "expected stable semantic version")
	flag.StringVar(&keyID, "trusted-key-id", "", "trusted release signer key ID")
	flag.StringVar(&publicKeyFile, "trusted-public-key", "", "absolute base64 Ed25519 public key file")
	flag.Parse()
	if flag.NArg() != 0 || directory == "" || version == "" || keyID == "" ||
		publicKeyFile == "" || !filepath.IsAbs(publicKeyFile) {
		fmt.Fprintln(os.Stderr, "verify-publication: --directory, --version, --trusted-key-id, and absolute --trusted-public-key are required")
		os.Exit(2)
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-publication: resolve directory: %v\n", err)
		os.Exit(1)
	}
	encoded, err := os.ReadFile(filepath.Clean(publicKeyFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-publication: read public key: %v\n", err)
		os.Exit(1)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(string(encoded)))
	if err != nil || len(decoded) != ed25519.PublicKeySize {
		fmt.Fprintln(os.Stderr, "verify-publication: trusted public key is invalid")
		os.Exit(1)
	}
	publication, err := releasepublish.VerifySigned(
		filepath.Clean(absolute), version, map[string]ed25519.PublicKey{keyID: decoded},
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-publication: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf(
		"verified Memora %s publication for %s with %d release assets\n",
		publication.Release.ProductVersion,
		publication.Release.Commit,
		len(releasepublish.AssetNames(publication.Release.ProductVersion)),
	)
}
