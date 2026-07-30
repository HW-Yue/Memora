package page

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestManagerCreateWriteReadReopen(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "space_7.memora")
	manager, err := Create(path, 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %#o, want 0600", got)
	}
	assertPageCount(t, manager, 0)
	assertSpaceManifest(t, path, 7)

	first := Page{
		Header:  Header{Type: TypeData, SpaceID: 7, PageID: 1, Generation: 3, LSN: 5},
		Payload: []byte("first"),
	}
	second := Page{
		Header:  Header{Type: TypeBTreeLeaf, SpaceID: 7, PageID: 2, Generation: 3, LSN: 8},
		Payload: []byte("second"),
	}
	if err := manager.Write(first); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if err := manager.Write(second); err != nil {
		t.Fatalf("Write(second) error = %v", err)
	}
	assertPageCount(t, manager, 2)
	assertPageEqual(t, readPage(t, manager, 1), first)

	first.Payload = []byte("overwritten")
	first.Header.LSN = 9
	if err := manager.Write(first); err != nil {
		t.Fatalf("Write(overwrite) error = %v", err)
	}
	if err := manager.Sync(); err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := Open(path, 7)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertPageCount(t, reopened, 2)
	assertPageEqual(t, readPage(t, reopened, 1), first)
	assertPageEqual(t, readPage(t, reopened, 2), second)
}

func TestManagerRejectsInvalidIdentityAndAllocationGap(t *testing.T) {
	t.Parallel()

	manager, err := Create(filepath.Join(t.TempDir(), "space.memora"), 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })

	tests := []struct {
		name  string
		value Page
	}{
		{name: "manifest slot", value: Page{Header: Header{Type: TypeData, SpaceID: 7}}},
		{name: "wrong space", value: Page{Header: Header{Type: TypeData, SpaceID: 8, PageID: 1}}},
		{name: "allocation gap", value: Page{Header: Header{Type: TypeData, SpaceID: 7, PageID: 2}}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			if err := manager.Write(test.value); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Write() error = %v, want ErrInvalid", err)
			}
		})
	}

	if _, err := manager.Read(0); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Read(0) error = %v, want ErrInvalid", err)
	}
	if _, err := manager.Read(1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Read(unallocated) error = %v, want ErrNotFound", err)
	}
	if _, err := manager.Read(math.MaxUint64); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Read(overflow) error = %v, want ErrInvalid", err)
	}
}

func TestManagerCreateDoesNotReplaceExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "existing.memora")
	want := []byte("belongs to the user")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if manager, err := Create(path, 7); !errors.Is(err, fs.ErrExist) {
		if manager != nil {
			_ = manager.Close()
		}
		t.Fatalf("Create(existing) error = %v, want fs.ErrExist", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing file = %q, want %q", got, want)
	}
}

func TestManagerOpenRejectsWrongSpaceAndCorruptManifest(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "space.memora")
	manager, err := Create(path, 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if opened, err := Open(path, 8); !errors.Is(err, ErrCorrupt) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("Open(wrong space) error = %v, want ErrCorrupt", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	data[0] ^= 0xff
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if opened, err := Open(path, 7); !errors.Is(err, ErrCorrupt) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("Open(corrupt manifest) error = %v, want ErrCorrupt", err)
	}
}

func TestManagerRejectsTruncatedFileAndDataPage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "space.memora")
	manager, err := Create(path, 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := manager.Write(Page{
		Header:  Header{Type: TypeData, SpaceID: 7, PageID: 1},
		Payload: []byte("payload"),
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if err := os.Truncate(path, Size+100); err != nil {
		t.Fatalf("Truncate() error = %v", err)
	}
	if opened, err := Open(path, 7); !errors.Is(err, ErrCorrupt) {
		if opened != nil {
			_ = opened.Close()
		}
		t.Fatalf("Open(truncated) error = %v, want ErrCorrupt", err)
	}
}

func TestManagerReadRejectsCorruptDataPage(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "space.memora")
	manager, err := Create(path, 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := manager.Write(Page{
		Header:  Header{Type: TypeData, SpaceID: 7, PageID: 1},
		Payload: []byte("payload"),
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("OpenFile() error = %v", err)
	}
	if _, err := file.WriteAt([]byte{0xff}, Size+HeaderSize); err != nil {
		_ = file.Close()
		t.Fatalf("WriteAt() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close(file) error = %v", err)
	}

	reopened, err := Open(path, 7)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	if _, err := reopened.Read(1); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Read(corrupt) error = %v, want ErrCorrupt", err)
	}
}

func TestManagerFaultsShortIOAndReadsOnlyTargetPage(t *testing.T) {
	t.Parallel()

	target, err := Encode(Page{
		Header:  Header{Type: TypeData, SpaceID: 7, PageID: 7},
		Payload: []byte("target"),
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}

	t.Run("short write", func(t *testing.T) {
		file := &faultPageFile{writeN: Size - 1}
		manager := &Manager{file: file, spaceID: 7, nextPageID: 1}
		err := manager.Write(Page{Header: Header{Type: TypeData, SpaceID: 7, PageID: 1}})
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("Write() error = %v, want io.ErrShortWrite", err)
		}
	})

	t.Run("short read", func(t *testing.T) {
		file := &faultPageFile{readData: target, readN: Size - 1, readErr: io.EOF}
		manager := &Manager{file: file, spaceID: 7, nextPageID: 8}
		if _, err := manager.Read(7); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Read() error = %v, want ErrCorrupt", err)
		}
	})

	t.Run("target only", func(t *testing.T) {
		file := &faultPageFile{readData: target, readN: Size}
		manager := &Manager{file: file, spaceID: 7, nextPageID: 8}
		got, err := manager.Read(7)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		if got.Header.PageID != 7 {
			t.Fatalf("Read().PageID = %d, want 7", got.Header.PageID)
		}
		if file.readCalls != 1 || file.lastReadLength != Size || file.lastReadOffset != 7*Size {
			t.Fatalf(
				"ReadAt calls=%d length=%d offset=%d, want 1/%d/%d",
				file.readCalls,
				file.lastReadLength,
				file.lastReadOffset,
				Size,
				7*Size,
			)
		}
	})

	t.Run("valid checksum wrong identity", func(t *testing.T) {
		wrong, err := Encode(Page{
			Header: Header{Type: TypeData, SpaceID: 8, PageID: 9},
		})
		if err != nil {
			t.Fatalf("Encode() error = %v", err)
		}
		file := &faultPageFile{readData: wrong, readN: Size}
		manager := &Manager{file: file, spaceID: 7, nextPageID: 8}
		if _, err := manager.Read(7); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Read() error = %v, want ErrCorrupt", err)
		}
	})

	t.Run("sync error", func(t *testing.T) {
		want := errors.New("injected sync failure")
		file := &faultPageFile{syncErr: want}
		manager := &Manager{file: file, spaceID: 7, nextPageID: 1}
		if err := manager.Sync(); !errors.Is(err, want) {
			t.Fatalf("Sync() error = %v, want injected error", err)
		}
	})
}

func TestManagerConcurrentReadAndOverwrite(t *testing.T) {
	t.Parallel()

	manager, err := Create(filepath.Join(t.TempDir(), "space.memora"), 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := manager.Write(Page{
		Header:  Header{Type: TypeData, SpaceID: 7, PageID: 1},
		Payload: []byte("0"),
	}); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for iteration := 0; iteration < 100; iteration++ {
				value, err := manager.Read(1)
				if err != nil {
					t.Errorf("Read() error = %v", err)
					return
				}
				if len(value.Payload) == 0 {
					t.Error("Read() returned empty payload")
					return
				}
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-start
		for iteration := 1; iteration <= 100; iteration++ {
			if err := manager.Write(Page{
				Header: Header{
					Type:    TypeData,
					SpaceID: 7,
					PageID:  1,
					LSN:     uint64(iteration),
				},
				Payload: []byte{byte(iteration)},
			}); err != nil {
				t.Errorf("Write() error = %v", err)
				return
			}
		}
	}()
	close(start)
	workers.Wait()
}

func TestManagerOperationsAfterClose(t *testing.T) {
	t.Parallel()

	manager, err := Create(filepath.Join(t.TempDir(), "space.memora"), 7)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := manager.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}

	if _, err := manager.Read(1); !errors.Is(err, ErrClosed) {
		t.Fatalf("Read() error = %v, want ErrClosed", err)
	}
	if err := manager.Write(Page{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Write() error = %v, want ErrClosed", err)
	}
	if err := manager.Sync(); !errors.Is(err, ErrClosed) {
		t.Fatalf("Sync() error = %v, want ErrClosed", err)
	}
	if _, err := manager.PageCount(); !errors.Is(err, ErrClosed) {
		t.Fatalf("PageCount() error = %v, want ErrClosed", err)
	}
}

func assertSpaceManifest(t *testing.T, path string, spaceID uint64) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open(manifest) error = %v", err)
	}
	defer func() { _ = file.Close() }()

	encoded := make([]byte, Size)
	if _, err := file.ReadAt(encoded, 0); err != nil {
		t.Fatalf("ReadAt(manifest) error = %v", err)
	}
	manifest, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode(manifest) error = %v", err)
	}
	if manifest.Header.Type != TypeManifest ||
		manifest.Header.SpaceID != spaceID ||
		manifest.Header.PageID != 0 {
		t.Fatalf("manifest header = %#v", manifest.Header)
	}
	wantPayload := []byte{'M', 'E', 'M', 'S', 'P', 'C', '0', '1', 1, 0, 16, 0, 0, 0, 0, 0}
	if !bytes.Equal(manifest.Payload, wantPayload) {
		t.Fatalf("manifest payload = %x, want %x", manifest.Payload, wantPayload)
	}
}

func assertPageCount(t *testing.T, manager *Manager, want uint64) {
	t.Helper()
	got, err := manager.PageCount()
	if err != nil {
		t.Fatalf("PageCount() error = %v", err)
	}
	if got != want {
		t.Fatalf("PageCount() = %d, want %d", got, want)
	}
}

func readPage(t *testing.T, manager *Manager, pageID uint64) Page {
	t.Helper()
	value, err := manager.Read(pageID)
	if err != nil {
		t.Fatalf("Read(%d) error = %v", pageID, err)
	}
	return value
}

func assertPageEqual(t *testing.T, got, want Page) {
	t.Helper()
	if got.Header != want.Header || !bytes.Equal(got.Payload, want.Payload) {
		t.Fatalf("Page = %#v, want %#v", got, want)
	}
}

type faultPageFile struct {
	readData       []byte
	readN          int
	readErr        error
	writeN         int
	writeErr       error
	syncErr        error
	readCalls      int
	lastReadLength int
	lastReadOffset int64
	closed         bool
}

func (file *faultPageFile) ReadAt(target []byte, offset int64) (int, error) {
	file.readCalls++
	file.lastReadLength = len(target)
	file.lastReadOffset = offset
	n := file.readN
	if n > len(target) {
		n = len(target)
	}
	if n > len(file.readData) {
		n = len(file.readData)
	}
	copy(target[:n], file.readData[:n])
	return n, file.readErr
}

func (file *faultPageFile) WriteAt([]byte, int64) (int, error) {
	return file.writeN, file.writeErr
}

func (file *faultPageFile) Sync() error {
	return file.syncErr
}

func (file *faultPageFile) Stat() (os.FileInfo, error) {
	return fakeFileInfo{size: Size}, nil
}

func (file *faultPageFile) Close() error {
	file.closed = true
	return nil
}

type fakeFileInfo struct {
	size int64
}

func (info fakeFileInfo) Name() string       { return "fake" }
func (info fakeFileInfo) Size() int64        { return info.size }
func (info fakeFileInfo) Mode() os.FileMode  { return 0o600 }
func (info fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (info fakeFileInfo) IsDir() bool        { return false }
func (info fakeFileInfo) Sys() any           { return nil }
