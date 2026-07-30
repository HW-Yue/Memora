package nativesnapshot

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativeconfig"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestLogicalSnapshotCarriesDatabaseConfigurationHistory(t *testing.T) {
	t.Parallel()
	source, err := nativestore.Create(filepath.Join(t.TempDir(), "source.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	configuration, err := nativeconfig.New(source)
	if err != nil {
		t.Fatal(err)
	}
	budgets := nativeconfig.Defaults()
	budgets.RouteChildren = 6
	if _, err := configuration.Update(budgets, 1, "agent:test", "snapshot configured database"); err != nil {
		t.Fatal(err)
	}
	encoded, err := NewNative(source).Export()
	if err != nil {
		t.Fatal(err)
	}

	target, err := nativestore.Create(filepath.Join(t.TempDir(), "target.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	if err := NewNative(target).Import(encoded); err != nil {
		t.Fatal(err)
	}
	history, err := nativeconfig.Existing(target).History()
	if err != nil || len(history) != 2 || history[1].Budgets.RouteChildren != 6 {
		t.Fatalf("imported configuration = %#v, %v", history, err)
	}
	reencoded, err := NewNative(target).Export()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(reencoded, encoded) {
		t.Fatal("configuration snapshot was not deterministic after import")
	}
}
