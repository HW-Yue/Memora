package buffer

import (
	"container/list"
	"errors"
	"fmt"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/page"
)

var (
	ErrInvalid          = errors.New("buffer pool argument is invalid")
	ErrIdentityMismatch = errors.New("loaded Page identity does not match key")
	ErrReleased         = errors.New("buffer pool Handle is released")
	ErrPoolFull         = errors.New("buffer pool has no evictable Frame")
)

type Key struct {
	SpaceID uint64
	PageID  uint64
}

type Loader interface {
	Load(Key) (page.Page, error)
}

type LoaderFunc func(Key) (page.Page, error)

func (function LoaderFunc) Load(key Key) (page.Page, error) {
	return function(key)
}

type Stats struct {
	Frames    uint64
	Loading   uint64
	Pins      uint64
	Young     uint64
	Old       uint64
	Evictions uint64
}

type Config struct {
	Capacity  uint64
	OldFrames uint64
}

type Pool struct {
	mu        sync.Mutex
	loader    Loader
	frames    map[Key]*frame
	config    Config
	young     list.List
	old       list.List
	evictions uint64
}

type frame struct {
	key     Key
	ready   chan struct{}
	loading bool
	loadErr error
	value   page.Page
	pins    uint64
	latch   sync.RWMutex
	queue   frameQueue
	element *list.Element
}

type frameQueue uint8

const (
	queueNone frameQueue = iota
	queueYoung
	queueOld
)

type Handle struct {
	mu       sync.RWMutex
	pool     *Pool
	frame    *frame
	released bool
}

func New(loader Loader, config Config) (*Pool, error) {
	if loader == nil || config.Capacity == 0 || config.OldFrames == 0 ||
		config.OldFrames > config.Capacity {
		return nil, ErrInvalid
	}
	return &Pool{loader: loader, frames: make(map[Key]*frame), config: config}, nil
}

func (pool *Pool) Fetch(key Key) (*Handle, error) {
	if key.PageID == 0 {
		return nil, fmt.Errorf("%w: Page ID is zero", ErrInvalid)
	}

	pool.mu.Lock()
	current, exists := pool.frames[key]
	if exists {
		current.pins++
		if !current.loading {
			pool.touchLocked(current)
		}
		ready := current.ready
		pool.mu.Unlock()
		<-ready
		if current.loadErr != nil {
			return nil, current.loadErr
		}
		return &Handle{pool: pool, frame: current}, nil
	}
	if err := pool.reserveMissLocked(); err != nil {
		pool.mu.Unlock()
		return nil, err
	}

	current = &frame{key: key, ready: make(chan struct{}), loading: true, pins: 1}
	pool.frames[key] = current
	pool.mu.Unlock()

	value, err := pool.loader.Load(key)
	if err == nil &&
		(value.Header.SpaceID != key.SpaceID || value.Header.PageID != key.PageID) {
		err = fmt.Errorf(
			"%w: got space=%d page=%d, want space=%d page=%d",
			ErrIdentityMismatch,
			value.Header.SpaceID,
			value.Header.PageID,
			key.SpaceID,
			key.PageID,
		)
	}

	pool.mu.Lock()
	current.loading = false
	current.loadErr = err
	if err != nil {
		current.pins = 0
		delete(pool.frames, key)
	} else {
		current.value = clonePage(value)
		current.queue = queueOld
		current.element = pool.old.PushFront(current)
		pool.trimOldLocked()
	}
	close(current.ready)
	pool.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &Handle{pool: pool, frame: current}, nil
}

func (pool *Pool) Stats() Stats {
	pool.mu.Lock()
	defer pool.mu.Unlock()
	state := Stats{
		Frames:    uint64(len(pool.frames)),
		Young:     uint64(pool.young.Len()),
		Old:       uint64(pool.old.Len()),
		Evictions: pool.evictions,
	}
	for _, current := range pool.frames {
		state.Pins += current.pins
		if current.loading {
			state.Loading++
		}
	}
	return state
}

func (pool *Pool) reserveMissLocked() error {
	if uint64(pool.old.Len()) >= pool.config.OldFrames {
		if victim := evictableFromBack(&pool.old); victim != nil {
			pool.evictLocked(victim)
			return nil
		}
	}
	if uint64(len(pool.frames)) < pool.config.Capacity {
		return nil
	}
	if victim := evictableFromBack(&pool.old); victim != nil {
		pool.evictLocked(victim)
		return nil
	}
	if victim := evictableFromBack(&pool.young); victim != nil {
		pool.evictLocked(victim)
		return nil
	}
	return ErrPoolFull
}

func (pool *Pool) touchLocked(current *frame) {
	switch current.queue {
	case queueYoung:
		pool.young.MoveToFront(current.element)
	case queueOld:
		if pool.config.OldFrames == pool.config.Capacity {
			pool.old.MoveToFront(current.element)
			return
		}
		pool.old.Remove(current.element)
		current.queue = queueYoung
		current.element = pool.young.PushFront(current)
		pool.rebalanceYoungLocked()
	}
}

func (pool *Pool) rebalanceYoungLocked() {
	youngFrames := pool.config.Capacity - pool.config.OldFrames
	for uint64(pool.young.Len()) > youngFrames {
		element := pool.young.Back()
		current := element.Value.(*frame)
		pool.young.Remove(element)
		current.queue = queueOld
		current.element = pool.old.PushFront(current)
	}
	pool.trimOldLocked()
}

func (pool *Pool) trimOldLocked() {
	for uint64(pool.old.Len()) > pool.config.OldFrames {
		victim := evictableFromBack(&pool.old)
		if victim == nil {
			return
		}
		pool.evictLocked(victim)
	}
}

func evictableFromBack(queue *list.List) *frame {
	for element := queue.Back(); element != nil; element = element.Prev() {
		current := element.Value.(*frame)
		if current.pins == 0 && !current.loading {
			return current
		}
	}
	return nil
}

func (pool *Pool) evictLocked(current *frame) {
	switch current.queue {
	case queueYoung:
		pool.young.Remove(current.element)
	case queueOld:
		pool.old.Remove(current.element)
	}
	delete(pool.frames, current.key)
	current.queue = queueNone
	current.element = nil
	pool.evictions++
}

func (handle *Handle) Inspect(inspect func(page.Page) error) error {
	if inspect == nil {
		return fmt.Errorf("%w: Inspect callback is nil", ErrInvalid)
	}
	handle.mu.RLock()
	defer handle.mu.RUnlock()
	if handle.released {
		return ErrReleased
	}
	handle.frame.latch.RLock()
	defer handle.frame.latch.RUnlock()
	return inspect(clonePage(handle.frame.value))
}

func (handle *Handle) InspectExclusive(inspect func(page.Page) error) error {
	if inspect == nil {
		return fmt.Errorf("%w: InspectExclusive callback is nil", ErrInvalid)
	}
	handle.mu.RLock()
	defer handle.mu.RUnlock()
	if handle.released {
		return ErrReleased
	}
	handle.frame.latch.Lock()
	defer handle.frame.latch.Unlock()
	return inspect(clonePage(handle.frame.value))
}

func (handle *Handle) Release() error {
	handle.mu.Lock()
	defer handle.mu.Unlock()
	if handle.released {
		return ErrReleased
	}
	handle.pool.mu.Lock()
	handle.frame.pins--
	handle.pool.mu.Unlock()
	handle.released = true
	return nil
}

func clonePage(value page.Page) page.Page {
	cloned := value
	cloned.Payload = append([]byte(nil), value.Payload...)
	return cloned
}
