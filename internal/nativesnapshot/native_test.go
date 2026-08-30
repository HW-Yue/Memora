package nativesnapshot_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerow"
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

// TestImportRecordsAttributionInTheChangeLogNotAHistoryRecord is E5 stage 6's
// gate.
//
// Attribution belongs to the transaction that wrote a revision and lives once
// per transaction in the Change Log. Ordinary writes stopped duplicating it
// into a per-Row History record in stage 5; RESTORE was the last writer, and it
// wrote one because the revisions it replays carry attribution but no change
// sequence — so there was nowhere else for it to go.
//
// There is now: RESTORE records the attribution it is replaying as Change Log
// envelopes and stamps each restored Row with the sequence that names its own.
// So every revision in the Database, however it got there, answers "who wrote
// this and why" the same way.
func TestImportRecordsAttributionInTheChangeLogNotAHistoryRecord(t *testing.T) {
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
	t.Cleanup(func() { _ = file.Close() })
	if err := nativesnapshot.NewNative(file).Import(fixture); err != nil {
		t.Fatalf("Import() error = %v", err)
	}

	histories, err := file.IDs(nativestore.ObjectKindHistory)
	if err != nil {
		t.Fatal(err)
	}
	if len(histories) != 0 {
		t.Fatalf("Import wrote %d History records; nothing writes that kind any more", len(histories))
	}

	// Every restored revision names an envelope, and the envelope carries the
	// attribution the snapshot recorded.
	rows := nativerow.New(file)
	values, err := rows.AllRows()
	if err != nil {
		t.Fatal(err)
	}
	if len(values) == 0 {
		t.Fatal("fixture restored no Rows")
	}
	changes := nativechange.New(file)
	for _, value := range values {
		if value.ChangeSequence == 0 {
			t.Fatalf("restored Row %q revision %d carries no change sequence, so its attribution has no home",
				value.ID, value.Revision)
		}
		envelope, err := changes.Get(value.ChangeSequence)
		if err != nil {
			t.Fatalf("Row %q change sequence %d: %v", value.ID, value.ChangeSequence, err)
		}
		if envelope.Actor == "" || envelope.Reason == "" {
			t.Fatalf("Row %q envelope carries empty attribution: %#v", value.ID, envelope)
		}
		named := false
		for _, entry := range envelope.Entries {
			named = named || (entry.ObjectID == value.ID && entry.AfterRevision == value.Revision)
		}
		if !named {
			t.Fatalf("envelope %d does not name revision %d of %q",
				value.ChangeSequence, value.Revision, value.ID)
		}
	}
}
