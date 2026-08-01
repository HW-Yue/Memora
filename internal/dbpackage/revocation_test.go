package dbpackage_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestRevokedPackageCannotInstallAndRevocationIsDurable(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "revocation-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	encoded, _, err := dbpackage.New(source).Pack(ctx, "work", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "revocation-target.db")
	defer target.Close()
	packages := dbpackage.New(target)
	revocation, err := dbpackage.NewRevocation("revoke-1", dbpackage.Hash(encoded), "", "user:test", "publisher withdrew this version")
	if err != nil {
		t.Fatal(err)
	}
	approved := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		Approval: &security.Approval{Version: security.ApprovalVersion,
			Action:        security.ActionApplyPackageRevocation,
			SubjectSHA256: strings.TrimPrefix(revocation.Hash, "sha256:"), Confirmed: true},
	})
	if err := packages.Revoke(approved, revocation); err != nil {
		t.Fatal(err)
	}
	if err := packages.Revoke(approved, revocation); err != nil {
		t.Fatalf("idempotent Revoke() = %v", err)
	}
	packages = dbpackage.New(target)
	if _, err := packages.Open(encoded); err != nil {
		t.Fatalf("revoked package Open() = %v", err)
	}
	_, err = packages.Install(installContext(ctx, encoded), encoded, dbpackage.InstallOptions{Trusted: true})
	assertCode(t, err, result.CodePermissionDenied)
}

func TestRevokedSignerBlocksEverySignedCandidate(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	source := openStore(t, "revoked-signer-source.db")
	defer source.Close()
	seedSource(t, ctx, source)
	seed := sha256.Sum256([]byte("revoked-signer"))
	encoded, _, err := dbpackage.New(source).PackSigned(ctx, "work", "Alice", dbpackage.Signer{
		KeyID: "publisher:revoked", PrivateKey: ed25519.NewKeyFromSeed(seed[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	target := openStore(t, "revoked-signer-target.db")
	defer target.Close()
	packages := dbpackage.New(target)
	revocation, err := dbpackage.NewRevocation("revoke-signer", "", "publisher:revoked", "user:test", "key compromise")
	if err != nil {
		t.Fatal(err)
	}
	ctx = security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "user:test", DefaultLevel: security.LevelStructural,
		Approval: &security.Approval{Version: security.ApprovalVersion,
			Action:        security.ActionApplyPackageRevocation,
			SubjectSHA256: strings.TrimPrefix(revocation.Hash, "sha256:"), Confirmed: true},
	})
	if err := packages.Revoke(ctx, revocation); err != nil {
		t.Fatal(err)
	}
	_, err = packages.Install(installContext(context.Background(), encoded), encoded, dbpackage.InstallOptions{Trusted: true})
	assertCode(t, err, result.CodePermissionDenied)
}
