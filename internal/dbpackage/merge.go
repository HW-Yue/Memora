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

const MergePlanVersion = "memora.package-merge-plan/v1"

type MergePlan struct {
	Version                string   `json:"version"`
	Status                 string   `json:"status"`
	ForkDatabaseID         string   `json:"fork_database_id"`
	ForkName               string   `json:"fork_name"`
	ForkSnapshotSHA256     string   `json:"fork_snapshot_sha256"`
	SourceDatabaseID       string   `json:"source_database_id"`
	BasePackageSHA256      string   `json:"base_package_sha256"`
	BaseSnapshotSHA256     string   `json:"base_snapshot_sha256"`
	IncomingPackageSHA256  string   `json:"incoming_package_sha256"`
	IncomingSnapshotSHA256 string   `json:"incoming_snapshot_sha256"`
	MergedSnapshotSHA256   string   `json:"merged_snapshot_sha256,omitempty"`
	Conflicts              []string `json:"conflicts"`
	Hash                   string   `json:"hash"`
}

type MergeReceipt struct {
	Version              string `json:"version"`
	Status               string `json:"status"`
	PlanHash             string `json:"plan_hash"`
	DatabaseID           string `json:"database_id"`
	PreviousBaseSnapshot string `json:"previous_base_snapshot_sha256"`
	BaseSnapshot         string `json:"base_snapshot_sha256"`
	ReadOnly             bool   `json:"read_only"`
}

func (service *Service) PlanMerge(
	ctx context.Context, forkSelector string, basePackage, incomingPackage []byte,
) (MergePlan, error) {
	plan, _, err := service.planMerge(ctx, forkSelector, basePackage, incomingPackage)
	return plan, err
}

func (service *Service) planMerge(
	ctx context.Context, forkSelector string, basePackage, incomingPackage []byte,
) (MergePlan, []byte, error) {
	if service == nil || service.store == nil {
		return MergePlan{}, nil, packageError(result.CodeInternal, "package merge Store is unavailable")
	}
	base, err := service.Open(basePackage)
	if err != nil {
		return MergePlan{}, nil, err
	}
	incoming, err := service.Open(incomingPackage)
	if err != nil {
		return MergePlan{}, nil, err
	}
	if err := service.checkRevoked(ctx, incoming); err != nil {
		return MergePlan{}, nil, err
	}
	fork, err := catalog.New(service.store, catalog.Options{}).DescribeDatabase(ctx, forkSelector)
	if err != nil {
		return MergePlan{}, nil, err
	}
	if fork.ReadOnly || fork.ForkedFromDatabaseID == "" || fork.ForkedFromSnapshotSHA256 == "" ||
		base.Manifest.DatabaseID != fork.ForkedFromDatabaseID || base.Manifest.SnapshotSHA256 != fork.ForkedFromSnapshotSHA256 ||
		incoming.Manifest.DatabaseID != base.Manifest.DatabaseID || !strings.EqualFold(incoming.Manifest.Name, base.Manifest.Name) ||
		incoming.Manifest.SchemaVersion < base.Manifest.SchemaVersion {
		return MergePlan{}, nil, packageError(result.CodeRevisionConflict, "package merge base, fork, or upstream identity does not match")
	}
	exported, err := service.snapshots.Export(ctx)
	if err != nil {
		return MergePlan{}, nil, err
	}
	forkSnapshot, _, err := snapshot.FilterDatabase(exported, fork.ID)
	if err != nil {
		return MergePlan{}, nil, err
	}
	forkHash, err := snapshot.CanonicalHash(forkSnapshot)
	if err != nil {
		return MergePlan{}, nil, err
	}
	baseFork, _, err := snapshot.ForkDatabase(base.Snapshot, snapshot.ForkOptions{
		DatabaseID: fork.ID, Name: fork.Name, SourcePackage: base.PackageSHA256, SourceSnapshot: base.Manifest.SnapshotSHA256,
	})
	if err != nil {
		return MergePlan{}, nil, err
	}
	incomingFork, _, err := snapshot.ForkDatabase(incoming.Snapshot, snapshot.ForkOptions{
		DatabaseID: fork.ID, Name: fork.Name, SourcePackage: incoming.PackageSHA256, SourceSnapshot: incoming.Manifest.SnapshotSHA256,
	})
	if err != nil {
		return MergePlan{}, nil, err
	}
	merged, conflicts, err := snapshot.MergeFork(baseFork, forkSnapshot, incomingFork, snapshot.MergeForkOptions{
		SourceDatabaseID: base.Manifest.DatabaseID, IncomingPackage: incoming.PackageSHA256,
		IncomingSnapshot: incoming.Manifest.SnapshotSHA256,
	})
	if err != nil {
		return MergePlan{}, nil, err
	}
	status, mergedHash := "ready", ""
	if len(conflicts) > 0 {
		status = "conflict"
	} else if mergedHash, err = snapshot.CanonicalHash(merged); err != nil {
		return MergePlan{}, nil, err
	}
	plan := MergePlan{
		Version: MergePlanVersion, Status: status, ForkDatabaseID: fork.ID, ForkName: fork.Name,
		ForkSnapshotSHA256: forkHash, SourceDatabaseID: base.Manifest.DatabaseID,
		BasePackageSHA256: base.PackageSHA256, BaseSnapshotSHA256: base.Manifest.SnapshotSHA256,
		IncomingPackageSHA256: incoming.PackageSHA256, IncomingSnapshotSHA256: incoming.Manifest.SnapshotSHA256,
		MergedSnapshotSHA256: mergedHash, Conflicts: append([]string(nil), conflicts...),
	}
	hash, err := mergePlanHash(plan)
	if err != nil {
		return MergePlan{}, nil, err
	}
	plan.Hash = "sha256:" + hash
	return plan, merged, nil
}

func (service *Service) ApplyMerge(
	ctx context.Context, basePackage, incomingPackage []byte, plan MergePlan,
) (MergeReceipt, error) {
	if err := validateMergePlan(plan); err != nil {
		return MergeReceipt{}, err
	}
	if plan.Status != "ready" || len(plan.Conflicts) != 0 {
		return MergeReceipt{}, packageError(result.CodeRevisionConflict, "conflicting package merge plan cannot be applied")
	}
	current, merged, err := service.planMerge(ctx, plan.ForkDatabaseID, basePackage, incomingPackage)
	if err != nil {
		return MergeReceipt{}, err
	}
	if !sameMergePlan(current, plan) {
		return MergeReceipt{}, packageError(result.CodeRevisionConflict, "package merge plan is stale or package inputs changed")
	}
	if err := security.RequireDatabaseLevel(ctx, security.LevelStructural, plan.ForkDatabaseID); err != nil {
		return MergeReceipt{}, err
	}
	if err := security.RequireApproval(ctx, security.ActionMergePackageFork, strings.TrimPrefix(plan.Hash, "sha256:")); err != nil {
		return MergeReceipt{}, err
	}
	if err := service.snapshots.MergeImportWithOptions(ctx, merged, snapshot.MergeOptions{
		ReplaceDatabaseID: plan.ForkDatabaseID, ReplaceForkBaseSnapshot: plan.BaseSnapshotSHA256,
	}); err != nil {
		return MergeReceipt{}, err
	}
	return MergeReceipt{
		Version: "memora.package-merge-receipt/v1", Status: "committed", PlanHash: plan.Hash,
		DatabaseID: plan.ForkDatabaseID, PreviousBaseSnapshot: plan.BaseSnapshotSHA256,
		BaseSnapshot: plan.IncomingSnapshotSHA256, ReadOnly: false,
	}, nil
}

func validateMergePlan(plan MergePlan) error {
	if plan.Version != MergePlanVersion || (plan.Status != "ready" && plan.Status != "conflict") ||
		plan.ForkDatabaseID == "" || plan.BaseSnapshotSHA256 == "" || plan.IncomingSnapshotSHA256 == "" ||
		!strings.HasPrefix(plan.Hash, "sha256:") {
		return packageError(result.CodeValidation, "package merge plan is invalid")
	}
	hash, err := mergePlanHash(plan)
	if err != nil || !equalHash(strings.TrimPrefix(plan.Hash, "sha256:"), hash) {
		return packageError(result.CodeValidation, "package merge plan hash is invalid")
	}
	return nil
}

func mergePlanHash(plan MergePlan) (string, error) {
	plan.Hash = ""
	encoded, err := json.Marshal(plan)
	if err != nil {
		return "", packageError(result.CodeInternal, "package merge plan could not be encoded")
	}
	return Hash(encoded), nil
}

func sameMergePlan(left, right MergePlan) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return string(leftJSON) == string(rightJSON)
}
