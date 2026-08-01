package dbpackage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	RevocationVersion = "memora.package-revocation/v1"
	revocationBucket  = "package_revocations"
)

type Revocation struct {
	Version       string `json:"version"`
	ID            string `json:"revocation_id"`
	PackageSHA256 string `json:"package_sha256,omitempty"`
	SignerKeyID   string `json:"signer_key_id,omitempty"`
	Actor         string `json:"actor"`
	Reason        string `json:"reason"`
	Hash          string `json:"hash"`
}

func NewRevocation(id, packageSHA256, signerKeyID, actor, reason string) (Revocation, error) {
	value := Revocation{
		Version: RevocationVersion, ID: strings.TrimSpace(id), PackageSHA256: strings.ToLower(packageSHA256),
		SignerKeyID: strings.TrimSpace(signerKeyID), Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
	}
	if err := validateRevocationFields(value); err != nil {
		return Revocation{}, err
	}
	hash, err := revocationHash(value)
	if err != nil {
		return Revocation{}, err
	}
	value.Hash = "sha256:" + hash
	return value, nil
}

func (service *Service) Revoke(ctx context.Context, value Revocation) error {
	if service == nil || service.store == nil {
		return packageError(result.CodeInternal, "package revocation Store is unavailable")
	}
	if err := validateRevocation(value); err != nil {
		return err
	}
	if err := security.RequireLevel(ctx, security.LevelStructural); err != nil {
		return err
	}
	if err := security.RequireApproval(ctx, security.ActionApplyPackageRevocation, strings.TrimPrefix(value.Hash, "sha256:")); err != nil {
		return err
	}
	encoded, _ := json.Marshal(value)
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if current, getErr := tx.Get(ctx, revocationBucket, value.ID); getErr == nil {
		if bytes.Equal(current, encoded) {
			return nil
		}
		return packageError(result.CodeRevisionConflict, "revocation ID already binds different content")
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return getErr
	}
	if err := tx.Put(ctx, revocationBucket, value.ID, encoded); err != nil {
		return err
	}
	return tx.Commit()
}

func (service *Service) checkRevoked(ctx context.Context, opened Opened) error {
	if service == nil || service.store == nil {
		return nil
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entries, err := tx.Scan(ctx, revocationBucket)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		var value Revocation
		if json.Unmarshal(entry.Value, &value) != nil || validateRevocation(value) != nil || value.ID != entry.Key {
			return packageError(result.CodeInternal, "package revocation registry is corrupt")
		}
		packageMatch := value.PackageSHA256 != "" && equalHash(value.PackageSHA256, opened.PackageSHA256)
		signerMatch := opened.Signature != nil && value.SignerKeyID != "" && value.SignerKeyID == opened.Signature.KeyID
		if packageMatch || signerMatch {
			return packageError(result.CodePermissionDenied, "package or signer has been revoked")
		}
	}
	return nil
}

func validateRevocation(value Revocation) error {
	if err := validateRevocationFields(value); err != nil || !strings.HasPrefix(value.Hash, "sha256:") {
		return packageError(result.CodeValidation, "package revocation is invalid")
	}
	hash, err := revocationHash(value)
	if err != nil || !equalHash(strings.TrimPrefix(value.Hash, "sha256:"), hash) {
		return packageError(result.CodeValidation, "package revocation hash is invalid")
	}
	return nil
}

func validateRevocationFields(value Revocation) error {
	if value.Version != RevocationVersion ||
		security.ValidateMetadataText(value.ID, 200, true) != nil ||
		security.ValidateMetadataText(value.Actor, 160, true) != nil ||
		security.ValidateMetadataText(value.Reason, 1200, true) != nil ||
		(value.PackageSHA256 == "" && value.SignerKeyID == "") ||
		(value.PackageSHA256 != "" && !equalHash(value.PackageSHA256, value.PackageSHA256)) ||
		(value.SignerKeyID != "" && security.ValidateMetadataText(value.SignerKeyID, 200, true) != nil) {
		return packageError(result.CodeValidation, "package revocation fields are invalid")
	}
	return nil
}

func revocationHash(value Revocation) (string, error) {
	value.Hash = ""
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", packageError(result.CodeInternal, "package revocation could not be encoded")
	}
	return Hash(encoded), nil
}
