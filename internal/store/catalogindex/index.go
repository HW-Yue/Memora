package catalogindex

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

type Receipt struct {
	Changed bool
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

// Validate checks whether a Catalog can be represented without identity,
// canonical name, or alias conflicts in the Lookup Index.
func Validate(databases []catalog.Database) error {
	_, err := buildEntries(databases)
	return err
}

func (index *Index) Replace(transactionID uint64, databases []catalog.Database) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: replace request", ErrInvalid)
	}
	desired, err := buildEntries(databases)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	current, err := index.readEntries(state)
	if err != nil {
		return Receipt{}, err
	}
	if equalEntries(current, desired) && state.RootPageID != 0 {
		return Receipt{State: state}, nil
	}

	plan, err := index.planReplacement(state, current, desired)
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) DatabaseByID(id string) (Locator, error) {
	key, err := identityKey(keyDatabaseID, id)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindDatabase, "", "")
}

func (index *Index) DatabaseByName(name string) (Locator, error) {
	key, err := nameKey(keyDatabaseName, "", name)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindDatabase, "", "")
}

func (index *Index) TableByID(id string) (Locator, error) {
	key, err := identityKey(keyTableID, id)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindTable, "", "")
}

func (index *Index) TableByName(databaseID, name string) (Locator, error) {
	key, err := nameKey(keyTableName, databaseID, name)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindTable, databaseID, "")
}

func (index *Index) ColumnByID(id string) (Locator, error) {
	key, err := identityKey(keyColumnID, id)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindColumn, "", "")
}

func (index *Index) ColumnByName(tableID, name string) (Locator, error) {
	key, err := nameKey(keyColumnName, tableID, name)
	if err != nil {
		return Locator{}, err
	}
	return index.lookup(key, KindColumn, "", tableID)
}

// ColumnsForTable returns each current Column locator once even though current
// names and aliases occupy separate keys in the same Table-scoped prefix.
func (index *Index) ColumnsForTable(tableID string) ([]Locator, error) {
	if index == nil || index.runtime == nil {
		return nil, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	start, err := scopedNamePrefix(keyColumnName, tableID)
	if err != nil {
		return nil, err
	}
	end, err := keyPrefixSuccessor(start)
	if err != nil {
		return nil, err
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return []Locator{}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, err
	}
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	byID := make(map[string]Locator)
	for {
		batch, err := cursor.Next(256)
		if err != nil {
			return nil, classifyTreeError(err)
		}
		for _, entry := range batch.Entries {
			if !bytes.HasPrefix(entry.Key, start) {
				return nil, fmt.Errorf("%w: Column key escaped Table prefix", ErrCorrupt)
			}
			if err := validateStoredKey(entry.Key); err != nil {
				return nil, err
			}
			locator, err := decodeLocator(entry.Value)
			if err != nil {
				return nil, err
			}
			if locator.Kind != KindColumn || locator.TableID != tableID {
				return nil, fmt.Errorf("%w: Column locator does not match Table prefix", ErrCorrupt)
			}
			if previous, exists := byID[locator.ID]; exists && previous != locator {
				return nil, fmt.Errorf("%w: Column aliases disagree", ErrCorrupt)
			}
			byID[locator.ID] = locator
		}
		if batch.Done {
			break
		}
	}
	result := make([]Locator, 0, len(byID))
	for _, locator := range byID {
		result = append(result, locator)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

// Databases returns every current Database identity locator exactly once.
func (index *Index) Databases() ([]Locator, error) {
	return index.identityLocators(keyDatabaseID, KindDatabase, "")
}

// TablesForDatabase returns every current Table identity locator in one Database.
func (index *Index) TablesForDatabase(databaseID string) ([]Locator, error) {
	if strings.TrimSpace(databaseID) != databaseID || databaseID == "" {
		return nil, fmt.Errorf("%w: Database ID", ErrInvalid)
	}
	return index.identityLocators(keyTableID, KindTable, databaseID)
}

func (index *Index) identityLocators(kind keyKind, locatorKind Kind, databaseID string) ([]Locator, error) {
	if index == nil || index.runtime == nil {
		return nil, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
	start := []byte{keyVersion, byte(kind)}
	end, err := keyPrefixSuccessor(start)
	if err != nil {
		return nil, err
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return []Locator{}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, err
	}
	cursor, err := searcher.NewCursor(start, end)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	result := make([]Locator, 0)
	for {
		batch, err := cursor.Next(256)
		if err != nil {
			return nil, classifyTreeError(err)
		}
		for _, entry := range batch.Entries {
			if !bytes.HasPrefix(entry.Key, start) {
				return nil, fmt.Errorf("%w: identity key escaped prefix", ErrCorrupt)
			}
			if err := validateStoredKey(entry.Key); err != nil {
				return nil, err
			}
			locator, err := decodeLocator(entry.Value)
			if err != nil {
				return nil, err
			}
			if locator.Kind != locatorKind ||
				(databaseID != "" && locator.DatabaseID != databaseID) {
				if databaseID != "" && locator.Kind == locatorKind {
					continue
				}
				return nil, fmt.Errorf("%w: identity locator kind", ErrCorrupt)
			}
			result = append(result, locator)
		}
		if batch.Done {
			break
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func (index *Index) lookup(key []byte, kind Kind, databaseID, tableID string) (Locator, error) {
	if index == nil || index.runtime == nil {
		return Locator{}, fmt.Errorf("%w: lookup Index", ErrInvalid)
	}
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
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return Locator{}, ErrNotFound
	}
	if err != nil {
		return Locator{}, classifyTreeError(err)
	}
	locator, err := decodeLocator(encoded)
	if err != nil {
		return Locator{}, err
	}
	if locator.Kind != kind ||
		(databaseID != "" && locator.DatabaseID != databaseID) ||
		(tableID != "" && locator.TableID != tableID) {
		return Locator{}, fmt.Errorf("%w: locator does not match key scope", ErrCorrupt)
	}
	return locator, nil
}

func (index *Index) readEntries(state treecontrol.State) (map[string][]byte, error) {
	result := make(map[string][]byte)
	if state.RootPageID == 0 {
		return result, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, err
	}
	cursor, err := searcher.NewCursor(nil, nil)
	if err != nil {
		return nil, err
	}
	for {
		batch, err := cursor.Next(256)
		if err != nil {
			return nil, classifyTreeError(err)
		}
		for _, entry := range batch.Entries {
			if err := validateStoredKey(entry.Key); err != nil {
				return nil, err
			}
			result[string(entry.Key)] = bytes.Clone(entry.Value)
		}
		if batch.Done {
			return result, nil
		}
	}
}

func (index *Index) planReplacement(
	state treecontrol.State,
	current, desired map[string][]byte,
) (btree.MutationPlan, error) {
	if state.RootPageID == 0 {
		return planBootstrap(state, desired)
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	for _, key := range sortedEntryKeys(current) {
		if _, keep := desired[key]; !keep {
			if err := planner.Delete([]byte(key)); err != nil {
				return btree.MutationPlan{}, classifyTreeError(err)
			}
		}
	}
	for _, key := range sortedEntryKeys(desired) {
		if old, exists := current[key]; exists && bytes.Equal(old, desired[key]) {
			continue
		}
		if err := planner.Upsert([]byte(key), desired[key]); err != nil {
			return btree.MutationPlan{}, classifyTreeError(err)
		}
	}
	return planner.Plan(), nil
}

func planBootstrap(state treecontrol.State, desired map[string][]byte) (btree.MutationPlan, error) {
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
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, rootID, rootID+1, reader,
	)
	if err != nil {
		return btree.MutationPlan{}, err
	}
	for _, key := range sortedEntryKeys(desired) {
		if err := planner.Upsert([]byte(key), desired[key]); err != nil {
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

func buildEntries(databases []catalog.Database) (map[string][]byte, error) {
	result := make(map[string][]byte)
	databaseIDs := make(map[string]struct{})
	tableIDs := make(map[string]struct{})
	columnIDs := make(map[string]struct{})
	add := func(key []byte, locator Locator) error {
		value, err := encodeLocator(locator)
		if err != nil {
			return err
		}
		encodedKey := string(key)
		if existing, exists := result[encodedKey]; exists && !bytes.Equal(existing, value) {
			return fmt.Errorf("%w: duplicate Catalog identity, name, or alias", ErrConflict)
		}
		result[encodedKey] = value
		return nil
	}
	addNames := func(kind keyKind, scope, name string, aliases []string, locator Locator) error {
		for _, candidate := range append([]string{name}, aliases...) {
			key, err := nameKey(kind, scope, candidate)
			if err != nil {
				return err
			}
			if err := add(key, locator); err != nil {
				return err
			}
		}
		return nil
	}
	for _, database := range databases {
		if strings.TrimSpace(database.ID) != database.ID || database.ID == "" {
			return nil, fmt.Errorf("%w: Database ID", ErrInvalid)
		}
		if _, duplicate := databaseIDs[database.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Database ID %q", ErrConflict, database.ID)
		}
		databaseIDs[database.ID] = struct{}{}
		locator := Locator{
			Kind: KindDatabase, ID: database.ID, SchemaRevision: database.SchemaVersion,
		}
		key, err := identityKey(keyDatabaseID, database.ID)
		if err != nil {
			return nil, err
		}
		if err := add(key, locator); err != nil {
			return nil, err
		}
		if err := addNames(keyDatabaseName, "", database.Name, database.Aliases, locator); err != nil {
			return nil, err
		}
		for _, table := range database.Tables {
			if table.DatabaseID != database.ID {
				return nil, fmt.Errorf("%w: Table %q parent", ErrInvalid, table.ID)
			}
			if _, duplicate := tableIDs[table.ID]; duplicate {
				return nil, fmt.Errorf("%w: duplicate Table ID %q", ErrConflict, table.ID)
			}
			tableIDs[table.ID] = struct{}{}
			tableLocator := Locator{
				Kind: KindTable, ID: table.ID, DatabaseID: database.ID,
				SchemaRevision: table.SchemaVersion,
			}
			key, err := identityKey(keyTableID, table.ID)
			if err != nil {
				return nil, err
			}
			if err := add(key, tableLocator); err != nil {
				return nil, err
			}
			if err := addNames(keyTableName, database.ID, table.Name, table.Aliases, tableLocator); err != nil {
				return nil, err
			}
			for _, column := range table.Columns {
				if _, duplicate := columnIDs[column.ID]; duplicate {
					return nil, fmt.Errorf("%w: duplicate Column ID %q", ErrConflict, column.ID)
				}
				columnIDs[column.ID] = struct{}{}
				columnLocator := Locator{
					Kind: KindColumn, ID: column.ID, DatabaseID: database.ID,
					TableID: table.ID, SchemaRevision: column.SchemaVersion,
				}
				key, err := identityKey(keyColumnID, column.ID)
				if err != nil {
					return nil, err
				}
				if err := add(key, columnLocator); err != nil {
					return nil, err
				}
				if err := addNames(keyColumnName, table.ID, column.Name, column.Aliases, columnLocator); err != nil {
					return nil, err
				}
			}
		}
	}
	return result, nil
}

func sortedEntryKeys(entries map[string][]byte) []string {
	result := make([]string, 0, len(entries))
	for key := range entries {
		result = append(result, key)
	}
	sort.Strings(result)
	return result
}

func equalEntries(left, right map[string][]byte) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if !bytes.Equal(value, right[key]) {
			return false
		}
	}
	return true
}

func classifyTreeError(err error) error {
	if errors.Is(err, btree.ErrCorrupt) || errors.Is(err, page.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
