package buffer

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

// BenchmarkFlushDirty64PageBatch is the F155 simple-scheduler arm. It measures
// one serial 1 MiB batch (64 x 16 KiB Pages) against the real Page file writer.
func BenchmarkFlushDirty64PageBatch(b *testing.B) {
	manager, err := page.Create(filepath.Join(b.TempDir(), "flush.pages"), 17)
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = manager.Close() }()
	for pageID := uint64(1); pageID <= 64; pageID++ {
		if err := manager.Write(page.Page{Header: page.Header{
			Type: page.TypeData, SpaceID: 17, PageID: pageID, Generation: 1,
		}, Payload: make([]byte, 1024)}); err != nil {
			b.Fatal(err)
		}
	}
	pool, err := New(LoaderFunc(func(key Key) (page.Page, error) {
		return manager.Read(key.PageID)
	}), Config{
		Capacity: 64, OldFrames: 32, Writer: manager,
		Durability: WALDurabilityFunc(func() (uint64, error) {
			return math.MaxUint64, nil
		}),
	})
	if err != nil {
		b.Fatal(err)
	}
	for pageID := uint64(1); pageID <= 64; pageID++ {
		handle, err := pool.Fetch(Key{SpaceID: 17, PageID: pageID})
		if err != nil {
			b.Fatal(err)
		}
		if err := handle.Release(); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for iteration := 1; iteration <= b.N; iteration++ {
		lsn := uint64(iteration)
		for pageID := uint64(1); pageID <= 64; pageID++ {
			handle, err := pool.Fetch(Key{SpaceID: 17, PageID: pageID})
			if err != nil {
				b.Fatal(err)
			}
			err = handle.Modify(lsn, func(value *page.Page) error {
				value.Payload[0] = byte(iteration)
				return nil
			})
			if releaseErr := handle.Release(); err == nil {
				err = releaseErr
			}
			if err != nil {
				b.Fatal(err)
			}
		}
		report, err := pool.FlushDirty(64)
		if err != nil || report.Flushed != 64 || report.Remaining != 0 {
			b.Fatalf("FlushDirty() = %+v, %v", report, err)
		}
	}
}
