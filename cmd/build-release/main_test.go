package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestReadPrivateKeyRequiresPrivateRegularFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "release.key")
	encoded := base64.StdEncoding.EncodeToString(make([]byte, ed25519.SeedSize)) + "\n"
	if err := os.WriteFile(path, []byte(encoded), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := readPrivateKey(path)
	if err != nil || len(key) != ed25519.PrivateKeySize {
		t.Fatalf("readPrivateKey() = %d bytes, %v", len(key), err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPrivateKey(path); err == nil {
		t.Fatal("readPrivateKey() accepted a group/world-readable key")
	}
}
