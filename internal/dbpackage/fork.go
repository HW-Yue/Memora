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

const ForkPlanVersion = "memora.package-fork-plan/v1"

type ForkPlan struct {
	Version              string `json:"version"`
	Status               string `json:"status"`
	SourceDatabaseID     string `json:"source_database_id"`
	SourceName           string `json:"source_name"`
	SourcePackageSHA256  string `json:"source_package_sha256"`
	SourceSnapshotSHA256 string `json:"source_snapshot_sha256"`
	TargetDatabaseID     string `json:"target_database_id"`
	TargetName           string `json:"target_name"`
	Hash                 string `json:"hash"`
}

type ForkReceipt struct {
	Version          string `json:"version"`
	Status           string `json:"status"`
	PlanHash         string `json:"plan_hash"`
	SourceDatabaseID string `json:"source_database_id"`
	DatabaseID       string `json:"database_id"`
	Name             string `json:"name"`
	ReadOnly         bool   `json:"read_only"`
}

func (service *Service) PlanFork(
	ctx context.Context, source, targetDatabaseID, targetName string,
) (ForkPlan, error) {
	if service == nil || service.store == nil {
		return ForkPlan{}, packageError(result.CodeInternal, "package fork Store is unavailable")
	}
	if security.ValidateMetadataText(targetDatabaseID, 200, true) != nil ||
		security.ValidateMetadataText(targetName, 200, true) != nil {
		return ForkPlan{}, packageError(result.CodeValidation, "package fork target identity is invalid")
	}
	dictionary := catalog.New(service.store, catalog.Options{})
	database, err := dictionary.DescribeDatabase(ctx, source)
	if err != nil {
		return ForkPlan{}, err
	}
	if !database.ReadOnly || database.PackageSHA256 == "" || database.PackageSnapshotSHA256 == "" {
		return ForkPlan{}, packageError(result.CodePermissionDenied, "package fork requires an installed read-only Database")
	}
	databases, err := dictionary.ShowDatabases(ctx)
	if err != nil {
		return ForkPlan{}, err
	}
	for _, existing := range databases {
		if existing.ID == targetDatabaseID || strings.EqualFold(existing.Name, targetName) {
			return ForkPlan{}, packageError(result.CodeAlreadyExists, "package fork target identity or name exists")
		}
	}
	plan := ForkPlan{
		Version: ForkPlanVersion, Status: "review_required",
		SourceDatabaseID: database.ID, SourceName: database.Name,
		SourcePackageSHA256: database.PackageSHA256, SourceSnapshotSHA256: database.PackageSnapshotSHA256,
		TargetDatabaseID: targetDatabaseID, TargetName: targetName,
	}
	hash, err := forkPlanHash(plan)
	if err != nil {
		return ForkPlan{}, err
	}
	plan.Hash = "sha256:" + hash
	return plan, nil
}

func (service *Service) ApplyFork(ctx context.Context, plan ForkPlan) (ForkReceipt, error) {
	if err := validateForkPlan(plan); err != nil {
		return ForkReceipt{}, err
	}
	current, err := service.PlanFork(ctx, plan.SourceDatabaseID, plan.TargetDatabaseID, plan.TargetName)
	if err != nil {
		return ForkReceipt{}, err
	}
	if current != plan {
		return ForkReceipt{}, packageError(result.CodeRevisionConflict, "package fork plan is stale")
	}
	if err := security.RequireDatabaseLevel(ctx, security.LevelStructural, plan.SourceDatabaseID); err != nil {
		return ForkReceipt{}, err
	}
	if err := security.RequireAnyDatabaseLevel(ctx, security.LevelStructural, plan.TargetDatabaseID, plan.TargetName); err != nil {
		return ForkReceipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionForkPackageDatabase, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return ForkReceipt{}, err
	}
	exported, err := service.snapshots.Export(ctx)
	if err != nil {
		return ForkReceipt{}, err
	}
	filtered, _, err := snapshot.FilterDatabase(exported, plan.SourceDatabaseID)
	if err != nil {
		return ForkReceipt{}, err
	}
	forked, _, err := snapshot.ForkDatabase(filtered, snapshot.ForkOptions{
		DatabaseID: plan.TargetDatabaseID, Name: plan.TargetName,
		SourcePackage: plan.SourcePackageSHA256, SourceSnapshot: plan.SourceSnapshotSHA256,
	})
	if err != nil {
		return ForkReceipt{}, err
	}
	if err := service.snapshots.MergeImportWithOptions(ctx, forked, snapshot.MergeOptions{}); err != nil {
		return ForkReceipt{}, err
	}
	return ForkReceipt{
		Version: "memora.package-fork-receipt/v1", Status: "committed", PlanHash: plan.Hash,
		SourceDatabaseID: plan.SourceDatabaseID, DatabaseID: plan.TargetDatabaseID,
		Name: plan.TargetName, ReadOnly: false,
	}, nil
}

func validateForkPlan(plan ForkPlan) error {
	if plan.Version != ForkPlanVersion || plan.Status != "review_required" || plan.SourceDatabaseID == "" ||
		plan.TargetDatabaseID == "" || plan.TargetName == "" || !strings.HasPrefix(plan.Hash, "sha256:") {
		return packageError(result.CodeValidation, "package fork plan is invalid")
	}
	hash, err := forkPlanHash(plan)
	if err != nil || !equalHash(strings.TrimPrefix(plan.Hash, "sha256:"), hash) {
		return packageError(result.CodeValidation, "package fork plan hash is invalid")
	}
	return nil
}

func forkPlanHash(plan ForkPlan) (string, error) {
	plan.Hash = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", packageError(result.CodeInternal, "package fork plan could not be encoded")
	}
	return Hash(encoded), nil
}
