package cli

import (
	"crypto/sha256"
	"encoding/hex"
)

// packageSHA256 is the subject digest an INSTALL PACKAGE approval carries.
// It used to live in internal/dbpackage, deleted 2026-09-02 with the rest of
// the unreachable legacy stack; the CLI still needs the digest to build the
// approval, so the two lines moved here rather than keeping a package alive
// for them.
func packageSHA256(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
