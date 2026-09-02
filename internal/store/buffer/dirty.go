package buffer

import (
	"errors"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/store/page"
)

var (
	ErrReadOnly      = errors.New("buffer pool is read-only")
	ErrWALNotDurable = errors.New("Page LSN is not durable in WAL")
	ErrStalePageLSN  = errors.New("Page LSN does not advance")
	ErrNotResident   = errors.New("Page is not resident in buffer pool")
)

type PageWriter interface {
	Write(page.Page) error
}

type PageWriterFunc func(page.Page) error

func (function PageWriterFunc) Write(value page.Page) error {
	return function(value)
}

type WALDurability interface {
	DurableLSN() (uint64, error)
}

type WALDurabilityFunc func() (uint64, error)

func (function WALDurabilityFunc) DurableLSN() (uint64, error) {
	return function()
}

type FlushReport struct {
	Attempted uint64
	Flushed   uint64
	Remaining uint64
}

func (handle *Handle) Modify(pageLSN uint64, modify func(*page.Page) error) error {
	if modify == nil || pageLSN == 0 {
		return fmt.Errorf("%w: invalid Modify request", ErrInvalid)
	}
	handle.pool.publishMu.RLock()
	defer handle.pool.publishMu.RUnlock()
	handle.mu.RLock()
	defer handle.mu.RUnlock()
	if handle.released {
		return ErrReleased
	}
	pool := handle.pool
	if pool.config.Writer == nil {
		return ErrReadOnly
	}

	current := handle.frame
	current.latch.Lock()
	defer current.latch.Unlock()
	if pageLSN <= current.value.Header.LSN {
		return fmt.Errorf(
			"%w: got %d after %d",
			ErrStalePageLSN,
			pageLSN,
			current.value.Header.LSN,
		)
	}
	durableLSN, err := pool.config.Durability.DurableLSN()
	if err != nil {
		return fmt.Errorf("read durable WAL LSN: %w", err)
	}
	if durableLSN < pageLSN {
		return fmt.Errorf(
			"%w: durable WAL LSN %d is before Page LSN %d",
			ErrWALNotDurable,
			durableLSN,
			pageLSN,
		)
	}

	candidate := clonePage(current.value)
	if err := modify(&candidate); err != nil {
		return err
	}
	candidate.Header.LSN = pageLSN
	if candidate.Header.SpaceID != current.key.SpaceID ||
		candidate.Header.PageID != current.key.PageID {
		return fmt.Errorf(
			"%w: got space=%d page=%d, want space=%d page=%d",
			ErrIdentityMismatch,
			candidate.Header.SpaceID,
			candidate.Header.PageID,
			current.key.SpaceID,
			current.key.PageID,
		)
	}
	if _, err := page.Encode(candidate); err != nil {
		return err
	}
	current.value = clonePage(candidate)
	pool.mu.Lock()
	if !current.dirty {
		current.dirty = true
		current.dirtyElement = pool.dirty.PushBack(current)
	}
	pool.mu.Unlock()
	return nil
}

func (pool *Pool) Flush(key Key) error {
	pool.publishMu.RLock()
	defer pool.publishMu.RUnlock()

	if pool.config.Writer == nil {
		return ErrReadOnly
	}
	durableLSN, err := pool.config.Durability.DurableLSN()
	if err != nil {
		return fmt.Errorf("read durable WAL LSN: %w", err)
	}
	_, err = pool.flush(key, durableLSN)
	return err
}

func (pool *Pool) FlushDirty(limit uint64) (FlushReport, error) {
	durableLSN, err := pool.config.Durability.DurableLSN()
	if err != nil {
		return FlushReport{}, fmt.Errorf("read durable WAL LSN: %w", err)
	}
	return pool.FlushDirtyThrough(limit, durableLSN)
}

// FlushDirtyThrough flushes dirty Pages against a durable WAL LSN the caller
// already knows, instead of asking the log for it.
//
// It exists for callers the log itself invokes — a checkpoint barrier runs
// inside the log's own lock, so a flush that called back into the log to read
// its durable LSN would deadlock. The bound is still enforced: a Page whose LSN
// is beyond durableLSN is refused exactly as before, so passing a stale value
// makes the flush fail rather than write a Page ahead of its log.
func (pool *Pool) FlushDirtyThrough(limit, durableLSN uint64) (FlushReport, error) {
	pool.publishMu.RLock()
	defer pool.publishMu.RUnlock()

	if pool.config.Writer == nil {
		return FlushReport{}, ErrReadOnly
	}
	if limit == 0 {
		return FlushReport{}, fmt.Errorf("%w: FlushDirty limit is zero", ErrInvalid)
	}
	pool.mu.Lock()
	keys := make([]Key, 0, min(limit, uint64(pool.dirty.Len())))
	control := make([]bool, 0, cap(keys))
	for element := pool.dirty.Front(); element != nil && uint64(len(keys)) < limit; element = element.Next() {
		value := element.Value.(*frame)
		keys = append(keys, value.key)
		control = append(control, value.value.Header.Type == page.TypeTreeControl)
	}
	pool.mu.Unlock()
	// Which Pages to flush is the dirty order's business — oldest first, so the
	// log tail can advance. The order they are written in is two rules, and the
	// dirty order only happens to satisfy one of them:
	//
	//   - the Tree control Page goes last, so it never points at data Pages the
	//     file does not hold yet (TestPublishBatchInstallsCommittedPagesControlLast);
	//   - data Pages go in ascending Page ID, because a Page file that is still
	//     growing only accepts the Page after the last one it has seen, and
	//     refuses one written ahead of a lower Page it has not.
	//
	// Selecting by age and writing by that order keeps the same Pages and the
	// same guarantees; the second rule used to hold by accident, on Trees whose
	// Page file was fully materialised before any checkpoint ran.
	order := make([]int, len(keys))
	for index := range order {
		order[index] = index
	}
	sort.SliceStable(order, func(left, right int) bool {
		leftIndex, rightIndex := order[left], order[right]
		if control[leftIndex] != control[rightIndex] {
			return !control[leftIndex]
		}
		if control[leftIndex] {
			return false
		}
		if keys[leftIndex].SpaceID != keys[rightIndex].SpaceID {
			return keys[leftIndex].SpaceID < keys[rightIndex].SpaceID
		}
		return keys[leftIndex].PageID < keys[rightIndex].PageID
	})
	ordered := make([]Key, len(keys))
	for position, index := range order {
		ordered[position] = keys[index]
	}
	keys = ordered

	report := FlushReport{Attempted: uint64(len(keys))}
	var result error
	for _, key := range keys {
		flushed, err := pool.flush(key, durableLSN)
		if flushed {
			report.Flushed++
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("flush space=%d page=%d: %w", key.SpaceID, key.PageID, err))
		}
	}
	pool.mu.Lock()
	report.Remaining = uint64(pool.dirty.Len())
	pool.mu.Unlock()
	return report, result
}

func (pool *Pool) flush(key Key, durableLSN uint64) (bool, error) {
	pool.mu.Lock()
	current, exists := pool.frames[key]
	if !exists || current.loading {
		pool.mu.Unlock()
		return false, ErrNotResident
	}
	if !current.dirty {
		pool.mu.Unlock()
		return false, nil
	}
	current.pins++
	pool.mu.Unlock()
	defer func() {
		pool.mu.Lock()
		current.pins--
		pool.mu.Unlock()
	}()

	current.flushMu.Lock()
	defer current.flushMu.Unlock()
	current.latch.RLock()
	defer current.latch.RUnlock()

	pool.mu.Lock()
	if !current.dirty {
		pool.mu.Unlock()
		return false, nil
	}
	value := clonePage(current.value)
	pool.mu.Unlock()

	if durableLSN < value.Header.LSN {
		return false, fmt.Errorf(
			"%w: durable WAL LSN %d is before Page LSN %d",
			ErrWALNotDurable,
			durableLSN,
			value.Header.LSN,
		)
	}
	if err := pool.config.Writer.Write(clonePage(value)); err != nil {
		return false, err
	}

	pool.mu.Lock()
	current.dirty = false
	pool.dirty.Remove(current.dirtyElement)
	current.dirtyElement = nil
	pool.mu.Unlock()
	return true, nil
}
