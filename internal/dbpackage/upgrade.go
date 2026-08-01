package dbpackage

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/snapshot"
)

const UpgradePlanVersion = "memora.package-upgrade-plan/v1"

type UpgradePlan struct {
	Version                string `json:"version"`
	Status                 string `json:"status"`
	DatabaseID             string `json:"database_id"`
	Name                   string `json:"name"`
	CurrentPackageSHA256   string `json:"current_package_sha256"`
	CurrentSnapshotSHA256  string `json:"current_snapshot_sha256"`
	IncomingPackageSHA256  string `json:"incoming_package_sha256"`
	IncomingSnapshotSHA256 string `json:"incoming_snapshot_sha256"`
	CurrentSchemaVersion   uint64 `json:"current_schema_version"`
	IncomingSchemaVersion  uint64 `json:"incoming_schema_version"`
	IncomingSignerKeyID    string `json:"incoming_signer_key_id,omitempty"`
	Hash                   string `json:"hash"`
}

type UpgradeReceipt struct {
	Version               string `json:"version"`
	Status                string `json:"status"`
	PlanHash              string `json:"plan_hash"`
	DatabaseID            string `json:"database_id"`
	Name                  string `json:"name"`
	PreviousPackageSHA256 string `json:"previous_package_sha256"`
	PackageSHA256         string `json:"package_sha256"`
	SnapshotSHA256        string `json:"snapshot_sha256"`
	ReadOnly              bool   `json:"read_only"`
}

func (service *Service) PlanUpgrade(ctx context.Context, encoded []byte) (UpgradePlan, error) {
	if service == nil || service.store == nil {
		return UpgradePlan{}, packageError(result.CodeInternal, "package upgrade target Store is unavailable")
	}
	opened, err := service.Open(encoded)
	if err != nil {
		return UpgradePlan{}, err
	}
	current, err := catalog.New(service.store, catalog.Options{}).DescribeDatabase(ctx, opened.Manifest.DatabaseID)
	if err != nil {
		return UpgradePlan{}, err
	}
	if !current.ReadOnly || !strings.EqualFold(current.Name, opened.Manifest.Name) || current.PackageSHA256 == "" || current.PackageSnapshotSHA256 == "" {
		return UpgradePlan{}, packageError(result.CodePermissionDenied, "package upgrade requires the matching installed read-only Database")
	}
	if current.PackageSnapshotSHA256 == opened.Manifest.SnapshotSHA256 {
		return UpgradePlan{}, packageError(result.CodeValidation, "incoming package is already installed")
	}
	if opened.Manifest.SchemaVersion < current.SchemaVersion {
		return UpgradePlan{}, packageError(result.CodeRevisionConflict, "package upgrade cannot reduce Schema version")
	}
	signer := ""
	if opened.Signature != nil {
		signer = opened.Signature.KeyID
	}
	plan := UpgradePlan{
		Version: UpgradePlanVersion, Status: "review_required",
		DatabaseID: current.ID, Name: current.Name,
		CurrentPackageSHA256: current.PackageSHA256, CurrentSnapshotSHA256: current.PackageSnapshotSHA256,
		IncomingPackageSHA256: opened.PackageSHA256, IncomingSnapshotSHA256: opened.Manifest.SnapshotSHA256,
		CurrentSchemaVersion: current.SchemaVersion, IncomingSchemaVersion: opened.Manifest.SchemaVersion,
		IncomingSignerKeyID: signer,
	}
	hash, err := upgradePlanHash(plan)
	if err != nil {
		return UpgradePlan{}, err
	}
	plan.Hash = "sha256:" + hash
	return plan, nil
}

func (service *Service) ApplyUpgrade(
	ctx context.Context, encoded []byte, plan UpgradePlan,
) (UpgradeReceipt, error) {
	if err := validateUpgradePlan(plan); err != nil {
		return UpgradeReceipt{}, err
	}
	current, err := service.PlanUpgrade(ctx, encoded)
	if err != nil {
		return UpgradeReceipt{}, err
	}
	if current != plan {
		return UpgradeReceipt{}, packageError(result.CodeRevisionConflict, "package upgrade plan is stale or does not match the package")
	}
	if err := security.RequireDatabaseLevel(ctx, security.LevelStructural, plan.DatabaseID); err != nil {
		return UpgradeReceipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionApplyPackageUpgrade, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return UpgradeReceipt{}, err
	}
	opened, err := service.Open(encoded)
	if err != nil {
		return UpgradeReceipt{}, err
	}
	if err := service.snapshots.MergeImportWithOptions(ctx, opened.Snapshot, snapshot.MergeOptions{
		ReadOnly: true, PackageSHA256: opened.PackageSHA256,
		PackageSnapshotSHA256: opened.Manifest.SnapshotSHA256,
		PackageSignerKeyID:    plan.IncomingSignerKeyID, ReplaceDatabaseID: plan.DatabaseID,
	}); err != nil {
		return UpgradeReceipt{}, err
	}
	return UpgradeReceipt{
		Version: "memora.package-upgrade-receipt/v1", Status: "committed", PlanHash: plan.Hash,
		DatabaseID: plan.DatabaseID, Name: plan.Name, PreviousPackageSHA256: plan.CurrentPackageSHA256,
		PackageSHA256: plan.IncomingPackageSHA256, SnapshotSHA256: plan.IncomingSnapshotSHA256, ReadOnly: true,
	}, nil
}

func validateUpgradePlan(plan UpgradePlan) error {
	if plan.Version != UpgradePlanVersion || plan.Status != "review_required" || plan.DatabaseID == "" || plan.Name == "" ||
		!strings.HasPrefix(plan.Hash, "sha256:") {
		return packageError(result.CodeValidation, "package upgrade plan is invalid")
	}
	want, err := upgradePlanHash(plan)
	if err != nil || !equalHash(strings.TrimPrefix(plan.Hash, "sha256:"), want) {
		return packageError(result.CodeValidation, "package upgrade plan hash is invalid")
	}
	return nil
}

func upgradePlanHash(plan UpgradePlan) (string, error) {
	plan.Hash = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", packageError(result.CodeInternal, "package upgrade plan could not be encoded")
	}
	return Hash(encoded), nil
}
