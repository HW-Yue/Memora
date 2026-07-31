package buffer

import (
	"errors"
	"fmt"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/page"
)

var (
	ErrInvalid          = errors.New("buffer pool argument is invalid")
	ErrIdentityMismatch = errors.New("loaded Page identity does not match key")
	ErrReleased         = errors.New("buffer pool Handle is released")
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
	Frames  uint64
	Loading uint64
	Pins    uint64
}

type Pool struct {
	mu     sync.Mutex
	loader Loader
	frames map[Key]*frame
}

type frame struct {
	ready   chan struct{}
	loading bool
	loadErr error
	value   page.Page
	pins    uint64
	latch   sync.RWMutex
}

type Handle struct {
	mu       sync.RWMutex
	pool     *Pool
	frame    *frame
	released bool
}

func New(loader Loader) (*Pool, error) {
	if loader == nil {
		return nil, ErrInvalid
	}
	return &Pool{loader: loader, frames: make(map[Key]*frame)}, nil
}

func (pool *Pool) Fetch(key Key) (*Handle, error) {
	if key.PageID == 0 {
		return nil, fmt.Errorf("%w: Page ID is zero", ErrInvalid)
	}

	pool.mu.Lock()
	current, exists := pool.frames[key]
	if exists {
		current.pins++
		ready := current.ready
		pool.mu.Unlock()
		<-ready
		if current.loadErr != nil {
			return nil, current.loadErr
		}
		return &Handle{pool: pool, frame: current}, nil
	}

	current = &frame{ready: make(chan struct{}), loading: true, pins: 1}
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
	state := Stats{Frames: uint64(len(pool.frames))}
	for _, current := range pool.frames {
		state.Pins += current.pins
		if current.loading {
			state.Loading++
		}
	}
	return state
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
