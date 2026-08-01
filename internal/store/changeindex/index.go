package changeindex

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

var (
	ErrInvalid  = errors.New("committed change index request is invalid")
	ErrConflict = errors.New("committed change index conflicts with immutable source")
	ErrCorrupt  = errors.New("committed change index is corrupt")
	ErrNotFound = errors.New("committed change index locator was not found")
)

type Locator struct {
	CommitSequence uint64 `json:"commit_sequence"`
	TransactionID  string `json:"transaction_id"`
	Checksum       string `json:"checksum"`
}

type Receipt struct {
	Changed bool
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

type operation struct{ key, value []byte }

var metaHighWaterKey = []byte{0}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

func (index *Index) Bootstrap(transactionID uint64, locators []Locator) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Bootstrap request", ErrInvalid)
	}
	prepared, err := prepareLocators(locators)
	if err != nil {
		return Receipt{}, err
	}
	for position, locator := range prepared {
		if locator.CommitSequence != uint64(position+1) {
			return Receipt{}, fmt.Errorf("%w: bootstrap sequence gap", ErrConflict)
		}
	}
	operations, err := locatorOperations(prepared)
	if err != nil {
		return Receipt{}, err
	}
	highWater := uint64(0)
	if len(prepared) > 0 {
		highWater = prepared[len(prepared)-1].CommitSequence
	}
	operations = append(operations, operation{key: bytes.Clone(metaHighWaterKey), value: encodeUint64(highWater)})

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID != 0 {
		return Receipt{}, fmt.Errorf("%w: committed change index is not empty", ErrConflict)
	}
	plan, err := planBootstrap(state, operations)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) Append(transactionID uint64, locators []Locator) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 || len(locators) == 0 {
		return Receipt{}, fmt.Errorf("%w: Append request", ErrInvalid)
	}
	prepared, err := prepareLocators(locators)
	if err != nil {
		return Receipt{}, err
	}
	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return Receipt{}, fmt.Errorf("%w: index is not bootstrapped", ErrConflict)
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return Receipt{}, err
	}
	highWater, err := highWaterWithSearcher(searcher)
	if err != nil {
		return Receipt{}, err
	}
	active := make([]Locator, 0, len(prepared))
	next := highWater
	for _, locator := range prepared {
		existing, sequenceFound, err := lookup(searcher, sequenceKey(locator.CommitSequence))
		if err != nil {
			return Receipt{}, err
		}
		byTransaction, transactionFound, err := lookup(searcher, transactionKey(locator.TransactionID))
		if err != nil {
			return Receipt{}, err
		}
		if sequenceFound || transactionFound {
			if !sequenceFound || !transactionFound || existing != locator || byTransaction != locator {
				return Receipt{}, fmt.Errorf("%w: locator identity changed", ErrConflict)
			}
			continue
		}
		if next == math.MaxUint64 || locator.CommitSequence != next+1 {
			return Receipt{}, fmt.Errorf("%w: sequence is not contiguous", ErrConflict)
		}
		next = locator.CommitSequence
		active = append(active, locator)
	}
	if len(active) == 0 {
		return Receipt{State: state}, nil
	}
	operations, err := locatorOperations(active)
	if err != nil {
		return Receipt{}, err
	}
	operations = append(operations, operation{key: bytes.Clone(metaHighWaterKey), value: encodeUint64(next)})
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return Receipt{}, err
	}
	for _, item := range operations {
		if err := planner.Upsert(item.key, item.value); err != nil {
			return Receipt{}, classifyTreeError(err)
		}
	}
	committed, err := index.runtime.Commit(transactionID, planner.Plan())
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) HighWater() (uint64, error) {
	if index == nil || index.runtime == nil {
		return 0, fmt.Errorf("%w: HighWater request", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return 0, fmt.Errorf("%w: missing root", ErrCorrupt)
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return 0, err
	}
	return highWaterWithSearcher(searcher)
}

func (index *Index) LookupSequence(sequence uint64) (Locator, error) {
	if index == nil || index.runtime == nil || sequence == 0 {
		return Locator{}, fmt.Errorf("%w: sequence lookup", ErrInvalid)
	}
	return index.lookupKey(sequenceKey(sequence), sequence, "")
}

func (index *Index) LookupTransaction(transactionID string) (Locator, error) {
	if index == nil || index.runtime == nil || !validTransactionID(transactionID) {
		return Locator{}, fmt.Errorf("%w: transaction lookup", ErrInvalid)
	}
	return index.lookupKey(transactionKey(transactionID), 0, transactionID)
}

func (index *Index) lookupKey(key []byte, sequence uint64, transactionID string) (Locator, error) {
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return Locator{}, ErrNotFound
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return Locator{}, err
	}
	locator, found, err := lookup(searcher, key)
	if err != nil {
		return Locator{}, err
	}
	if !found {
		return Locator{}, ErrNotFound
	}
	if (sequence != 0 && locator.CommitSequence != sequence) ||
		(transactionID != "" && locator.TransactionID != transactionID) {
		return Locator{}, fmt.Errorf("%w: key/value identity mismatch", ErrCorrupt)
	}
	return locator, nil
}

func (index *Index) Range(after, through uint64, limit int) ([]Locator, error) {
	if index == nil || index.runtime == nil || limit < 1 || limit > 1000 || through < after {
		return nil, fmt.Errorf("%w: range request", ErrInvalid)
	}
	if after >= through {
		return []Locator{}, nil
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, err
	}
	highWater, err := highWaterWithSearcher(searcher)
	if err != nil {
		return nil, err
	}
	if through > highWater || after == math.MaxUint64 {
		return nil, fmt.Errorf("%w: range exceeds high-water", ErrInvalid)
	}
	start := sequenceKey(after + 1)
	end := []byte{2}
	if through < math.MaxUint64 {
		end = sequenceKey(through + 1)
	}
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return nil, err
	}
	batch, err := cursor.Next(uint64(limit))
	if err != nil {
		return nil, classifyTreeError(err)
	}
	result := make([]Locator, 0, len(batch.Entries))
	for _, entry := range batch.Entries {
		sequence, err := decodeSequenceKey(entry.Key)
		if err != nil {
			return nil, err
		}
		locator, err := decodeLocator(entry.Value)
		if err != nil || locator.CommitSequence != sequence {
			return nil, fmt.Errorf("%w: range locator identity", ErrCorrupt)
		}
		result = append(result, locator)
	}
	return result, nil
}

func prepareLocators(locators []Locator) ([]Locator, error) {
	result := append([]Locator(nil), locators...)
	sort.Slice(result, func(left, right int) bool { return result[left].CommitSequence < result[right].CommitSequence })
	transactions := map[string]struct{}{}
	for index, locator := range result {
		if err := validateLocator(locator); err != nil {
			return nil, err
		}
		if index > 0 && result[index-1].CommitSequence == locator.CommitSequence {
			return nil, fmt.Errorf("%w: duplicate sequence", ErrConflict)
		}
		if _, duplicate := transactions[locator.TransactionID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate transaction", ErrConflict)
		}
		transactions[locator.TransactionID] = struct{}{}
	}
	return result, nil
}

func locatorOperations(locators []Locator) ([]operation, error) {
	result := make([]operation, 0, len(locators)*2)
	for _, locator := range locators {
		encoded, err := encodeLocator(locator)
		if err != nil {
			return nil, err
		}
		result = append(result,
			operation{key: sequenceKey(locator.CommitSequence), value: encoded},
			operation{key: transactionKey(locator.TransactionID), value: encoded},
		)
	}
	sort.Slice(result, func(left, right int) bool { return bytes.Compare(result[left].key, result[right].key) < 0 })
	return result, nil
}

func lookup(searcher *btree.Searcher, key []byte) (Locator, bool, error) {
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return Locator{}, false, nil
	}
	if err != nil {
		return Locator{}, false, classifyTreeError(err)
	}
	locator, err := decodeLocator(encoded)
	return locator, err == nil, err
}

func highWaterWithSearcher(searcher *btree.Searcher) (uint64, error) {
	encoded, err := searcher.Get(metaHighWaterKey)
	if err != nil || len(encoded) != 8 {
		return 0, fmt.Errorf("%w: high-water", ErrCorrupt)
	}
	return binary.BigEndian.Uint64(encoded), nil
}

func sequenceKey(sequence uint64) []byte {
	key := make([]byte, 9)
	key[0] = 1
	binary.BigEndian.PutUint64(key[1:], sequence)
	return key
}

func decodeSequenceKey(key []byte) (uint64, error) {
	if len(key) != 9 || key[0] != 1 {
		return 0, fmt.Errorf("%w: sequence key", ErrCorrupt)
	}
	sequence := binary.BigEndian.Uint64(key[1:])
	if sequence == 0 {
		return 0, fmt.Errorf("%w: zero sequence key", ErrCorrupt)
	}
	return sequence, nil
}

func transactionKey(transactionID string) []byte { return append([]byte{2}, transactionID...) }

func validateLocator(locator Locator) error {
	if locator.CommitSequence == 0 || !validTransactionID(locator.TransactionID) ||
		len(locator.Checksum) != 64 || strings.ToLower(locator.Checksum) != locator.Checksum {
		return fmt.Errorf("%w: locator", ErrInvalid)
	}
	if decoded, err := hex.DecodeString(locator.Checksum); err != nil || len(decoded) != 32 {
		return fmt.Errorf("%w: checksum", ErrInvalid)
	}
	return nil
}

func validTransactionID(value string) bool {
	if len(value) != 36 || !strings.HasPrefix(value, "txn_") {
		return false
	}
	decoded, err := hex.DecodeString(value[4:])
	return err == nil && len(decoded) == 16 && strings.ToLower(value) == value
}

func encodeUint64(value uint64) []byte {
	encoded := make([]byte, 8)
	binary.BigEndian.PutUint64(encoded, value)
	return encoded
}

func planBootstrap(state treecontrol.State, operations []operation) (btree.MutationPlan, error) {
	rootID := treecontrol.FirstDataPageID
	root, err := btree.Encode(page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: state.SpaceID, PageID: rootID,
		Generation: state.Generation,
	}, btree.Node{Kind: btree.KindLeaf})
	if err != nil {
		return btree.MutationPlan{}, err
	}
	reader := btree.ReaderFunc(func(pageID uint64) (page.Page, error) {
		if pageID == rootID {
			return root, nil
		}
		return page.Page{}, page.ErrNotFound
	})
	planner, err := btree.NewMutationPlanner(state.SpaceID, state.Generation, rootID, rootID+1, reader)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	for _, item := range operations {
		if err := planner.Upsert(item.key, item.value); err != nil {
			return btree.MutationPlan{}, err
		}
	}
	plan := planner.Plan()
	if len(plan.Changes) == 0 {
		plan.Changes = []btree.PageChange{{Page: root}}
	}
	plan.Allocated = append([]uint64{rootID}, plan.Allocated...)
	return plan, nil
}

func classifyTreeError(err error) error {
	if errors.Is(err, btree.ErrCorrupt) || errors.Is(err, page.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
