package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
)

const (
	sourceObjectSuffix    = ".source"
	sourceReferenceSuffix = ".json"
	sourceStreamBuffer    = 32 << 10
)

type SourceStore struct {
	mu      sync.Mutex
	root    string
	objects string
	refs    string
	staging string
	config  SourceStoreConfig
	state   sourceStoreState
}

type sourceStoreState struct {
	references map[string]map[string]SourceReference
	objects    map[string]uint64
}

func OpenSourceStore(root string, config SourceStoreConfig) (*SourceStore, error) {
	if root == "" || !validSourceStoreConfig(config) {
		return nil, ErrSourceStoreInvalid
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrSourceStoreInvalid, err)
	}
	store := &SourceStore{
		root: absolute, objects: filepath.Join(absolute, "objects"),
		refs: filepath.Join(absolute, "refs"), staging: filepath.Join(absolute, "staging"),
		config: config,
	}
	for _, directory := range []string{store.root, store.objects, store.refs, store.staging} {
		if err := secureSourceDirectory(directory); err != nil {
			return nil, err
		}
	}
	if err := store.cleanStaging(); err != nil {
		return nil, err
	}
	state, err := store.scanState(true)
	if err != nil {
		return nil, err
	}
	if !sourceStateWithinQuota(state, config) {
		return nil, ErrSourceStoreQuota
	}
	store.state = state
	return store, nil
}

func (store *SourceStore) Put(
	ctx context.Context,
	request SourcePutRequest,
	reader io.Reader,
) (SourcePutReceipt, error) {
	if store == nil || ctx == nil || reader == nil || !validSourcePutRequest(request) {
		return SourcePutReceipt{}, ErrSourceStoreInvalid
	}
	if err := ctx.Err(); err != nil {
		return SourcePutReceipt{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existing, ok := store.referenceLocked(request.JobID, request.SourceID); ok {
		if !referenceMatchesRequest(existing, request) {
			return SourcePutReceipt{}, ErrSourceStoreConflict
		}
		if _, err := store.resolveLocked(request.JobID, request.SourceID); err != nil {
			return SourcePutReceipt{}, err
		}
		return SourcePutReceipt{Reference: existing, Replayed: true}, nil
	}
	jobReferences := store.state.references[request.JobID]
	if uint64(len(jobReferences)) >= store.config.MaxSourcesPerJob {
		return SourcePutReceipt{}, ErrSourceStoreQuota
	}

	temporary, err := os.CreateTemp(store.staging, "put-*.tmp")
	if err != nil {
		return SourcePutReceipt{}, fmt.Errorf("stage Source: %w", err)
	}
	temporaryPath := temporary.Name()
	keepTemporary := false
	defer func() {
		if !keepTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return SourcePutReceipt{}, fmt.Errorf("secure staged Source: %w", err)
	}
	digest := sha256.New()
	written, copyErr := copySourceWithLimit(ctx, temporary, digest, reader, store.config.MaxObjectBytes)
	if copyErr == nil {
		copyErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if copyErr != nil {
		return SourcePutReceipt{}, fmt.Errorf("stream Source: %w", copyErr)
	}
	if closeErr != nil {
		return SourcePutReceipt{}, fmt.Errorf("close staged Source: %w", closeErr)
	}
	if written == 0 {
		return SourcePutReceipt{}, ErrSourceStoreInvalid
	}
	if written > store.config.MaxObjectBytes {
		return SourcePutReceipt{}, ErrSourceStoreQuota
	}
	actualDigest := "sha256:" + hex.EncodeToString(digest.Sum(nil))
	if actualDigest != request.ExpectedSHA256 {
		return SourcePutReceipt{}, ErrSourceStoreDigest
	}
	jobBytes := sourceJobBytes(jobReferences)
	if written > store.config.MaxJobBytes || jobBytes > store.config.MaxJobBytes-written {
		return SourcePutReceipt{}, ErrSourceStoreQuota
	}
	objectBytes, objectExists := store.state.objects[actualDigest]
	if objectExists {
		if objectBytes != written {
			return SourcePutReceipt{}, ErrSourceStoreCorrupt
		}
		if err := store.verifyObject(actualDigest, written); err != nil {
			return SourcePutReceipt{}, err
		}
	} else {
		physicalBytes := sourcePhysicalBytes(store.state.objects)
		if written > store.config.MaxPhysicalBytes || physicalBytes > store.config.MaxPhysicalBytes-written {
			return SourcePutReceipt{}, ErrSourceStoreQuota
		}
		if err := store.publishObject(temporaryPath, actualDigest, written); err != nil {
			return SourcePutReceipt{}, err
		}
		keepTemporary = true
	}
	reference := SourceReference{
		Version: SourceReferenceVersion, JobID: request.JobID, SourceID: request.SourceID,
		SHA256: actualDigest, Bytes: written, MediaType: request.MediaType, FileName: request.FileName,
	}
	if err := store.publishReference(reference); err != nil {
		if !objectExists {
			_ = os.Remove(store.objectPath(actualDigest))
		}
		return SourcePutReceipt{}, err
	}
	if jobReferences == nil {
		jobReferences = make(map[string]SourceReference)
		store.state.references[request.JobID] = jobReferences
	}
	jobReferences[request.SourceID] = reference
	if !objectExists {
		store.state.objects[actualDigest] = written
	}
	return SourcePutReceipt{Reference: reference, ObjectReused: objectExists}, nil
}

func (store *SourceStore) Resolve(jobID, sourceID string) (SourceReference, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) || !validSourceIntakeID(sourceID, 128) {
		return SourceReference{}, ErrSourceStoreInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.resolveLocked(jobID, sourceID)
}

func (store *SourceStore) Open(
	jobID string,
	sourceID string,
) (SourceReference, SourceReadCloser, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) || !validSourceIntakeID(sourceID, 128) {
		return SourceReference{}, nil, ErrSourceStoreInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	reference, err := store.resolveLocked(jobID, sourceID)
	if err != nil {
		return SourceReference{}, nil, err
	}
	file, err := os.Open(store.objectPath(reference.SHA256))
	if err != nil {
		return SourceReference{}, nil, fmt.Errorf("open Source object: %w", err)
	}
	return reference, file, nil
}

func (store *SourceStore) Stats() (SourceStoreStats, error) {
	if store == nil {
		return SourceStoreStats{}, ErrSourceStoreInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return sourceStateStats(store.state), nil
}

func (store *SourceStore) ReleaseJob(jobID string) (SourceCleanupReceipt, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) {
		return SourceCleanupReceipt{}, ErrSourceStoreInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	references := store.state.references[jobID]
	if len(references) == 0 {
		return SourceCleanupReceipt{}, nil
	}
	jobPath := store.jobReferencePath(jobID)
	for sourceID := range references {
		path := store.referencePath(jobID, sourceID)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SourceCleanupReceipt{}, fmt.Errorf("remove Source reference: %w", err)
		}
	}
	if err := os.Remove(jobPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return SourceCleanupReceipt{}, fmt.Errorf("remove Source job directory: %w", err)
	}
	delete(store.state.references, jobID)
	receipt := SourceCleanupReceipt{ReferencesRemoved: uint64(len(references))}
	for digest, size := range uniqueSourceObjects(references) {
		if sourceDigestReferenced(store.state.references, digest) {
			continue
		}
		if err := os.Remove(store.objectPath(digest)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return SourceCleanupReceipt{}, fmt.Errorf("remove Source object: %w", err)
		}
		delete(store.state.objects, digest)
		receipt.ObjectsRemoved++
		receipt.BytesRemoved += size
	}
	if err := syncSourceDirectory(store.refs); err != nil {
		return SourceCleanupReceipt{}, err
	}
	if err := syncSourceDirectory(store.objects); err != nil {
		return SourceCleanupReceipt{}, err
	}
	return receipt, nil
}

func (store *SourceStore) referenceLocked(jobID, sourceID string) (SourceReference, bool) {
	job := store.state.references[jobID]
	if job == nil {
		return SourceReference{}, false
	}
	reference, ok := job[sourceID]
	return reference, ok
}

func (store *SourceStore) resolveLocked(jobID, sourceID string) (SourceReference, error) {
	want, ok := store.referenceLocked(jobID, sourceID)
	if !ok {
		return SourceReference{}, ErrSourceStoreNotFound
	}
	got, err := readSourceReference(store.referencePath(jobID, sourceID))
	if err != nil || !reflect.DeepEqual(got, want) {
		return SourceReference{}, ErrSourceStoreCorrupt
	}
	if err := store.verifyObject(got.SHA256, got.Bytes); err != nil {
		return SourceReference{}, err
	}
	return got, nil
}

func (store *SourceStore) publishObject(path, digest string, size uint64) error {
	target := store.objectPath(digest)
	if err := os.Link(path, target); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("publish Source object: %w", err)
		}
		if err := store.verifyObject(digest, size); err != nil {
			return err
		}
	} else if err := os.Chmod(target, 0o600); err != nil {
		return fmt.Errorf("secure Source object: %w", err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove staged Source: %w", err)
	}
	if err := syncSourceDirectory(store.objects); err != nil {
		return err
	}
	return nil
}

func (store *SourceStore) publishReference(reference SourceReference) error {
	jobPath := store.jobReferencePath(reference.JobID)
	if err := secureSourceDirectory(jobPath); err != nil {
		return err
	}
	encoded, err := json.Marshal(reference)
	if err != nil {
		return ErrSourceStoreInvalid
	}
	temporary, err := os.CreateTemp(jobPath, "ref-*.tmp")
	if err != nil {
		return fmt.Errorf("stage Source reference: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("secure Source reference: %w", err)
	}
	if _, err := temporary.Write(encoded); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write Source reference: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync Source reference: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close Source reference: %w", err)
	}
	if err := os.Rename(temporaryPath, store.referencePath(reference.JobID, reference.SourceID)); err != nil {
		return fmt.Errorf("publish Source reference: %w", err)
	}
	return syncSourceDirectory(jobPath)
}

func (store *SourceStore) verifyObject(digest string, size uint64) error {
	path := store.objectPath(digest)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 || uint64(info.Size()) != size {
		return ErrSourceStoreCorrupt
	}
	file, err := os.Open(path)
	if err != nil {
		return ErrSourceStoreCorrupt
	}
	hasher := sha256.New()
	_, copyErr := io.CopyBuffer(hasher, file, make([]byte, sourceStreamBuffer))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || "sha256:"+hex.EncodeToString(hasher.Sum(nil)) != digest {
		return ErrSourceStoreCorrupt
	}
	return nil
}

func (store *SourceStore) cleanStaging() error {
	entries, err := os.ReadDir(store.staging)
	if err != nil {
		return fmt.Errorf("read Source staging: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return ErrSourceStoreCorrupt
		}
		if err := os.Remove(filepath.Join(store.staging, entry.Name())); err != nil {
			return fmt.Errorf("clean Source staging: %w", err)
		}
	}
	return syncSourceDirectory(store.staging)
}

func (store *SourceStore) scanState(cleanOrphans bool) (sourceStoreState, error) {
	state := sourceStoreState{
		references: make(map[string]map[string]SourceReference), objects: make(map[string]uint64),
	}
	referencedObjects := make(map[string]uint64)
	jobEntries, err := os.ReadDir(store.refs)
	if err != nil {
		return state, fmt.Errorf("read Source references: %w", err)
	}
	for _, jobEntry := range jobEntries {
		if !jobEntry.IsDir() || !validSourceDiskHash(jobEntry.Name()) {
			return state, ErrSourceStoreCorrupt
		}
		jobPath := filepath.Join(store.refs, jobEntry.Name())
		refEntries, err := os.ReadDir(jobPath)
		if err != nil {
			return state, ErrSourceStoreCorrupt
		}
		if len(refEntries) == 0 {
			if cleanOrphans {
				if err := os.Remove(jobPath); err != nil {
					return state, fmt.Errorf("remove empty Source reference directory: %w", err)
				}
			}
			continue
		}
		for _, refEntry := range refEntries {
			if refEntry.IsDir() || !strings.HasSuffix(refEntry.Name(), sourceReferenceSuffix) ||
				!validSourceDiskHash(strings.TrimSuffix(refEntry.Name(), sourceReferenceSuffix)) {
				return state, ErrSourceStoreCorrupt
			}
			reference, err := readSourceReference(filepath.Join(jobPath, refEntry.Name()))
			if err != nil || sourceDiskHash(reference.JobID) != jobEntry.Name() ||
				sourceDiskHash(reference.SourceID)+sourceReferenceSuffix != refEntry.Name() {
				return state, ErrSourceStoreCorrupt
			}
			job := state.references[reference.JobID]
			if job == nil {
				job = make(map[string]SourceReference)
				state.references[reference.JobID] = job
			}
			if _, duplicate := job[reference.SourceID]; duplicate {
				return state, ErrSourceStoreCorrupt
			}
			if size, exists := referencedObjects[reference.SHA256]; exists && size != reference.Bytes {
				return state, ErrSourceStoreCorrupt
			}
			referencedObjects[reference.SHA256] = reference.Bytes
			job[reference.SourceID] = reference
		}
	}
	objectEntries, err := os.ReadDir(store.objects)
	if err != nil {
		return state, fmt.Errorf("read Source objects: %w", err)
	}
	for _, objectEntry := range objectEntries {
		name := objectEntry.Name()
		if objectEntry.IsDir() || !strings.HasSuffix(name, sourceObjectSuffix) ||
			!validSourceDiskHash(strings.TrimSuffix(name, sourceObjectSuffix)) {
			return state, ErrSourceStoreCorrupt
		}
		digest := "sha256:" + strings.TrimSuffix(name, sourceObjectSuffix)
		referencedSize, referenced := referencedObjects[digest]
		path := filepath.Join(store.objects, name)
		if !referenced {
			if cleanOrphans {
				if err := os.Remove(path); err != nil {
					return state, fmt.Errorf("remove orphan Source object: %w", err)
				}
			}
			continue
		}
		if err := store.verifyObject(digest, referencedSize); err != nil {
			return state, err
		}
		state.objects[digest] = referencedSize
	}
	for digest := range referencedObjects {
		if _, exists := state.objects[digest]; !exists {
			return state, ErrSourceStoreCorrupt
		}
	}
	return state, nil
}

func (store *SourceStore) objectPath(digest string) string {
	return filepath.Join(store.objects, strings.TrimPrefix(digest, "sha256:")+sourceObjectSuffix)
}

func (store *SourceStore) jobReferencePath(jobID string) string {
	return filepath.Join(store.refs, sourceDiskHash(jobID))
}

func (store *SourceStore) referencePath(jobID, sourceID string) string {
	return filepath.Join(store.jobReferencePath(jobID), sourceDiskHash(sourceID)+sourceReferenceSuffix)
}

func readSourceReference(path string) (SourceReference, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm() != 0o600 || info.Size() > 16<<10 {
		return SourceReference{}, ErrSourceStoreCorrupt
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return SourceReference{}, ErrSourceStoreCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var reference SourceReference
	if err := decoder.Decode(&reference); err != nil || reference.Validate() != nil {
		return SourceReference{}, ErrSourceStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return SourceReference{}, ErrSourceStoreCorrupt
	}
	return reference, nil
}

func secureSourceDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("open Source Store directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrSourceStoreCorrupt
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure Source Store directory: %w", err)
	}
	return nil
}

func syncSourceDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync Source Store directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Source Store directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Source Store directory: %w", closeErr)
	}
	return nil
}

func copySourceWithLimit(
	ctx context.Context,
	target io.Writer,
	digest hash.Hash,
	source io.Reader,
	limit uint64,
) (uint64, error) {
	limited := io.LimitReader(&sourceContextReader{ctx: ctx, reader: source}, int64(limit)+1)
	written, err := io.CopyBuffer(io.MultiWriter(target, digest), limited, make([]byte, sourceStreamBuffer))
	return uint64(written), err
}

type sourceContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader *sourceContextReader) Read(target []byte) (int, error) {
	select {
	case <-reader.ctx.Done():
		return 0, reader.ctx.Err()
	default:
		return reader.reader.Read(target)
	}
}

func referenceMatchesRequest(reference SourceReference, request SourcePutRequest) bool {
	return reference.JobID == request.JobID && reference.SourceID == request.SourceID &&
		reference.SHA256 == request.ExpectedSHA256 && reference.MediaType == request.MediaType &&
		reference.FileName == request.FileName
}

func sourceDiskHash(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func validSourceDiskHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func sourceJobBytes(references map[string]SourceReference) uint64 {
	var total uint64
	for _, reference := range references {
		total += reference.Bytes
	}
	return total
}

func sourcePhysicalBytes(objects map[string]uint64) uint64 {
	var total uint64
	for _, size := range objects {
		total += size
	}
	return total
}

func sourceStateStats(state sourceStoreState) SourceStoreStats {
	stats := SourceStoreStats{Objects: uint64(len(state.objects)), PhysicalBytes: sourcePhysicalBytes(state.objects)}
	for _, references := range state.references {
		stats.References += uint64(len(references))
		stats.LogicalBytes += sourceJobBytes(references)
	}
	return stats
}

func sourceStateWithinQuota(state sourceStoreState, config SourceStoreConfig) bool {
	stats := sourceStateStats(state)
	if stats.PhysicalBytes > config.MaxPhysicalBytes {
		return false
	}
	for _, references := range state.references {
		if uint64(len(references)) > config.MaxSourcesPerJob || sourceJobBytes(references) > config.MaxJobBytes {
			return false
		}
		for _, reference := range references {
			if reference.Bytes > config.MaxObjectBytes {
				return false
			}
		}
	}
	return true
}

func sourceDigestReferenced(jobs map[string]map[string]SourceReference, digest string) bool {
	for _, references := range jobs {
		for _, reference := range references {
			if reference.SHA256 == digest {
				return true
			}
		}
	}
	return false
}

func uniqueSourceObjects(references map[string]SourceReference) map[string]uint64 {
	objects := make(map[string]uint64)
	for _, reference := range references {
		objects[reference.SHA256] = reference.Bytes
	}
	return objects
}
