package buffer

import (
	"fmt"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/page"
)

// SpaceStore is one space's page file, as much of it as a buffer pool needs.
type SpaceStore interface {
	Read(pageID uint64) (page.Page, error)
	Write(value page.Page) error
}

// Router is a Loader and a PageWriter that dispatch on SpaceID.
//
// One pool can hold pages from every space already — the frame key carries the
// SpaceID — but a pool built with a loader closed over a single store can only
// ever serve that one space. Routing here is what lets one pool, with one
// capacity, serve every Tree: resident memory is then bounded by that single
// budget instead of growing with the number of Trees.
type Router struct {
	mu     sync.RWMutex
	stores map[uint64]SpaceStore
}

func NewRouter() *Router {
	return &Router{stores: make(map[uint64]SpaceStore)}
}

// Register attaches a space's store. Registering a space twice is refused:
// silently replacing a store would leave the pool holding frames loaded from
// the previous one.
func (router *Router) Register(spaceID uint64, store SpaceStore) error {
	if router == nil || spaceID == 0 || store == nil {
		return fmt.Errorf("%w: Router registration", ErrInvalid)
	}
	router.mu.Lock()
	defer router.mu.Unlock()
	if _, exists := router.stores[spaceID]; exists {
		return fmt.Errorf("%w: space %d is already registered", ErrInvalid, spaceID)
	}
	router.stores[spaceID] = store
	return nil
}

// Spaces reports how many spaces are registered.
func (router *Router) Spaces() int {
	if router == nil {
		return 0
	}
	router.mu.RLock()
	defer router.mu.RUnlock()
	return len(router.stores)
}

func (router *Router) store(spaceID uint64) (SpaceStore, error) {
	if router == nil {
		return nil, fmt.Errorf("%w: Router is nil", ErrInvalid)
	}
	router.mu.RLock()
	defer router.mu.RUnlock()
	store, exists := router.stores[spaceID]
	if !exists {
		return nil, fmt.Errorf("%w: space %d is not registered", ErrInvalid, spaceID)
	}
	return store, nil
}

func (router *Router) Load(key Key) (page.Page, error) {
	store, err := router.store(key.SpaceID)
	if err != nil {
		return page.Page{}, err
	}
	return store.Read(key.PageID)
}

// Write routes on the page's own SpaceID. A page carries the space it belongs
// to in its header, so a flush needs no other context to find its file.
func (router *Router) Write(value page.Page) error {
	store, err := router.store(value.Header.SpaceID)
	if err != nil {
		return err
	}
	return store.Write(value)
}
