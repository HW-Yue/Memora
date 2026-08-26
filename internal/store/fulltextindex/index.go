package fulltextindex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/store/btree"
	"github.com/HW-Yue/Memora/internal/store/page"
	"github.com/HW-Yue/Memora/internal/store/treecommit"
	"github.com/HW-Yue/Memora/internal/store/treecontrol"
	"github.com/HW-Yue/Memora/internal/store/wal"
)

var (
	ErrInvalid  = errors.New("Fulltext Index input is invalid")
	ErrConflict = errors.New("Fulltext Index revision conflicts")
	ErrCorrupt  = errors.New("Fulltext Index is corrupt")
)

type Receipt struct {
	Changed bool
	Replay  bool
	Digest  string
	Added   int
	Removed int
	State   treecontrol.State
	WAL     wal.Receipt
}

type Index struct {
	mu      sync.RWMutex
	runtime *treecommit.Runtime
}

type postingIdentity struct {
	term    string
	fieldID string
}

type preparedDocument struct {
	compiled    fulltext.CompiledDocument
	object      objectRecord
	objectKey   []byte
	objectValue []byte
	postings    map[postingIdentity]fulltext.Posting
}

type operation struct {
	key    []byte
	value  []byte
	delete bool
}

func Open(runtime *treecommit.Runtime) (*Index, error) {
	if runtime == nil || runtime.State().SpaceID == 0 {
		return nil, fmt.Errorf("%w: durable Tree Runtime", ErrInvalid)
	}
	return &Index{runtime: runtime}, nil
}

func (index *Index) Bootstrap(transactionID uint64, documents []fulltext.Document) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 {
		return Receipt{}, fmt.Errorf("%w: Bootstrap request", ErrInvalid)
	}
	prepared := make([]preparedDocument, 0, len(documents))
	seen := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		value, err := prepareDocument(document)
		if err != nil {
			return Receipt{}, err
		}
		identity := string(value.objectKey)
		if _, exists := seen[identity]; exists {
			return Receipt{}, fmt.Errorf("%w: duplicate object identity", ErrConflict)
		}
		seen[identity] = struct{}{}
		prepared = append(prepared, value)
	}
	sort.Slice(prepared, func(left, right int) bool {
		return bytes.Compare(prepared[left].objectKey, prepared[right].objectKey) < 0
	})
	operations := make(map[string]operation)
	added := 0
	for _, document := range prepared {
		if err := addOperation(operations, operation{key: document.objectKey, value: document.objectValue}); err != nil {
			return Receipt{}, err
		}
		for _, posting := range document.postings {
			if err := addPostingOperations(operations, posting); err != nil {
				return Receipt{}, err
			}
			added++
		}
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	state := index.runtime.State()
	if state.RootPageID != 0 {
		return Receipt{}, fmt.Errorf("%w: Fulltext Index is not empty", ErrConflict)
	}
	plan, err := planBootstrap(state, sortedOperations(operations))
	if err != nil {
		return Receipt{}, err
	}
	committed, err := index.runtime.Commit(transactionID, plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{Changed: true, Added: added, State: committed.State, WAL: committed.WAL}, nil
}

func (index *Index) Replace(transactionID uint64, document fulltext.Document) (Receipt, error) {
	return index.replaceBatch(transactionID, []fulltext.Document{document}, "Replace")
}

// ReplaceBatch validates every document against one committed root and applies
// all object/owner/posting changes in one MutationPlan and WAL transaction.
// Digest is populated only for the single-document form because a batch has no
// independent canonical document identity.
func (index *Index) ReplaceBatch(transactionID uint64, documents []fulltext.Document) (Receipt, error) {
	return index.replaceBatch(transactionID, documents, "ReplaceBatch")
}

func (index *Index) replaceBatch(
	transactionID uint64,
	documents []fulltext.Document,
	operationName string,
) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 || len(documents) == 0 {
		return Receipt{}, fmt.Errorf("%w: %s request", ErrInvalid, operationName)
	}
	prepared, err := prepareBatch(documents, operationName)
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	result, err := index.planReplaceBatchLocked(prepared)
	if err != nil {
		return Receipt{}, err
	}
	if !result.changed {
		return Receipt{Replay: true, Digest: result.digest, State: index.runtime.State()}, nil
	}
	committed, err := index.runtime.Commit(transactionID, result.plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{
		Changed: true, Digest: result.digest, Added: result.added, Removed: result.removed,
		State: committed.State, WAL: committed.WAL,
	}, nil
}

// StageReplaceBatch validates and plans a ReplaceBatch and enrols the Tree in
// group, so it can be committed in one WAL transaction together with other
// Trees.
//
// On success the Index's write lock is held until the group commit finishes —
// the group owns releasing it. A batch that is a pure replay adds no member and
// releases immediately.
func (index *Index) StageReplaceBatch(group *treecommit.Group, documents []fulltext.Document) error {
	if index == nil || index.runtime == nil || group == nil || len(documents) == 0 {
		return fmt.Errorf("%w: StageReplaceBatch request", ErrInvalid)
	}
	prepared, err := prepareBatch(documents, "StageReplaceBatch")
	if err != nil {
		return err
	}
	index.mu.Lock()
	result, err := index.planReplaceBatchLocked(prepared)
	if err != nil || !result.changed {
		index.mu.Unlock()
		return err
	}
	group.Add(index.runtime, result.plan, index.mu.Unlock)
	return nil
}

// prepareBatch decodes and orders the documents of one batch, rejecting a
// batch that names the same object twice.
func prepareBatch(documents []fulltext.Document, operationName string) ([]preparedDocument, error) {
	prepared := make([]preparedDocument, 0, len(documents))
	seen := make(map[string]struct{}, len(documents))
	for _, document := range documents {
		next, err := prepareDocument(document)
		if err != nil {
			return nil, err
		}
		identity := string(next.objectKey)
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("%w: duplicate object identity in %s", ErrConflict, operationName)
		}
		seen[identity] = struct{}{}
		prepared = append(prepared, next)
	}
	sort.Slice(prepared, func(left, right int) bool {
		return bytes.Compare(prepared[left].objectKey, prepared[right].objectKey) < 0
	})
	return prepared, nil
}

// planned is a ReplaceBatch worked out but not committed. changed is false for
// a pure replay, which has nothing to write.
type planned struct {
	plan    btree.MutationPlan
	digest  string
	added   int
	removed int
	changed bool
}

// planReplaceBatchLocked builds the mutation plan for a ReplaceBatch. The
// caller holds the write lock.
func (index *Index) planReplaceBatchLocked(prepared []preparedDocument) (planned, error) {
	operations, digest, added, removed, err := index.batchOperationsLocked(prepared)
	if err != nil {
		return planned{}, err
	}
	if len(operations) == 0 {
		return planned{digest: digest}, nil
	}
	plan, err := index.plan(index.runtime.State(), sortedOperations(operations))
	if err != nil {
		return planned{}, err
	}
	return planned{plan: plan, digest: digest, added: added, removed: removed, changed: true}, nil
}

// batchOperationsLocked turns a prepared batch into the Tree operations that
// bring the index in line with it. The caller holds the write lock.
func (index *Index) batchOperationsLocked(
	prepared []preparedDocument,
) (map[string]operation, string, int, int, error) {
	state := index.runtime.State()
	searcher, err := index.searcher(state)
	if err != nil {
		return nil, "", 0, 0, err
	}
	operations := make(map[string]operation)
	added, removed := 0, 0
	for _, next := range prepared {
		current, found, err := lookupObject(searcher, next.objectKey)
		if err != nil {
			return nil, "", 0, 0, err
		}
		currentPostings := make(map[postingIdentity]fulltext.Posting)
		if !found {
			// Any first revision is acceptable. This index is derived and
			// catches up from the change log, so the first document it sees for
			// an object may already be several revisions old — created and
			// updated before catch-up next ran. Demanding revision 1 here would
			// turn an ordinary lag into corruption.
			orphaned, err := scanPrefix(searcher, mustOwnerPrefix(next.object.kind, next.object.objectID))
			if err != nil {
				return nil, "", 0, 0, err
			}
			if len(orphaned) != 0 {
				return nil, "", 0, 0, fmt.Errorf("%w: owner entries exist without object", ErrCorrupt)
			}
		} else {
			if current.databaseID != next.object.databaseID || current.tableID != next.object.tableID {
				return nil, "", 0, 0, fmt.Errorf("%w: object identity changed scope", ErrConflict)
			}
			currentPostings, err = loadOwnedPostings(searcher, current)
			if err != nil {
				return nil, "", 0, 0, err
			}
			if next.compiled.Revision == current.revision {
				if next.compiled.Digest != current.digest {
					return nil, "", 0, 0, fmt.Errorf("%w: revision has different content", ErrConflict)
				}
				if !samePostingMaps(currentPostings, next.postings) {
					return nil, "", 0, 0, fmt.Errorf("%w: replay postings disagree with object digest", ErrCorrupt)
				}
				continue
			}
			// Advance, not advance-by-one. The strict rule belonged to the era
			// when the writer pushed every revision inline, where a gap meant
			// the writer had skipped one. A follower legitimately jumps: three
			// updates between two catch-up rounds produce one document at the
			// final revision. Keeping the strict rule would wedge catch-up
			// permanently the first time a round failed and writes continued —
			// every later round would span the gap and be refused. Skipping is
			// impossible by construction anyway: the cursor only moves past
			// what was applied.
			if next.compiled.Revision < current.revision {
				return nil, "", 0, 0, fmt.Errorf("%w: revision must advance", ErrConflict)
			}
		}

		if err := addOperation(operations, operation{key: next.objectKey, value: next.objectValue}); err != nil {
			return nil, "", 0, 0, err
		}
		for identity, posting := range currentPostings {
			if _, retained := next.postings[identity]; retained {
				continue
			}
			ownerKey, err := encodeOwnerKey(posting)
			if err != nil {
				return nil, "", 0, 0, err
			}
			postingKey, err := encodePostingKey(posting)
			if err != nil {
				return nil, "", 0, 0, err
			}
			if err := addOperation(operations, operation{key: ownerKey, delete: true}); err != nil {
				return nil, "", 0, 0, err
			}
			if err := addOperation(operations, operation{key: postingKey, delete: true}); err != nil {
				return nil, "", 0, 0, err
			}
		}
		for _, posting := range next.postings {
			if err := addPostingOperations(operations, posting); err != nil {
				return nil, "", 0, 0, err
			}
		}
		documentAdded, documentRemoved := postingDifference(currentPostings, next.postings)
		added += documentAdded
		removed += documentRemoved
	}
	digest := ""
	if len(prepared) == 1 {
		digest = prepared[0].compiled.Digest
	}
	return operations, digest, added, removed, nil
}

// Cursor reports how far through the committed change log this derived index
// has been brought. Zero means it has never caught up.
//
// The value lives in this Tree, written in the same transaction as the
// documents it describes, so it cannot outrun or lag what it claims to
// describe — a cursor kept beside the index would start lying the first time a
// crash landed between the two writes, and a lying cursor makes catch-up skip
// the gap silently.
func (index *Index) Cursor() (uint64, error) {
	if index == nil || index.runtime == nil {
		return 0, fmt.Errorf("%w: cursor read", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	return index.cursorLocked()
}

func (index *Index) cursorLocked() (uint64, error) {
	searcher, err := index.searcher(index.runtime.State())
	if err != nil || searcher == nil {
		return 0, err
	}
	encoded, err := searcher.Get(cursorKey())
	if errors.Is(err, btree.ErrNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, classifyTreeError(err)
	}
	return decodeCursorValue(encoded)
}

// AdvanceThrough applies a catch-up round: the documents projected from the
// change log, and the cursor that says how far those changes reached, in one
// transaction.
//
// documents may be empty — a batch of changes that touches nothing indexable
// still has to move the cursor, or catch-up would reconsider it forever.
func (index *Index) AdvanceThrough(
	transactionID uint64, documents []fulltext.Document, cursor uint64,
) (Receipt, error) {
	if index == nil || index.runtime == nil || transactionID == 0 || cursor == 0 {
		return Receipt{}, fmt.Errorf("%w: AdvanceThrough request", ErrInvalid)
	}
	prepared, err := prepareBatch(documents, "AdvanceThrough")
	if err != nil {
		return Receipt{}, err
	}

	index.mu.Lock()
	defer index.mu.Unlock()
	result, err := index.planAdvanceLocked(prepared, cursor)
	if err != nil {
		return Receipt{}, err
	}
	if !result.changed {
		return Receipt{Replay: true, Digest: result.digest, State: index.runtime.State()}, nil
	}
	committed, err := index.runtime.Commit(transactionID, result.plan)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{
		Changed: true, Digest: result.digest, Added: result.added, Removed: result.removed,
		State: committed.State, WAL: committed.WAL,
	}, nil
}

// planAdvanceLocked builds the plan for a catch-up round: the document
// operations plus the cursor write.
func (index *Index) planAdvanceLocked(prepared []preparedDocument, cursor uint64) (planned, error) {
	current, err := index.cursorLocked()
	if err != nil {
		return planned{}, err
	}
	if cursor < current {
		return planned{}, fmt.Errorf("%w: cursor moves backwards", ErrConflict)
	}
	operations, digest, added, removed, err := index.batchOperationsLocked(prepared)
	if err != nil {
		return planned{}, err
	}
	if cursor > current {
		value, err := encodeCursorValue(cursor)
		if err != nil {
			return planned{}, err
		}
		if err := addOperation(operations, operation{key: cursorKey(), value: value}); err != nil {
			return planned{}, err
		}
	}
	if len(operations) == 0 {
		return planned{digest: digest}, nil
	}
	plan, err := index.plan(index.runtime.State(), sortedOperations(operations))
	if err != nil {
		return planned{}, err
	}
	return planned{plan: plan, digest: digest, added: added, removed: removed, changed: true}, nil
}

func (index *Index) Postings(term string) ([]fulltext.Posting, error) {
	prefix, err := postingPrefix(term)
	if err != nil {
		return nil, err
	}
	return index.readPostings(prefix, false)
}

// PostingsInDatabases reads only explicit (term, database_id) key prefixes.
// An empty Database scope is an empty result and never means all Databases.
func (index *Index) PostingsInDatabases(ctx context.Context, terms, databaseIDs []string) ([]fulltext.Posting, error) {
	if index == nil || index.runtime == nil || ctx == nil {
		return nil, fmt.Errorf("%w: scoped posting read", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(terms) == 0 || len(databaseIDs) == 0 {
		return []fulltext.Posting{}, nil
	}
	prefixes := make([][]byte, 0, len(terms)*len(databaseIDs))
	seenTerms, seenDatabases := map[string]struct{}{}, map[string]struct{}{}
	for _, term := range terms {
		if _, duplicate := seenTerms[term]; duplicate {
			return nil, fmt.Errorf("%w: duplicate posting term", ErrInvalid)
		}
		seenTerms[term] = struct{}{}
	}
	for _, databaseID := range databaseIDs {
		if _, duplicate := seenDatabases[databaseID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate posting Database", ErrInvalid)
		}
		seenDatabases[databaseID] = struct{}{}
	}
	for _, term := range terms {
		for _, databaseID := range databaseIDs {
			prefix, err := postingDatabasePrefix(term, databaseID)
			if err != nil {
				return nil, err
			}
			prefixes = append(prefixes, prefix)
		}
	}

	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return []fulltext.Posting{}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	result := []fulltext.Posting{}
	for _, prefix := range prefixes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, scanErr := scanPrefix(searcher, prefix)
		if scanErr != nil {
			return nil, scanErr
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			decoded, decodeErr := decodePostingKey(entry.Key)
			if decodeErr != nil {
				return nil, decodeErr
			}
			revision, frequency, decodeErr := decodePostingValue(entry.Value)
			if decodeErr != nil {
				return nil, decodeErr
			}
			posting := decoded.posting
			posting.Revision, posting.Frequency = revision, frequency
			if mirrorErr := validatePostingMirror(searcher, posting, entry.Value); mirrorErr != nil {
				return nil, mirrorErr
			}
			result = append(result, posting)
		}
	}
	sortPostings(result)
	return result, nil
}

func (index *Index) AllPostings() ([]fulltext.Posting, error) {
	return index.readPostings(allPostingPrefix(), true)
}

func (index *Index) Objects() ([]fulltext.Object, error) {
	if index == nil || index.runtime == nil {
		return nil, fmt.Errorf("%w: object inventory", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return []fulltext.Object{}, nil
	}
	searcher, err := index.searcher(state)
	if err != nil {
		return nil, err
	}
	entries, err := scanAll(searcher)
	if err != nil {
		return nil, err
	}
	result := make([]fulltext.Object, 0)
	postings := 0
	for _, entry := range entries {
		if len(entry.Key) < 2 || entry.Key[0] != keyVersion {
			return nil, fmt.Errorf("%w: unknown physical key", ErrCorrupt)
		}
		if entry.Key[1] == keyKindPosting {
			postings++
		}
		if entry.Key[1] != keyKindObject {
			continue
		}
		kind, objectID, err := decodeObjectKey(entry.Key)
		if err != nil {
			return nil, err
		}
		object, err := decodeObjectValue(entry.Value, kind, objectID)
		if err != nil {
			return nil, err
		}
		result = append(result, fulltext.Object{
			Kind: object.kind, DatabaseID: object.databaseID, TableID: object.tableID,
			ObjectID: object.objectID, Revision: object.revision, State: object.state, Digest: object.digest,
		})
	}
	if err := validateAllRecords(searcher, postings); err != nil {
		return nil, err
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Kind != result[right].Kind {
			return result[left].Kind < result[right].Kind
		}
		if result[left].DatabaseID != result[right].DatabaseID {
			return result[left].DatabaseID < result[right].DatabaseID
		}
		if result[left].TableID != result[right].TableID {
			return result[left].TableID < result[right].TableID
		}
		return result[left].ObjectID < result[right].ObjectID
	})
	return result, nil
}

func (index *Index) readPostings(prefix []byte, validateAll bool) ([]fulltext.Posting, error) {
	if index == nil || index.runtime == nil {
		return nil, fmt.Errorf("%w: posting read", ErrInvalid)
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	state := index.runtime.State()
	if state.RootPageID == 0 {
		return []fulltext.Posting{}, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	entries, err := scanPrefix(searcher, prefix)
	if err != nil {
		return nil, err
	}
	result := make([]fulltext.Posting, 0, len(entries))
	for _, entry := range entries {
		decoded, err := decodePostingKey(entry.Key)
		if err != nil {
			return nil, err
		}
		revision, frequency, err := decodePostingValue(entry.Value)
		if err != nil {
			return nil, err
		}
		posting := decoded.posting
		posting.Revision, posting.Frequency = revision, frequency
		if err := validatePostingMirror(searcher, posting, entry.Value); err != nil {
			return nil, err
		}
		result = append(result, posting)
	}
	if validateAll {
		if err := validateAllRecords(searcher, len(result)); err != nil {
			return nil, err
		}
	}
	sortPostings(result)
	return result, nil
}

func prepareDocument(document fulltext.Document) (preparedDocument, error) {
	compiled, err := fulltext.Compile(document)
	if err != nil {
		return preparedDocument{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	object := objectRecord{
		kind: compiled.Kind, databaseID: compiled.DatabaseID, tableID: compiled.TableID,
		objectID: compiled.ObjectID, revision: compiled.Revision, state: compiled.State, digest: compiled.Digest,
	}
	objectKey, err := encodeObjectKey(object.kind, object.objectID)
	if err != nil {
		return preparedDocument{}, err
	}
	objectValue, err := encodeObjectValue(object)
	if err != nil {
		return preparedDocument{}, err
	}
	postings := make(map[postingIdentity]fulltext.Posting, len(compiled.Postings))
	for _, posting := range compiled.Postings {
		identity := postingIdentity{term: posting.Term, fieldID: posting.FieldID}
		if _, exists := postings[identity]; exists {
			return preparedDocument{}, fmt.Errorf("%w: duplicate compiled posting", ErrInvalid)
		}
		postings[identity] = posting
	}
	return preparedDocument{
		compiled: compiled, object: object, objectKey: objectKey, objectValue: objectValue, postings: postings,
	}, nil
}

func addPostingOperations(operations map[string]operation, posting fulltext.Posting) error {
	ownerKey, err := encodeOwnerKey(posting)
	if err != nil {
		return err
	}
	postingKey, err := encodePostingKey(posting)
	if err != nil {
		return err
	}
	value, err := encodePostingValue(posting.Revision, posting.Frequency)
	if err != nil {
		return err
	}
	if err := addOperation(operations, operation{key: ownerKey, value: value}); err != nil {
		return err
	}
	return addOperation(operations, operation{key: postingKey, value: value})
}

func addOperation(operations map[string]operation, value operation) error {
	identity := string(value.key)
	if _, exists := operations[identity]; exists {
		return fmt.Errorf("%w: duplicate physical key", ErrConflict)
	}
	operations[identity] = operation{key: bytes.Clone(value.key), value: bytes.Clone(value.value), delete: value.delete}
	return nil
}

func sortedOperations(values map[string]operation) []operation {
	result := make([]operation, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(left, right int) bool { return bytes.Compare(result[left].key, result[right].key) < 0 })
	return result
}

func lookupObject(searcher *btree.Searcher, key []byte) (objectRecord, bool, error) {
	if searcher == nil {
		return objectRecord{}, false, nil
	}
	encoded, err := searcher.Get(key)
	if errors.Is(err, btree.ErrNotFound) {
		return objectRecord{}, false, nil
	}
	if err != nil {
		return objectRecord{}, false, classifyTreeError(err)
	}
	kind, objectID, err := decodeObjectKey(key)
	if err != nil {
		return objectRecord{}, false, err
	}
	value, err := decodeObjectValue(encoded, kind, objectID)
	return value, true, err
}

func loadOwnedPostings(searcher *btree.Searcher, object objectRecord) (map[postingIdentity]fulltext.Posting, error) {
	entries, err := scanPrefix(searcher, mustOwnerPrefix(object.kind, object.objectID))
	if err != nil {
		return nil, err
	}
	if object.state != fulltext.StateLive && len(entries) != 0 {
		return nil, fmt.Errorf("%w: inactive object owns postings", ErrCorrupt)
	}
	result := make(map[postingIdentity]fulltext.Posting, len(entries))
	for _, entry := range entries {
		owner, err := decodeOwnerKey(entry.Key)
		if err != nil || owner.kind != object.kind || owner.objectID != object.objectID {
			return nil, fmt.Errorf("%w: owner key escaped object prefix", ErrCorrupt)
		}
		revision, frequency, err := decodePostingValue(entry.Value)
		if err != nil {
			return nil, err
		}
		posting := fulltext.Posting{
			Term: owner.term, Kind: object.kind, DatabaseID: object.databaseID, TableID: object.tableID,
			ObjectID: object.objectID, Revision: revision, FieldID: owner.fieldID, Frequency: frequency,
		}
		if revision != object.revision {
			return nil, fmt.Errorf("%w: owner revision disagrees with object", ErrCorrupt)
		}
		postingKey, err := encodePostingKey(posting)
		if err != nil {
			return nil, err
		}
		mirrored, err := searcher.Get(postingKey)
		if errors.Is(err, btree.ErrNotFound) {
			return nil, fmt.Errorf("%w: owner has no forward posting", ErrCorrupt)
		}
		if err != nil {
			return nil, classifyTreeError(err)
		}
		if !bytes.Equal(mirrored, entry.Value) {
			return nil, fmt.Errorf("%w: owner and posting values disagree", ErrCorrupt)
		}
		identity := postingIdentity{term: posting.Term, fieldID: posting.FieldID}
		if _, exists := result[identity]; exists {
			return nil, fmt.Errorf("%w: duplicate owner posting", ErrCorrupt)
		}
		result[identity] = posting
	}
	return result, nil
}

func validatePostingMirror(searcher *btree.Searcher, posting fulltext.Posting, encoded []byte) error {
	objectKey, err := encodeObjectKey(posting.Kind, posting.ObjectID)
	if err != nil {
		return err
	}
	object, found, err := lookupObject(searcher, objectKey)
	if err != nil {
		return err
	}
	if !found || object.state != fulltext.StateLive || object.databaseID != posting.DatabaseID ||
		object.tableID != posting.TableID || object.revision != posting.Revision {
		return fmt.Errorf("%w: posting disagrees with current object", ErrCorrupt)
	}
	ownerKey, err := encodeOwnerKey(posting)
	if err != nil {
		return err
	}
	ownerValue, err := searcher.Get(ownerKey)
	if errors.Is(err, btree.ErrNotFound) {
		return fmt.Errorf("%w: posting has no owner mirror", ErrCorrupt)
	}
	if err != nil {
		return classifyTreeError(err)
	}
	if !bytes.Equal(ownerValue, encoded) {
		return fmt.Errorf("%w: posting and owner values disagree", ErrCorrupt)
	}
	return nil
}

func validateOwnerMirror(searcher *btree.Searcher, owner ownerRecord, encoded []byte) error {
	revision, frequency, err := decodePostingValue(encoded)
	if err != nil {
		return err
	}
	objectKey, err := encodeObjectKey(owner.kind, owner.objectID)
	if err != nil {
		return err
	}
	object, found, err := lookupObject(searcher, objectKey)
	if err != nil {
		return err
	}
	if !found || object.state != fulltext.StateLive || object.revision != revision {
		return fmt.Errorf("%w: owner disagrees with current object", ErrCorrupt)
	}
	posting := fulltext.Posting{
		Term: owner.term, Kind: owner.kind, DatabaseID: object.databaseID, TableID: object.tableID,
		ObjectID: owner.objectID, Revision: revision, FieldID: owner.fieldID, Frequency: frequency,
	}
	postingKey, err := encodePostingKey(posting)
	if err != nil {
		return err
	}
	postingValue, err := searcher.Get(postingKey)
	if errors.Is(err, btree.ErrNotFound) {
		return fmt.Errorf("%w: owner has no posting mirror", ErrCorrupt)
	}
	if err != nil {
		return classifyTreeError(err)
	}
	if !bytes.Equal(postingValue, encoded) {
		return fmt.Errorf("%w: owner and posting values disagree", ErrCorrupt)
	}
	return nil
}

func validateMetadataEntry(key, value []byte) error {
	if !bytes.Equal(key, cursorKey()) {
		return fmt.Errorf("%w: unknown metadata key", ErrCorrupt)
	}
	_, err := decodeCursorValue(value)
	return err
}

func validateAllRecords(searcher *btree.Searcher, expectedPostings int) error {
	entries, err := scanAll(searcher)
	if err != nil {
		return err
	}
	owners, postings := 0, 0
	for _, entry := range entries {
		if len(entry.Key) < 2 || entry.Key[0] != keyVersion {
			return fmt.Errorf("%w: unknown physical key", ErrCorrupt)
		}
		switch entry.Key[1] {
		case keyKindObject:
			kind, objectID, err := decodeObjectKey(entry.Key)
			if err != nil {
				return err
			}
			if _, err := decodeObjectValue(entry.Value, kind, objectID); err != nil {
				return err
			}
		case keyKindOwner:
			owner, err := decodeOwnerKey(entry.Key)
			if err != nil {
				return err
			}
			if err := validateOwnerMirror(searcher, owner, entry.Value); err != nil {
				return err
			}
			owners++
		case keyKindPosting:
			decoded, err := decodePostingKey(entry.Key)
			if err != nil {
				return err
			}
			revision, frequency, err := decodePostingValue(entry.Value)
			if err != nil {
				return err
			}
			posting := decoded.posting
			posting.Revision, posting.Frequency = revision, frequency
			if err := validatePostingMirror(searcher, posting, entry.Value); err != nil {
				return err
			}
			postings++
		case keyKindMetadata:
			// The index's own bookkeeping. It is validated for shape but takes
			// no part in the owner/posting cardinality check below — it
			// describes the index rather than anything indexed.
			if err := validateMetadataEntry(entry.Key, entry.Value); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: unknown physical key kind", ErrCorrupt)
		}
	}
	if owners != postings || postings != expectedPostings {
		return fmt.Errorf("%w: owner/posting cardinality", ErrCorrupt)
	}
	return nil
}

func scanPrefix(searcher *btree.Searcher, prefix []byte) ([]btree.LeafEntry, error) {
	if searcher == nil {
		return []btree.LeafEntry{}, nil
	}
	end, err := prefixSuccessor(prefix)
	if err != nil {
		return nil, err
	}
	cursor, err := searcher.NewCursor(prefix, end)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	result := make([]btree.LeafEntry, 0)
	for {
		batch, err := cursor.Next(256)
		if err != nil {
			return nil, classifyTreeError(err)
		}
		result = append(result, batch.Entries...)
		if batch.Done {
			return result, nil
		}
	}
}

func scanAll(searcher *btree.Searcher) ([]btree.LeafEntry, error) {
	if searcher == nil {
		return []btree.LeafEntry{}, nil
	}
	cursor, err := searcher.NewCursor(nil, nil)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	result := make([]btree.LeafEntry, 0)
	for {
		batch, err := cursor.Next(256)
		if err != nil {
			return nil, classifyTreeError(err)
		}
		result = append(result, batch.Entries...)
		if batch.Done {
			return result, nil
		}
	}
}

func mustOwnerPrefix(kind fulltext.ObjectKind, objectID string) []byte {
	prefix, _ := ownerPrefix(kind, objectID)
	return prefix
}

func samePostingMaps(left, right map[postingIdentity]fulltext.Posting) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func postingDifference(current, next map[postingIdentity]fulltext.Posting) (int, int) {
	var added, removed int
	for key, value := range current {
		if replacement, exists := next[key]; !exists || replacement != value {
			removed++
		}
	}
	for key, value := range next {
		if previous, exists := current[key]; !exists || previous != value {
			added++
		}
	}
	return added, removed
}

func (index *Index) searcher(state treecontrol.State) (*btree.Searcher, error) {
	if state.RootPageID == 0 {
		return nil, nil
	}
	searcher, err := btree.NewSearcher(state.SpaceID, state.RootPageID, index.runtime)
	if err != nil {
		return nil, classifyTreeError(err)
	}
	return searcher, nil
}

func (index *Index) plan(state treecontrol.State, operations []operation) (btree.MutationPlan, error) {
	if state.RootPageID == 0 {
		return planBootstrap(state, operations)
	}
	planner, err := btree.NewMutationPlanner(
		state.SpaceID, state.Generation, state.RootPageID, state.NextPageID, index.runtime,
		index.runtime.FreePageIDs()...,
	)
	if err != nil {
		return btree.MutationPlan{}, classifyTreeError(err)
	}
	if err := applyOperations(planner, operations); err != nil {
		return btree.MutationPlan{}, err
	}
	return planner.Plan(), nil
}

func planBootstrap(state treecontrol.State, operations []operation) (btree.MutationPlan, error) {
	rootID := treecontrol.FirstDataPageID
	root, err := btree.Encode(page.Header{
		Type: page.TypeBTreeLeaf, SpaceID: state.SpaceID, PageID: rootID, Generation: state.Generation,
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
	if err := applyOperations(planner, operations); err != nil {
		return btree.MutationPlan{}, err
	}
	plan := planner.Plan()
	if len(plan.Changes) == 0 {
		plan.Changes = []btree.PageChange{{Page: root}}
	}
	plan.Allocated = append([]uint64{rootID}, plan.Allocated...)
	return plan, nil
}

func applyOperations(planner *btree.MutationPlanner, operations []operation) error {
	for _, operation := range operations {
		var err error
		if operation.delete {
			err = planner.Delete(operation.key)
		} else {
			err = planner.Upsert(operation.key, operation.value)
		}
		if err != nil {
			return classifyTreeError(err)
		}
	}
	return nil
}

func sortPostings(values []fulltext.Posting) {
	sort.Slice(values, func(left, right int) bool {
		if values[left].Term != values[right].Term {
			return values[left].Term < values[right].Term
		}
		if values[left].Kind != values[right].Kind {
			return values[left].Kind < values[right].Kind
		}
		if values[left].DatabaseID != values[right].DatabaseID {
			return values[left].DatabaseID < values[right].DatabaseID
		}
		if values[left].TableID != values[right].TableID {
			return values[left].TableID < values[right].TableID
		}
		if values[left].ObjectID != values[right].ObjectID {
			return values[left].ObjectID < values[right].ObjectID
		}
		if values[left].Revision != values[right].Revision {
			return values[left].Revision < values[right].Revision
		}
		return values[left].FieldID < values[right].FieldID
	})
}

func classifyTreeError(err error) error {
	if errors.Is(err, btree.ErrCorrupt) || errors.Is(err, page.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
