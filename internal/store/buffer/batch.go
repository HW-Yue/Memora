package buffer

import (
	"container/list"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
)

var ErrPublishConflict = errors.New("Buffer batch publish conflicts with committed Frame")

type BatchChange struct {
	Page        page.Page
	ExpectedLSN uint64
	New         bool
}

func (pool *Pool) PublishBatch(changes []BatchChange) error {
	if pool.config.Writer == nil {
		return ErrReadOnly
	}
	candidates, maximumLSN, err := validateBatchChanges(changes)
	if err != nil {
		return err
	}
	durableLSN, err := pool.config.Durability.DurableLSN()
	if err != nil {
		return fmt.Errorf("read durable WAL LSN: %w", err)
	}
	if durableLSN < maximumLSN {
		return fmt.Errorf(
			"%w: durable WAL LSN %d is before batch LSN %d",
			ErrWALNotDurable,
			durableLSN,
			maximumLSN,
		)
	}

	pool.publishMu.Lock()
	defer pool.publishMu.Unlock()
	pool.mu.Lock()
	defer pool.mu.Unlock()

	protected := make(map[Key]struct{}, len(candidates))
	newFrames := 0
	for _, change := range candidates {
		key := Key{
			SpaceID: change.Page.Header.SpaceID,
			PageID:  change.Page.Header.PageID,
		}
		protected[key] = struct{}{}
		current, exists := pool.frames[key]
		if change.New {
			if change.ExpectedLSN != 0 || exists {
				return fmt.Errorf(
					"%w: new Page %d is resident or has expected LSN",
					ErrPublishConflict,
					key.PageID,
				)
			}
			newFrames++
			continue
		}
		if !exists || current.loading {
			return fmt.Errorf(
				"%w: Page %d is not a ready resident Frame",
				ErrPublishConflict,
				key.PageID,
			)
		}
		if current.value.Header.LSN != change.ExpectedLSN ||
			current.value.Header.Generation != change.Page.Header.Generation ||
			!validBatchTypeTransition(
				current.value.Header.Type,
				change.Page.Header.Type,
			) ||
			change.Page.Header.LSN <= current.value.Header.LSN {
			return fmt.Errorf(
				"%w: Page %d expected LSN or generation",
				ErrPublishConflict,
				key.PageID,
			)
		}
	}

	requiredVictims := len(pool.frames) + newFrames - int(pool.config.Capacity)
	if requiredVictims < 0 {
		requiredVictims = 0
	}
	victims := pool.batchVictimsLocked(requiredVictims, protected)
	if len(victims) != requiredVictims {
		return ErrPoolFull
	}
	for _, victim := range victims {
		pool.evictLocked(victim)
	}
	for _, change := range candidates {
		key := Key{
			SpaceID: change.Page.Header.SpaceID,
			PageID:  change.Page.Header.PageID,
		}
		current, exists := pool.frames[key]
		if !exists {
			ready := make(chan struct{})
			close(ready)
			current = &frame{
				key: key, ready: ready, value: clonePage(change.Page),
				queue: queueOld,
			}
			current.element = pool.old.PushFront(current)
			pool.frames[key] = current
		} else {
			current.value = clonePage(change.Page)
		}
		if current.dirty {
			pool.dirty.MoveToBack(current.dirtyElement)
		} else {
			current.dirty = true
			current.dirtyElement = pool.dirty.PushBack(current)
		}
	}
	return nil
}

func validBatchTypeTransition(current, next page.Type) bool {
	if next == page.TypeFree {
		return current == page.TypeBTreeInternal ||
			current == page.TypeBTreeLeaf
	}
	return current == next
}

func validateBatchChanges(changes []BatchChange) ([]BatchChange, uint64, error) {
	if len(changes) == 0 {
		return nil, 0, fmt.Errorf("%w: empty batch", ErrInvalid)
	}
	candidates := make([]BatchChange, len(changes))
	keys := make(map[Key]struct{}, len(changes))
	spaceID := changes[0].Page.Header.SpaceID
	generation := changes[0].Page.Header.Generation
	var maximumLSN uint64
	for index, change := range changes {
		value := change.Page
		key := Key{SpaceID: value.Header.SpaceID, PageID: value.Header.PageID}
		isControl := value.Header.Type == page.TypeTreeControl
		if value.Header.SpaceID != spaceID ||
			value.Header.Generation == 0 ||
			value.Header.Generation != generation ||
			(isControl && value.Header.PageID != treecontrol.PageID) ||
			(!isControl && value.Header.PageID < treecontrol.FirstDataPageID) ||
			value.Header.LSN == 0 ||
			value.Header.Flags != 0 ||
			(index == len(changes)-1) != isControl {
			return nil, 0, fmt.Errorf(
				"%w: batch Page %d identity, type, generation, or LSN",
				ErrInvalid,
				value.Header.PageID,
			)
		}
		if !isControl &&
			value.Header.Type != page.TypeBTreeInternal &&
			value.Header.Type != page.TypeBTreeLeaf &&
			value.Header.Type != page.TypeFree {
			return nil, 0, fmt.Errorf(
				"%w: batch Page %d type",
				ErrInvalid,
				value.Header.PageID,
			)
		}
		if isControl {
			if _, err := treecontrol.Decode(value, spaceID); err != nil {
				return nil, 0, fmt.Errorf(
					"%w: decode Tree control: %v",
					ErrInvalid,
					err,
				)
			}
		}
		if _, duplicate := keys[key]; duplicate {
			return nil, 0, fmt.Errorf("%w: duplicate batch Page", ErrInvalid)
		}
		if _, err := page.Encode(value); err != nil {
			return nil, 0, fmt.Errorf(
				"%w: encode batch Page %d: %v",
				ErrInvalid,
				value.Header.PageID,
				err,
			)
		}
		keys[key] = struct{}{}
		candidates[index] = BatchChange{
			Page: clonePage(value), ExpectedLSN: change.ExpectedLSN, New: change.New,
		}
		if value.Header.LSN > maximumLSN {
			maximumLSN = value.Header.LSN
		}
	}
	return candidates, maximumLSN, nil
}

func (pool *Pool) batchVictimsLocked(
	count int,
	protected map[Key]struct{},
) []*frame {
	victims := make([]*frame, 0, count)
	appendQueue := func(queue *list.List) {
		for element := queue.Back(); element != nil && len(victims) < count; element = element.Prev() {
			current := element.Value.(*frame)
			if _, keep := protected[current.key]; keep ||
				current.pins != 0 || current.loading || current.dirty {
				continue
			}
			victims = append(victims, current)
		}
	}
	appendQueue(&pool.old)
	appendQueue(&pool.young)
	return victims
}
