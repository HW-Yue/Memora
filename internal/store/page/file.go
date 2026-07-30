package page

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

const (
	spaceManifestVersion     = uint16(1)
	spaceManifestPayloadSize = 16
)

var (
	ErrClosed   = errors.New("page file manager is closed")
	ErrNotFound = errors.New("page is not allocated")
)

var spaceManifestMagic = [8]byte{'M', 'E', 'M', 'S', 'P', 'C', '0', '1'}

type pageFile interface {
	ReadAt([]byte, int64) (int, error)
	WriteAt([]byte, int64) (int, error)
	Sync() error
	Stat() (os.FileInfo, error)
	Close() error
}

type Manager struct {
	mu         sync.RWMutex
	file       pageFile
	spaceID    uint64
	nextPageID uint64
	closed     bool
}

func Create(path string, spaceID uint64) (*Manager, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalid)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create Page file: %w", err)
	}
	removeOnError := true
	defer func() {
		if removeOnError {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()

	manifest, err := Encode(spaceManifest(spaceID))
	if err != nil {
		return nil, fmt.Errorf("encode space manifest: %w", err)
	}
	if err := writeFullAt(file, manifest, 0); err != nil {
		return nil, fmt.Errorf("write space manifest: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync space manifest: %w", err)
	}
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		return nil, fmt.Errorf("sync Page file directory: %w", err)
	}

	removeOnError = false
	return &Manager{file: file, spaceID: spaceID, nextPageID: 1}, nil
}

func Open(path string, expectedSpaceID uint64) (*Manager, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: path is required", ErrInvalid)
	}
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open Page file: %w", err)
	}

	manager, err := openManager(file, expectedSpaceID)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return manager, nil
}

func openManager(file pageFile, expectedSpaceID uint64) (*Manager, error) {
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat Page file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() < Size || info.Size()%Size != 0 {
		return nil, fmt.Errorf("%w: invalid Page file size or type", ErrCorrupt)
	}

	encoded := make([]byte, Size)
	if err := readFullAt(file, encoded, 0); err != nil {
		return nil, fmt.Errorf("read space manifest: %w", err)
	}
	manifest, err := Decode(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode space manifest: %w", err)
	}
	if err := validateSpaceManifest(manifest, expectedSpaceID); err != nil {
		return nil, err
	}

	return &Manager{
		file:       file,
		spaceID:    expectedSpaceID,
		nextPageID: uint64(info.Size() / Size),
	}, nil
}

func (manager *Manager) Read(pageID uint64) (Page, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	if manager.closed {
		return Page{}, ErrClosed
	}
	offset, err := pageOffset(pageID)
	if err != nil {
		return Page{}, err
	}
	if pageID >= manager.nextPageID {
		return Page{}, fmt.Errorf("%w: page %d", ErrNotFound, pageID)
	}

	encoded := make([]byte, Size)
	if err := readFullAt(manager.file, encoded, offset); err != nil {
		return Page{}, fmt.Errorf("read Page %d: %w", pageID, err)
	}
	value, err := Decode(encoded)
	if err != nil {
		return Page{}, fmt.Errorf("decode Page %d: %w", pageID, err)
	}
	if value.Header.SpaceID != manager.spaceID || value.Header.PageID != pageID {
		return Page{}, fmt.Errorf(
			"%w: Page identity is space=%d page=%d, want space=%d page=%d",
			ErrCorrupt,
			value.Header.SpaceID,
			value.Header.PageID,
			manager.spaceID,
			pageID,
		)
	}
	return value, nil
}

func (manager *Manager) Write(value Page) error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.closed {
		return ErrClosed
	}
	if value.Header.PageID == 0 ||
		value.Header.SpaceID != manager.spaceID ||
		value.Header.PageID > manager.nextPageID {
		return fmt.Errorf(
			"%w: Page identity is space=%d page=%d, next page is %d",
			ErrInvalid,
			value.Header.SpaceID,
			value.Header.PageID,
			manager.nextPageID,
		)
	}
	offset, err := pageOffset(value.Header.PageID)
	if err != nil {
		return err
	}
	encoded, err := Encode(value)
	if err != nil {
		return err
	}
	if err := writeFullAt(manager.file, encoded, offset); err != nil {
		return fmt.Errorf("write Page %d: %w", value.Header.PageID, err)
	}
	if value.Header.PageID == manager.nextPageID {
		manager.nextPageID++
	}
	return nil
}

func (manager *Manager) Sync() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.closed {
		return ErrClosed
	}
	if err := manager.file.Sync(); err != nil {
		return fmt.Errorf("sync Page file: %w", err)
	}
	return nil
}

func (manager *Manager) PageCount() (uint64, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()

	if manager.closed {
		return 0, ErrClosed
	}
	return manager.nextPageID - 1, nil
}

func (manager *Manager) Close() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()

	if manager.closed {
		return nil
	}
	manager.closed = true
	if err := manager.file.Close(); err != nil {
		return fmt.Errorf("close Page file: %w", err)
	}
	return nil
}

func spaceManifest(spaceID uint64) Page {
	payload := make([]byte, spaceManifestPayloadSize)
	copy(payload[0:8], spaceManifestMagic[:])
	binary.LittleEndian.PutUint16(payload[8:10], spaceManifestVersion)
	binary.LittleEndian.PutUint16(payload[10:12], spaceManifestPayloadSize)
	return Page{
		Header:  Header{Type: TypeManifest, SpaceID: spaceID},
		Payload: payload,
	}
}

func validateSpaceManifest(value Page, expectedSpaceID uint64) error {
	if value.Header.Type != TypeManifest ||
		value.Header.Flags != 0 ||
		value.Header.SpaceID != expectedSpaceID ||
		value.Header.PageID != 0 ||
		value.Header.Generation != 0 ||
		value.Header.LSN != 0 {
		return fmt.Errorf("%w: invalid space manifest identity", ErrCorrupt)
	}
	if len(value.Payload) != spaceManifestPayloadSize ||
		!bytes.Equal(value.Payload[0:8], spaceManifestMagic[:]) ||
		binary.LittleEndian.Uint16(value.Payload[8:10]) != spaceManifestVersion ||
		binary.LittleEndian.Uint16(value.Payload[10:12]) != spaceManifestPayloadSize ||
		binary.LittleEndian.Uint32(value.Payload[12:16]) != 0 {
		return fmt.Errorf("%w: invalid space manifest payload", ErrCorrupt)
	}
	return nil
}

func pageOffset(pageID uint64) (int64, error) {
	if pageID == 0 || pageID > math.MaxInt64/Size {
		return 0, fmt.Errorf("%w: Page ID %d cannot be addressed", ErrInvalid, pageID)
	}
	return int64(pageID * Size), nil
}

func readFullAt(file pageFile, target []byte, offset int64) error {
	read, err := file.ReadAt(target, offset)
	if read != len(target) {
		return fmt.Errorf("%w: read %d of %d bytes", ErrCorrupt, read, len(target))
	}
	if err != nil {
		return err
	}
	return nil
}

func writeFullAt(file pageFile, value []byte, offset int64) error {
	written, err := file.WriteAt(value, offset)
	if written != len(value) {
		if err != nil {
			return errors.Join(io.ErrShortWrite, err)
		}
		return io.ErrShortWrite
	}
	return err
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
