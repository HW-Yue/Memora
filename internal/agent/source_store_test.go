package agent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestSourceStorePutReopenAndVerifiedOpen(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("an entire temporary source body\nwith deterministic bytes")
	request := sourcePutRequest("job-book", "source-book", payload)
	receipt, err := store.Put(context.Background(), request, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replayed || receipt.ObjectReused || receipt.Reference.SHA256 != request.ExpectedSHA256 ||
		receipt.Reference.Bytes != uint64(len(payload)) || receipt.Reference.Validate() != nil {
		t.Fatalf("receipt = %#v, Validate=%v", receipt, receipt.Reference.Validate())
	}
	reopened, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	reference, reader, err := reopened.Open("job-book", "source-book")
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		t.Fatalf("read=%v close=%v", readErr, closeErr)
	}
	if !bytes.Equal(got, payload) || reference.MediaType != "application/epub+zip" ||
		reference.FileName != "book.epub" {
		t.Fatalf("reference=%#v payload=%q", reference, got)
	}
	assertSourceStorePermissions(t, root)
}

func TestSourceStoreReplaysDeduplicatesAndReleasesLastReference(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("shared source object")
	jobOne := sourcePutRequest("job-one", "source-shared", payload)
	first, err := store.Put(context.Background(), jobOne, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := store.Put(context.Background(), jobOne, bytes.NewReader([]byte("not consumed")))
	if err != nil {
		t.Fatal(err)
	}
	if !replay.Replayed || replay.ObjectReused || replay.Reference != first.Reference {
		t.Fatalf("replay = %#v, first = %#v", replay, first)
	}
	jobTwo := sourcePutRequest("job-two", "source-shared", payload)
	second, err := store.Put(context.Background(), jobTwo, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	if second.Replayed || !second.ObjectReused {
		t.Fatalf("second = %#v", second)
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Objects != 1 || stats.References != 2 || stats.PhysicalBytes != uint64(len(payload)) {
		t.Fatalf("stats = %#v", stats)
	}
	conflictPayload := []byte("different source object")
	conflict := sourcePutRequest("job-one", "source-shared", conflictPayload)
	if _, err := store.Put(context.Background(), conflict, bytes.NewReader(conflictPayload)); !errors.Is(err, agent.ErrSourceStoreConflict) {
		t.Fatalf("conflicting Put error = %v", err)
	}
	releasedOne, err := store.ReleaseJob("job-one")
	if err != nil {
		t.Fatal(err)
	}
	if releasedOne.ReferencesRemoved != 1 || releasedOne.ObjectsRemoved != 0 || releasedOne.BytesRemoved != 0 {
		t.Fatalf("released job one = %#v", releasedOne)
	}
	if _, reader, err := store.Open("job-two", "source-shared"); err != nil {
		t.Fatal(err)
	} else if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if replayRelease, err := store.ReleaseJob("job-one"); err != nil || replayRelease != (agent.SourceCleanupReceipt{}) {
		t.Fatalf("replayed release = %#v, error = %v", replayRelease, err)
	}
	releasedTwo, err := store.ReleaseJob("job-two")
	if err != nil {
		t.Fatal(err)
	}
	if releasedTwo.ReferencesRemoved != 1 || releasedTwo.ObjectsRemoved != 1 ||
		releasedTwo.BytesRemoved != uint64(len(payload)) {
		t.Fatalf("released job two = %#v", releasedTwo)
	}
	stats, err = store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != (agent.SourceStoreStats{}) {
		t.Fatalf("stats after release = %#v", stats)
	}
}

func TestSourceStoreEnforcesObjectJobPhysicalAndSourceQuotas(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config agent.SourceStoreConfig
		run    func(*testing.T, *agent.SourceStore)
	}{
		{
			name: "object bytes",
			config: agent.SourceStoreConfig{
				MaxObjectBytes: 4, MaxJobBytes: 100, MaxPhysicalBytes: 100, MaxSourcesPerJob: 10,
			},
			run: func(t *testing.T, store *agent.SourceStore) {
				payload := []byte("12345")
				_, err := store.Put(context.Background(), sourcePutRequest("job", "one", payload), bytes.NewReader(payload))
				if !errors.Is(err, agent.ErrSourceStoreQuota) {
					t.Fatalf("Put error = %v", err)
				}
			},
		},
		{
			name: "job bytes",
			config: agent.SourceStoreConfig{
				MaxObjectBytes: 10, MaxJobBytes: 6, MaxPhysicalBytes: 100, MaxSourcesPerJob: 10,
			},
			run: func(t *testing.T, store *agent.SourceStore) {
				putSource(t, store, "job", "one", []byte("1234"))
				payload := []byte("5678")
				_, err := store.Put(context.Background(), sourcePutRequest("job", "two", payload), bytes.NewReader(payload))
				if !errors.Is(err, agent.ErrSourceStoreQuota) {
					t.Fatalf("Put error = %v", err)
				}
			},
		},
		{
			name: "physical bytes",
			config: agent.SourceStoreConfig{
				MaxObjectBytes: 10, MaxJobBytes: 100, MaxPhysicalBytes: 6, MaxSourcesPerJob: 10,
			},
			run: func(t *testing.T, store *agent.SourceStore) {
				putSource(t, store, "job-one", "one", []byte("1234"))
				payload := []byte("5678")
				_, err := store.Put(context.Background(), sourcePutRequest("job-two", "two", payload), bytes.NewReader(payload))
				if !errors.Is(err, agent.ErrSourceStoreQuota) {
					t.Fatalf("Put error = %v", err)
				}
				shared := []byte("1234")
				if _, err := store.Put(context.Background(), sourcePutRequest("job-two", "shared", shared), bytes.NewReader(shared)); err != nil {
					t.Fatalf("dedupe within physical quota: %v", err)
				}
			},
		},
		{
			name: "sources per job",
			config: agent.SourceStoreConfig{
				MaxObjectBytes: 10, MaxJobBytes: 100, MaxPhysicalBytes: 100, MaxSourcesPerJob: 1,
			},
			run: func(t *testing.T, store *agent.SourceStore) {
				putSource(t, store, "job", "one", []byte("1234"))
				payload := []byte("1234")
				_, err := store.Put(context.Background(), sourcePutRequest("job", "two", payload), bytes.NewReader(payload))
				if !errors.Is(err, agent.ErrSourceStoreQuota) {
					t.Fatalf("Put error = %v", err)
				}
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store, err := agent.OpenSourceStore(t.TempDir(), test.config)
			if err != nil {
				t.Fatal(err)
			}
			test.run(t, store)
		})
	}
}

func TestSourceStoreRejectsDigestAndReaderFaultWithoutVisibleReference(t *testing.T) {
	t.Parallel()

	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("actual bytes")
	mismatch := sourcePutRequest("job", "mismatch", []byte("expected bytes"))
	if _, err := store.Put(context.Background(), mismatch, bytes.NewReader(payload)); !errors.Is(err, agent.ErrSourceStoreDigest) {
		t.Fatalf("digest mismatch error = %v", err)
	}
	fault := sourcePutRequest("job", "fault", payload)
	if _, err := store.Put(context.Background(), fault, &faultingSourceReader{data: payload, err: io.ErrUnexpectedEOF}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("reader fault error = %v", err)
	}
	for _, sourceID := range []string{"mismatch", "fault"} {
		if _, err := store.Resolve("job", sourceID); !errors.Is(err, agent.ErrSourceStoreNotFound) {
			t.Fatalf("Resolve(%q) error = %v", sourceID, err)
		}
	}
	stats, err := store.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stats != (agent.SourceStoreStats{}) {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestSourceStoreConcurrentPutHasOneCommitAndReplays(t *testing.T) {
	t.Parallel()

	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("one concurrently uploaded source")
	request := sourcePutRequest("job-race", "source-race", payload)
	const contenders = 16
	start := make(chan struct{})
	receipts := make(chan agent.SourcePutReceipt, contenders)
	errorsCh := make(chan error, contenders)
	var wait sync.WaitGroup
	for range contenders {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			receipt, putErr := store.Put(context.Background(), request, bytes.NewReader(payload))
			receipts <- receipt
			errorsCh <- putErr
		}()
	}
	close(start)
	wait.Wait()
	close(receipts)
	close(errorsCh)
	for putErr := range errorsCh {
		if putErr != nil {
			t.Fatalf("concurrent Put error = %v", putErr)
		}
	}
	committed, replayed := 0, 0
	for receipt := range receipts {
		if receipt.Replayed {
			replayed++
		} else {
			committed++
		}
	}
	if committed != 1 || replayed != contenders-1 {
		t.Fatalf("committed=%d replayed=%d", committed, replayed)
	}
}

func TestSourceStoreReopenCleansResidueAndCorruptionFailsClosed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("outside must survive"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("verified object")
	putSource(t, store, "job-corrupt", "source", payload)
	objectPath := onlySourceObjectPath(t, root)
	if err := os.WriteFile(filepath.Join(root, "staging", "crash.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	orphanPayload := []byte("unreferenced")
	orphanDigest := sourceDigest(orphanPayload)
	orphanPath := filepath.Join(root, "objects", strings.TrimPrefix(orphanDigest, "sha256:")+".source")
	if err := os.WriteFile(orphanPath, orphanPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := agent.OpenSourceStore(root, testSourceStoreConfig()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "staging", "crash.tmp")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staging residue stat error = %v", err)
	}
	if _, err := os.Stat(orphanPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan object stat error = %v", err)
	}
	if err := os.WriteFile(objectPath, []byte("tampered object"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open("job-corrupt", "source"); !errors.Is(err, agent.ErrSourceStoreCorrupt) {
		t.Fatalf("Open tampered object error = %v", err)
	}
	if err := os.Remove(objectPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, objectPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Open("job-corrupt", "source"); !errors.Is(err, agent.ErrSourceStoreCorrupt) {
		t.Fatalf("Open symlink object error = %v", err)
	}
	if _, err := store.ReleaseJob("job-corrupt"); err != nil {
		t.Fatal(err)
	}
	outsideData, err := os.ReadFile(outside)
	if err != nil || string(outsideData) != "outside must survive" {
		t.Fatalf("outside file = %q, error = %v", outsideData, err)
	}
}

type faultingSourceReader struct {
	data []byte
	err  error
	done bool
}

func (reader *faultingSourceReader) Read(target []byte) (int, error) {
	if !reader.done {
		reader.done = true
		return copy(target, reader.data), nil
	}
	return 0, reader.err
}

func testSourceStoreConfig() agent.SourceStoreConfig {
	return agent.SourceStoreConfig{
		MaxObjectBytes: 1 << 20, MaxJobBytes: 2 << 20,
		MaxPhysicalBytes: 4 << 20, MaxSourcesPerJob: 32,
	}
}

func sourcePutRequest(jobID, sourceID string, payload []byte) agent.SourcePutRequest {
	return agent.SourcePutRequest{
		Version: agent.SourcePutRequestVersion, JobID: jobID, SourceID: sourceID,
		ExpectedSHA256: sourceDigest(payload), MediaType: "application/epub+zip", FileName: "book.epub",
	}
}

func putSource(t *testing.T, store *agent.SourceStore, jobID, sourceID string, payload []byte) agent.SourcePutReceipt {
	t.Helper()
	receipt, err := store.Put(context.Background(), sourcePutRequest(jobID, sourceID, payload), bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func sourceDigest(payload []byte) string {
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func assertSourceStorePermissions(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := os.FileMode(0o600)
		if entry.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Errorf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func onlySourceObjectPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "objects"))
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".source") {
			paths = append(paths, filepath.Join(root, "objects", entry.Name()))
		}
	}
	if len(paths) != 1 {
		t.Fatalf("object paths = %v", paths)
	}
	return paths[0]
}
