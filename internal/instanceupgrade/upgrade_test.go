package instanceupgrade_test

import (
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/instanceupgrade"
	"github.com/HW-Yue/Memora/internal/testkit"
)

const (
	instanceID  = "018f2f7e-7b5d-7c31-8a29-53f27d8f93c1"
	migrationID = "01922222-2222-7222-8222-222222222222"
)

func TestPreviewIsReadOnlyAndDistinguishesCompatibility(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	plan, err := instanceupgrade.Preview(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != instanceupgrade.PlanVersion ||
		plan.InstanceID != instanceID ||
		plan.FromVersion != 1 ||
		plan.ToVersion != instance.FormatVersion ||
		!plan.RequiresStop ||
		!plan.RequiresApply {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := os.Stat(plan.BackupRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Preview created backup root: %v", err)
	}
	setFormatVersion(t, root, instance.FormatVersion)
	if _, err := instanceupgrade.Preview(root); !errors.Is(err, instanceupgrade.ErrAlreadyCurrent) {
		t.Fatalf("current Preview error = %v", err)
	}
	setFormatVersion(t, root, instance.FormatVersion+1)
	if _, err := instanceupgrade.Preview(root); !errors.Is(err, instanceupgrade.ErrDowngradeUnsupported) {
		t.Fatalf("newer Preview error = %v", err)
	}
}

func TestApplyCreatesVerifiedBackupAndUpgradesAtomically(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	completedAt := time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)
	receipt, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
		Clock:    testkit.NewFakeClock(completedAt),
		IDs:      testkit.NewFakeIDs(migrationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Version != instanceupgrade.ReceiptVersion ||
		receipt.Status != "upgraded" ||
		receipt.MigrationID != migrationID ||
		!receipt.BackupRetained {
		t.Fatalf("receipt = %#v", receipt)
	}
	metadata, err := instance.Read(root)
	if err != nil || metadata.FormatVersion != instance.FormatVersion ||
		metadata.InstanceID != instanceID {
		t.Fatalf("upgraded metadata = %#v, %v", metadata, err)
	}
	backup, err := instanceupgrade.VerifyBackup(receipt.BackupPath)
	if err != nil || backup.InstanceID != instanceID || backup.FromVersion != 1 {
		t.Fatalf("backup = %#v, %v", backup, err)
	}
	assertFile(t, filepath.Join(root, "databases", "payload.bin"), "authoritative data\n")
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(instance.MigrationJournalRelativePath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("migration journal remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "system", "format-state.json")); err != nil {
		t.Fatalf("format state missing: %v", err)
	}
}

func TestApplyRequiresApprovalAndStoppedDaemon(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	if _, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{}); !errors.Is(err, instanceupgrade.ErrApprovalRequired) {
		t.Fatalf("unapproved Apply error = %v", err)
	}
	lease, err := daemon.Acquire(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
	}); !errors.Is(err, instanceupgrade.ErrDaemonRunning) {
		t.Fatalf("running Apply error = %v", err)
	}
	inspection, err := instance.Inspect(root)
	if err != nil || inspection.Metadata.FormatVersion != 1 {
		t.Fatalf("running Apply changed metadata: %#v, %v", inspection, err)
	}
}

func TestInterruptedMigrationBlocksStartupAndDoctorRepairRollsBack(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	interrupted := errors.New("simulated crash")
	_, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
		Clock:    testkit.NewFakeClock(time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)),
		IDs:      testkit.NewFakeIDs(migrationID),
		Fault: func(phase string) error {
			if phase == "after_metadata" {
				return interrupted
			}
			return nil
		},
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("interrupted Apply error = %v", err)
	}
	if _, err := instance.Read(root); !errors.Is(err, instance.ErrMigrationIncomplete) {
		t.Fatalf("Read after interruption = %v", err)
	}
	receipt, err := instanceupgrade.Repair(context.Background(), root, "", instanceupgrade.Options{
		Approved: true,
		Clock:    testkit.NewFakeClock(time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "rolled_back" || receipt.ToVersion != 1 || !receipt.BackupRetained {
		t.Fatalf("repair receipt = %#v", receipt)
	}
	inspection, err := instance.Inspect(root)
	if err != nil ||
		inspection.Status != instance.CompatibilityUpgradeRequired ||
		inspection.Metadata.InstanceID != instanceID {
		t.Fatalf("repaired inspection = %#v, %v", inspection, err)
	}
	assertFile(t, filepath.Join(root, "databases", "payload.bin"), "authoritative data\n")
	if _, err := os.Stat(filepath.Join(root, "system", "format-state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("repair retained format state: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "databases", "payload.bin"), []byte("current data\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backupPayload := filepath.Join(receipt.BackupPath, "snapshot", "databases", "payload.bin")
	if err := os.WriteFile(backupPayload, []byte("corrupt backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceupgrade.Repair(context.Background(), root, receipt.BackupPath, instanceupgrade.Options{
		Approved: true,
	}); err == nil || !strings.Contains(err.Error(), "does not match manifest") {
		t.Fatalf("corrupt repair error = %v", err)
	}
	assertFile(t, filepath.Join(root, "databases", "payload.bin"), "current data\n")
}

func TestExplicitBackupRollbackIsJournaledAndRestoresExactSnapshot(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	receipt, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
		Clock:    testkit.NewFakeClock(time.Date(2026, 7, 30, 15, 0, 0, 0, time.UTC)),
		IDs:      testkit.NewFakeIDs(migrationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	postBackup := filepath.Join(root, "databases", "post-upgrade.bin")
	if err := os.WriteFile(postBackup, []byte("must be rolled back\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	interrupted := errors.New("simulated repair crash")
	if _, err := instanceupgrade.Repair(
		context.Background(),
		root,
		receipt.BackupPath,
		instanceupgrade.Options{
			Approved: true,
			Clock:    testkit.NewFakeClock(time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)),
			Fault: func(phase string) error {
				if phase == "after_repair_journal" {
					return interrupted
				}
				return nil
			},
		},
	); !errors.Is(err, interrupted) {
		t.Fatalf("interrupted Repair error = %v", err)
	}
	if _, err := instance.Read(root); !errors.Is(err, instance.ErrMigrationIncomplete) {
		t.Fatalf("Read while repair is pending = %v", err)
	}
	rolledBack, err := instanceupgrade.Repair(
		context.Background(),
		root,
		"",
		instanceupgrade.Options{
			Approved: true,
			Clock:    testkit.NewFakeClock(time.Date(2026, 7, 30, 17, 0, 0, 0, time.UTC)),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Status != "rolled_back" || rolledBack.BackupPath != receipt.BackupPath {
		t.Fatalf("rollback receipt = %#v", rolledBack)
	}
	if _, err := os.Stat(postBackup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("post-backup file survived rollback: %v", err)
	}
	assertFile(t, filepath.Join(root, "databases", "payload.bin"), "authoritative data\n")
	inspection, err := instance.Inspect(root)
	if err != nil ||
		inspection.Status != instance.CompatibilityUpgradeRequired ||
		inspection.Metadata.FormatVersion != instance.MinimumUpgradableVersion {
		t.Fatalf("rolled-back inspection = %#v, %v", inspection, err)
	}
}

func TestRepairRejectsOverlappingBackupAndUnsupportedDowngrade(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	if _, err := instanceupgrade.Repair(
		context.Background(),
		root,
		filepath.Join(root, "backup"),
		instanceupgrade.Options{Approved: true},
	); err == nil || !strings.Contains(err.Error(), "must be disjoint") {
		t.Fatalf("overlapping backup error = %v", err)
	}

	receipt, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
		IDs:      testkit.NewFakeIDs(migrationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	setFormatVersion(t, root, instance.FormatVersion+1)
	if _, err := instanceupgrade.Repair(
		context.Background(),
		root,
		receipt.BackupPath,
		instanceupgrade.Options{Approved: true},
	); !errors.Is(err, instanceupgrade.ErrDowngradeUnsupported) {
		t.Fatalf("newer format repair error = %v", err)
	}
}

func TestVerifyBackupRejectsPrivacyModeDrift(t *testing.T) {
	t.Parallel()

	root := legacyInstance(t)
	receipt, err := instanceupgrade.Apply(context.Background(), root, instanceupgrade.Options{
		Approved: true,
		IDs:      testkit.NewFakeIDs(migrationID),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(receipt.BackupPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := instanceupgrade.VerifyBackup(receipt.BackupPath); err == nil ||
		!strings.Contains(err.Error(), "private regular directory") {
		t.Fatalf("backup mode error = %v", err)
	}
}

func legacyInstance(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "instance")
	if _, err := instance.Initialize(context.Background(), root, instance.Options{
		Clock: testkit.NewFakeClock(time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)),
		IDs:   testkit.NewFakeIDs(instanceID),
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "databases", "payload.bin"),
		[]byte("authoritative data\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	setFormatVersion(t, root, instance.MinimumUpgradableVersion)
	return root
}

func setFormatVersion(t *testing.T, root string, version uint32) {
	t.Helper()
	path := filepath.Join(root, "instance.meta")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(encoded[8:12], version)
	binary.LittleEndian.PutUint32(encoded[40:44], crc32.ChecksumIEEE(encoded[:40]))
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s = %q, want %q", path, got, want)
	}
}
