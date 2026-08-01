package buffer

import (
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/store/page"
)

// BenchmarkBufferPoolHotHitParallel is the F154 reproducible contention arm:
// 32 resident Pages, parallel Fetch/Release, and no loader I/O after warm-up.
func BenchmarkBufferPoolHotHitParallel(b *testing.B) {
	loader := LoaderFunc(func(key Key) (page.Page, error) {
		return testPage(key, "hot"), nil
	})
	pool, err := New(loader, Config{Capacity: 64, OldFrames: 32})
	if err != nil {
		b.Fatal(err)
	}
	for pageID := uint64(1); pageID <= 32; pageID++ {
		handle, err := pool.Fetch(Key{SpaceID: 7, PageID: pageID})
		if err != nil {
			b.Fatal(err)
		}
		if err := handle.Release(); err != nil {
			b.Fatal(err)
		}
	}
	var sequence atomic.Uint64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(parallel *testing.PB) {
		for parallel.Next() {
			pageID := sequence.Add(1)%32 + 1
			handle, err := pool.Fetch(Key{SpaceID: 7, PageID: pageID})
			if err != nil {
				b.Fatal(err)
			}
			if err := handle.Release(); err != nil {
				b.Fatal(err)
			}
		}
	})
}
