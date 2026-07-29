package snapshot_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/snapshot"
)

func TestV0FixtureMigratesToV1AndPreservesUnknownFields(t *testing.T) {
	t.Parallel()

	fixture, err := os.ReadFile(filepath.Join("testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := snapshot.Migrate(fixture)
	if err != nil {
		t.Fatal(err)
	}
	assertJSONField(t, migrated, "version", snapshot.Version)
	assertJSONField(t, migrated, "future_v0", "preserve-me")
	if !jsonPathExists(migrated, "catalog", "databases", 0, "future_catalog") {
		t.Fatal("unknown Catalog field was not preserved")
	}
	if !jsonPathExists(migrated, "rows", 0, "future_row") {
		t.Fatal("unknown Row field was not preserved")
	}
	first, err := snapshot.CanonicalHash(migrated)
	if err != nil {
		t.Fatal(err)
	}
	second, err := snapshot.CanonicalHash(migrated)
	if err != nil || first != second {
		t.Fatalf("canonical hashes = %q, %q, %v", first, second, err)
	}
}

func TestSnapshotFormatRejectsUnknownVersionAndDanglingHistoryWithStableCode(t *testing.T) {
	t.Parallel()

	_, err := snapshot.Migrate([]byte(`{"version":"memora.logical-snapshot/v99"}`))
	assertFormatCode(t, err, result.CodeValidation)

	fixture, err := os.ReadFile(filepath.Join("testdata", "logical-snapshot-v0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fixture, &fields); err != nil {
		t.Fatal(err)
	}
	fields["version"] = json.RawMessage(`"memora.logical-snapshot/v1"`)
	fields["rows"] = json.RawMessage(`[]`)
	fields["relations"] = json.RawMessage(`{"current":[],"versions":[]}`)
	fields["commit_sequence"] = json.RawMessage(`1`)
	corrupt, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	_, err = snapshot.Migrate(corrupt)
	assertFormatCode(t, err, result.CodeValidation)
}

func assertFormatCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) {
		t.Fatalf("error = %v has no stable code, want %q", err, want)
	}
	if got := stable.StableCode(); got != string(want) {
		t.Fatalf("error = %v, code = %q, want %q", err, got, want)
	}
}
