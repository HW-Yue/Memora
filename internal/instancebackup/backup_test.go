package instancebackup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/instancebackup"
)

func TestCreateAndVerifyPortableInstanceBackup(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if _, err := instance.Initialize(context.Background(), source, instance.Options{Clock: fixedClock{}, IDs: fixedID{}}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "databases", "payload.bin"), []byte("portable authority"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "backup")
	receipt, err := instancebackup.Create(context.Background(), source, destination, instancebackup.Options{Clock: fixedClock{}})
	if err != nil || receipt.Status != "created" || receipt.FileCount < 2 {
		t.Fatalf("Create() = %#v, %v", receipt, err)
	}
	manifest, err := instancebackup.Verify(destination)
	if err != nil || manifest.Hash != receipt.Hash || manifest.InstanceID != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("Verify() = %#v, %v", manifest, err)
	}
	if err := os.WriteFile(filepath.Join(destination, "snapshot", "databases", "payload.bin"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := instancebackup.Verify(destination); err == nil {
		t.Fatal("Verify accepted damaged backup")
	}
}

func TestBackupFaultDoesNotPublishDestination(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	if _, err := instance.Initialize(context.Background(), source, instance.Options{Clock: fixedClock{}, IDs: fixedID{}}); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(parent, "backup")
	_, err := instancebackup.Create(context.Background(), source, destination, instancebackup.Options{Fault: func(point string) error {
		if point == "before_publish" {
			return os.ErrInvalid
		}
		return nil
	}})
	if err == nil {
		t.Fatal("faulted backup succeeded")
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("fault published destination: %v", statErr)
	}
}

type fixedClock struct{}

func (fixedClock) Now() time.Time { return time.Date(2026, 8, 1, 8, 0, 0, 0, time.UTC) }

type fixedID struct{}

func (fixedID) Next() (string, error) { return "11111111-1111-4111-8111-111111111111", nil }
