package nativesnapshot_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativesnapshot"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/snapshot"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestLogicalSnapshotNativeRoundTripPreservesCanonicalHash(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(filepath.Join("..", "snapshot", "testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	if err := nativesnapshot.NewNative(file).Import(fixture); err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	exported, err := nativesnapshot.NewNative(reopened).Export()
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	want, err := snapshot.CanonicalHash(fixture)
	if err != nil {
		t.Fatal(err)
	}
	got, err := snapshot.CanonicalHash(exported)
	if err != nil || got != want {
		t.Fatalf("native round-trip hash = %s, %v, want %s", got, err, want)
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(exported, &value) != nil || len(value["future_v0"]) == 0 {
		t.Fatal("forward-compatible snapshot field was not preserved")
	}
	if err := nativesnapshot.NewNative(reopened).Import(fixture); stableCode(err) != string(result.CodeAlreadyExists) {
		t.Fatalf("second Import() error = %v", err)
	}
}

func TestInvalidNativeImportLeavesTargetEmpty(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(filepath.Join("..", "snapshot", "testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if json.Unmarshal(fixture, &value) != nil {
		t.Fatal("fixture is invalid")
	}
	rows := value["rows"].([]any)
	rows[0].(map[string]any)["row_id"] = "row_dangling"
	invalid, _ := json.Marshal(value)
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	service := nativesnapshot.NewNative(file)
	if err := service.Import(invalid); stableCode(err) != string(result.CodeValidation) {
		t.Fatalf("invalid Import() error = %v", err)
	}
	if err := service.Import(fixture); err != nil {
		t.Fatalf("valid Import() after failure = %v", err)
	}
}

func stableCode(err error) string {
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return stable.StableCode()
	}
	return ""
}
