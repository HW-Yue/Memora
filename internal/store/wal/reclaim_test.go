package wal

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

func TestSegmentReclaimManifestDeleteReopenAndRecover(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "redo")
	set, initReceipt, checkpoint, deltaReceipt := reclaimableSet(t, directory)
	report, err := set.Reclaim()
	if err != nil {
		t.Fatalf("Reclaim() error = %v", err)
	}
	if report.FirstRetainedSegmentID != 2 || report.DeletedSegments != 1 ||
		report.ResidualSegments != 0 {
		t.Fatalf("Reclaim() report = %#v", report)
	}
	if _, err := os.Stat(segmentPath(directory, 1)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old Segment Stat() error = %v, want not-exist", err)
	}
	if _, err := os.Stat(filepath.Join(directory, segmentManifestName)); err != nil {
		t.Fatalf("manifest Stat() error = %v", err)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	state, err := reopened.State()
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if len(state) != 1 || state[0].ID != 2 {
		t.Fatalf("retained state = %#v", state)
	}
	gotCheckpoint, exists, err := reopened.LatestCheckpoint()
	if err != nil || !exists || gotCheckpoint != checkpoint {
		t.Fatalf("LatestCheckpoint() = %#v, %t, %v", gotCheckpoint, exists, err)
	}
	if _, err := reopened.Commit(1, []Record{{Type: TypePageInit}}); !errors.Is(
		err,
		ErrDuplicateTransaction,
	) {
		t.Fatalf("Commit(reclaimed ID) error = %v", err)
	}
	store := fakeStoreWithPage(page.Page{
		Header: page.Header{
			Type: page.TypeData, SpaceID: 7, PageID: 1, LSN: initReceipt.FirstLSN,
		},
		Payload: []byte("abc"),
	})
	recovery, err := RecoverSegmentSet(reopened, map[uint64]PageStore{7: store})
	if err != nil {
		t.Fatalf("RecoverSegmentSet() error = %v", err)
	}
	if recovery.Transactions != 1 || recovery.LastCommitLSN != deltaReceipt.CommitLSN ||
		string(store.pages[1].Payload) != "aZc" {
		t.Fatalf("recovery = %#v, Page = %#v", recovery, store.pages[1])
	}
}

func TestSegmentReclaimRequiresCheckpointInLaterSegment(t *testing.T) {
	t.Parallel()

	set, err := CreateSegmentSet(filepath.Join(t.TempDir(), "redo"), 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	if _, err := set.Commit(1, []Record{{Type: TypePageInit}}); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if _, err := set.Reclaim(); !errors.Is(err, ErrNoReclaimableSegments) {
		t.Fatalf("Reclaim(no checkpoint) error = %v", err)
	}
	if _, err := set.PublishCheckpoint(&recordingBarrier{}); err != nil {
		t.Fatalf("PublishCheckpoint() error = %v", err)
	}
	if _, err := set.Reclaim(); !errors.Is(err, ErrNoReclaimableSegments) {
		t.Fatalf("Reclaim(same Segment) error = %v", err)
	}
}

func TestSegmentReclaimPublishFaultsKeepOldSegments(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"write", "rename", "directory sync"} {
		name := name
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, _, _, _ := reclaimableSet(t, directory)
			injected := errors.New("injected " + name + " failure")
			switch name {
			case "write":
				set.writeManifestTemp = func(string, []byte) (string, error) {
					return "", injected
				}
			case "rename":
				set.renameManifest = func(string, string) error { return injected }
			case "directory sync":
				set.syncSetDirectory = func(string) error { return injected }
			}
			if _, err := set.Reclaim(); !errors.Is(err, injected) {
				t.Fatalf("Reclaim() error = %v, want injected", err)
			}
			for id := uint64(1); id <= 2; id++ {
				if _, err := os.Stat(segmentPath(directory, id)); err != nil {
					t.Fatalf("Segment %d was removed: %v", id, err)
				}
			}
		})
	}
}

func TestSegmentReclaimDeleteFailureIsRecoverable(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "redo")
	set, _, checkpoint, _ := reclaimableSet(t, directory)
	injected := errors.New("injected delete failure")
	set.removeSegmentFile = func(string) error { return injected }
	report, err := set.Reclaim()
	if !errors.Is(err, injected) {
		t.Fatalf("Reclaim() error = %v, want injected", err)
	}
	if report.FirstRetainedSegmentID != 2 || report.ResidualSegments != 1 {
		t.Fatalf("Reclaim() report = %#v", report)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet(residual) error = %v", err)
	}
	if got, exists, err := reopened.LatestCheckpoint(); err != nil || !exists || got != checkpoint {
		t.Fatalf("LatestCheckpoint() = %#v, %t, %v", got, exists, err)
	}
	if report, err := reopened.Reclaim(); err != nil {
		t.Fatalf("Reclaim(retry) error = %v", err)
	} else if report.DeletedSegments != 1 || report.ResidualSegments != 0 {
		t.Fatalf("Reclaim(retry) report = %#v", report)
	}
	_ = reopened.Close()
}

func TestSegmentReclaimFinalDirectorySyncFailureRemainsReopenable(t *testing.T) {
	t.Parallel()

	directory := filepath.Join(t.TempDir(), "redo")
	set, _, checkpoint, _ := reclaimableSet(t, directory)
	injected := errors.New("injected final directory sync failure")
	syncCalls := 0
	set.syncSetDirectory = func(path string) error {
		syncCalls++
		if syncCalls == 2 {
			return injected
		}
		return syncDirectory(path)
	}
	report, err := set.Reclaim()
	if !errors.Is(err, injected) {
		t.Fatalf("Reclaim() error = %v, want injected", err)
	}
	if report.DeletedSegments != 1 || report.ResidualSegments != 0 {
		t.Fatalf("Reclaim() report = %#v", report)
	}
	if err := set.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("OpenSegmentSet() error = %v", err)
	}
	defer func() { _ = reopened.Close() }()
	if got, exists, err := reopened.LatestCheckpoint(); err != nil || !exists || got != checkpoint {
		t.Fatalf("LatestCheckpoint() = %#v, %t, %v", got, exists, err)
	}
}

func TestPersistManifestFileWriteAndSyncFaults(t *testing.T) {
	t.Parallel()

	encoded := encodeRetainedManifest(retainedManifest{
		FirstSegmentID:           2,
		FirstStartLSN:            100,
		ReclaimedTransactionHigh: 1,
		Checkpoint: Checkpoint{
			ID: 1, RecoveryLSN: 90, CoveredSegmentID: 1,
			RecordLSN: 164, DurableLSN: 252,
		},
	})
	for _, name := range []string{"write", "sync"} {
		name := name
		t.Run(name, func(t *testing.T) {
			file, err := os.CreateTemp(t.TempDir(), "manifest")
			if err != nil {
				t.Fatalf("CreateTemp() error = %v", err)
			}
			injected := errors.New("injected manifest " + name + " failure")
			fault := &transactionFaultFile{segmentFile: file}
			if name == "write" {
				fault.failWriteCall = 1
				fault.writeErr = injected
			} else {
				fault.syncErr = injected
			}
			if err := persistManifestFile(fault, encoded); !errors.Is(err, injected) {
				t.Fatalf("persistManifestFile() error = %v, want injected", err)
			}
			_ = file.Close()
		})
	}
}

func TestOpenSegmentSetRejectsCorruptReclaimManifestAndMissingRetainedSegment(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"manifest bit flip",
		"forged checkpoint",
		"zero high-water",
		"missing retained",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "redo")
			set, _, _, _ := reclaimableSet(t, directory)
			if _, err := set.Reclaim(); err != nil {
				t.Fatalf("Reclaim() error = %v", err)
			}
			if err := set.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			switch name {
			case "manifest bit flip":
				path := filepath.Join(directory, segmentManifestName)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				data[24] ^= 0xff
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			case "forged checkpoint", "zero high-water":
				path := filepath.Join(directory, segmentManifestName)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("ReadFile() error = %v", err)
				}
				manifest, err := decodeRetainedManifest(data)
				if err != nil {
					t.Fatalf("decodeRetainedManifest() error = %v", err)
				}
				if name == "forged checkpoint" {
					manifest.Checkpoint.ID++
				} else {
					manifest.ReclaimedTransactionHigh = 0
				}
				if err := os.WriteFile(path, encodeRetainedManifest(manifest), 0o600); err != nil {
					t.Fatalf("WriteFile() error = %v", err)
				}
			case "missing retained":
				if err := os.Remove(segmentPath(directory, 2)); err != nil {
					t.Fatalf("Remove() error = %v", err)
				}
			}
			if opened, err := OpenSegmentSet(directory, 0); !errors.Is(err, ErrCorrupt) {
				if opened != nil {
					_ = opened.Close()
				}
				t.Fatalf("OpenSegmentSet() error = %v, want ErrCorrupt", err)
			}
		})
	}
}

func reclaimableSet(
	t *testing.T,
	directory string,
) (*SegmentSet, Receipt, Checkpoint, Receipt) {
	t.Helper()
	set, err := CreateSegmentSet(directory, 0)
	if err != nil {
		t.Fatalf("CreateSegmentSet() error = %v", err)
	}
	t.Cleanup(func() { _ = set.Close() })
	initReceipt, err := set.Commit(1, []Record{{
		Type: TypePageInit, SpaceID: 7, PageID: 1,
		Payload: mustPageImage(t, page.Page{
			Header:  page.Header{Type: page.TypeData, SpaceID: 7, PageID: 1},
			Payload: []byte("abc"),
		}),
	}})
	if err != nil {
		t.Fatalf("Commit(init) error = %v", err)
	}
	if _, err := set.Roll(); err != nil {
		t.Fatalf("Roll() error = %v", err)
	}
	checkpoint, err := set.PublishCheckpoint(&recordingBarrier{})
	if err != nil {
		t.Fatalf("PublishCheckpoint() error = %v", err)
	}
	delta, err := EncodePageDelta(1, []byte("Z"))
	if err != nil {
		t.Fatalf("EncodePageDelta() error = %v", err)
	}
	deltaReceipt, err := set.Commit(2, []Record{{
		Type: TypePageDelta, SpaceID: 7, PageID: 1, Payload: delta,
	}})
	if err != nil {
		t.Fatalf("Commit(delta) error = %v", err)
	}
	return set, initReceipt, checkpoint, deltaReceipt
}
