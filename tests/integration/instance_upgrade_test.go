//go:build integration

package integration_test

import (
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/instanceupgrade"
)

func TestInstanceUpgradeCLIRequiresPlanApprovalAndStoppedDaemon(t *testing.T) {
	root := repositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "memora")
	command(t, root, "go", "build", "-o", binary, "./cmd/memora")
	dataDir := filepath.Join(temporary, "instance")
	command(t, root, binary, "init", "--data-dir", dataDir)
	rewriteFixtureFormat(t, dataDir, instance.MinimumUpgradableVersion)

	if output, err := runCommand(root, binary, "daemon", "start", "--data-dir", dataDir); err == nil ||
		!strings.Contains(output, "upgrade is required") {
		t.Fatalf("legacy daemon start output=%q err=%v", output, err)
	}
	var plan instanceupgrade.Plan
	if err := json.Unmarshal(
		[]byte(command(t, root, binary, "upgrade", "--plan", "--data-dir", dataDir)),
		&plan,
	); err != nil {
		t.Fatal(err)
	}
	if plan.FromVersion != instance.MinimumUpgradableVersion ||
		plan.ToVersion != instance.FormatVersion ||
		!plan.RequiresApply {
		t.Fatalf("upgrade plan = %#v", plan)
	}
	if _, err := os.Stat(plan.BackupRoot); !os.IsNotExist(err) {
		t.Fatalf("read-only plan created backup root: %v", err)
	}
	if output, err := runCommand(
		root,
		binary,
		"upgrade",
		"--apply",
		"--data-dir",
		dataDir,
	); err == nil || !strings.Contains(output, "requires --yes") {
		t.Fatalf("unapproved upgrade output=%q err=%v", output, err)
	}

	var receipt instanceupgrade.Receipt
	if err := json.Unmarshal(
		[]byte(command(
			t,
			root,
			binary,
			"upgrade",
			"--apply",
			"--yes",
			"--data-dir",
			dataDir,
		)),
		&receipt,
	); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "upgraded" || !receipt.BackupRetained {
		t.Fatalf("upgrade receipt = %#v", receipt)
	}
	if _, err := instance.Read(dataDir); err != nil {
		t.Fatalf("read upgraded Instance: %v", err)
	}

	command(t, root, binary, "daemon", "start", "--data-dir", dataDir)
	if output, err := runCommand(
		root,
		binary,
		"doctor",
		"repair",
		"--yes",
		"--backup",
		receipt.BackupPath,
		"--data-dir",
		dataDir,
	); err == nil || !strings.Contains(output, "must be stopped") {
		t.Fatalf("repair with daemon running output=%q err=%v", output, err)
	}
	command(t, root, binary, "daemon", "stop", "--data-dir", dataDir)
	command(
		t,
		root,
		binary,
		"doctor",
		"repair",
		"--yes",
		"--backup",
		receipt.BackupPath,
		"--data-dir",
		dataDir,
	)
	inspection, err := instance.Inspect(dataDir)
	if err != nil ||
		inspection.Status != instance.CompatibilityUpgradeRequired ||
		inspection.Metadata.FormatVersion != instance.MinimumUpgradableVersion {
		t.Fatalf("repaired Instance = %#v, %v", inspection, err)
	}
}

func TestNewerInstanceFormatIsNotSilentlyDowngraded(t *testing.T) {
	root := repositoryRoot(t)
	temporary := t.TempDir()
	binary := filepath.Join(temporary, "memora")
	command(t, root, "go", "build", "-o", binary, "./cmd/memora")
	dataDir := filepath.Join(temporary, "newer-instance")
	command(t, root, binary, "init", "--data-dir", dataDir)
	rewriteFixtureFormat(t, dataDir, instance.FormatVersion+1)

	if output, err := runCommand(
		root,
		binary,
		"upgrade",
		"--plan",
		"--data-dir",
		dataDir,
	); err == nil || !strings.Contains(output, "downgrade is unsupported") {
		t.Fatalf("newer format plan output=%q err=%v", output, err)
	}
	inspection, err := instance.Inspect(dataDir)
	if err != nil ||
		inspection.Status != instance.CompatibilityNewerFormat ||
		inspection.Metadata.FormatVersion != instance.FormatVersion+1 {
		t.Fatalf("newer Instance changed = %#v, %v", inspection, err)
	}
}

func rewriteFixtureFormat(t *testing.T, root string, version uint32) {
	t.Helper()
	metadataPath := filepath.Join(root, "instance.meta")
	encoded, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint32(encoded[8:12], version)
	binary.LittleEndian.PutUint32(encoded[40:44], crc32.ChecksumIEEE(encoded[:40]))
	if err := os.WriteFile(metadataPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
