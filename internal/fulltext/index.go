package fulltext

import (
	"fmt"
	"sort"
	"sync"
)

type Posting struct {
	Term       string
	Kind       ObjectKind
	DatabaseID string
	TableID    string
	ObjectID   string
	Revision   uint64
	FieldID    string
	Frequency  uint64
}

type Receipt struct {
	Digest  string
	Replay  bool
	Added   int
	Removed int
}

type objectKey struct {
	kind       ObjectKind
	databaseID string
	tableID    string
	objectID   string
}

type objectIdentity struct {
	kind     ObjectKind
	objectID string
}

type postingKey struct {
	term       string
	kind       ObjectKind
	databaseID string
	tableID    string
	objectID   string
	fieldID    string
}

type indexedDocument struct {
	revision uint64
	digest   string
	postings map[postingKey]Posting
}

type Index struct {
	mu        sync.RWMutex
	documents map[objectKey]indexedDocument
	owners    map[objectIdentity]objectKey
	postings  map[postingKey]Posting
}

func New() *Index {
	return &Index{
		documents: make(map[objectKey]indexedDocument),
		owners:    make(map[objectIdentity]objectKey),
		postings:  make(map[postingKey]Posting),
	}
}

func Build(documents []Document) (*Index, error) {
	type seed struct {
		key      objectKey
		identity objectIdentity
		record   indexedDocument
	}
	seeds := make([]seed, 0, len(documents))
	seenKeys := make(map[objectKey]struct{}, len(documents))
	seenIdentities := make(map[objectIdentity]objectKey, len(documents))
	for _, value := range documents {
		normalized, postings, digest, err := normalizeDocument(value)
		if err != nil {
			return nil, err
		}
		key := documentKey(normalized)
		identity := objectIdentity{kind: normalized.Kind, objectID: normalized.ObjectID}
		if _, exists := seenKeys[key]; exists {
			return nil, documentError("document %q is duplicated", normalized.ObjectID)
		}
		if existing, exists := seenIdentities[identity]; exists && existing != key {
			return nil, documentError("document %q changes identity scope", normalized.ObjectID)
		}
		seenKeys[key] = struct{}{}
		seenIdentities[identity] = key
		seeds = append(seeds, seed{
			key: key, identity: identity,
			record: indexedDocument{revision: normalized.Revision, digest: digest, postings: postingMap(postings)},
		})
	}
	sort.Slice(seeds, func(left, right int) bool { return lessObjectKey(seeds[left].key, seeds[right].key) })
	index := New()
	for _, value := range seeds {
		index.documents[value.key] = value.record
		index.owners[value.identity] = value.key
		for key, posting := range value.record.postings {
			index.postings[key] = posting
		}
	}
	return index, nil
}

func (index *Index) Replace(value Document) (Receipt, error) {
	normalized, postings, digest, err := normalizeDocument(value)
	if err != nil {
		return Receipt{}, err
	}
	if index == nil {
		return Receipt{}, fmt.Errorf("%w: index is nil", ErrInvalidDocument)
	}
	key := documentKey(normalized)
	identity := objectIdentity{kind: normalized.Kind, objectID: normalized.ObjectID}
	next := indexedDocument{revision: normalized.Revision, digest: digest, postings: postingMap(postings)}

	index.mu.Lock()
	defer index.mu.Unlock()
	if owner, exists := index.owners[identity]; exists && owner != key {
		return Receipt{}, revisionError("document %q changes identity scope", normalized.ObjectID)
	}
	current, exists := index.documents[key]
	if !exists {
		if normalized.Revision != 1 {
			return Receipt{}, revisionError("first incremental revision must be 1")
		}
	} else {
		if normalized.Revision == current.revision {
			if digest == current.digest {
				return Receipt{Digest: digest, Replay: true}, nil
			}
			return Receipt{}, revisionError("revision %d has different content", normalized.Revision)
		}
		if normalized.Revision != current.revision+1 {
			return Receipt{}, revisionError("revision must advance from %d to %d", current.revision, current.revision+1)
		}
	}

	added, removed := postingDifference(current.postings, next.postings)
	for posting := range current.postings {
		delete(index.postings, posting)
	}
	for posting, value := range next.postings {
		index.postings[posting] = value
	}
	index.documents[key] = next
	index.owners[identity] = key
	return Receipt{Digest: digest, Added: added, Removed: removed}, nil
}

func (index *Index) Postings(term string) []Posting {
	if index == nil {
		return nil
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	result := make([]Posting, 0)
	for _, posting := range index.postings {
		if posting.Term == term {
			result = append(result, posting)
		}
	}
	sortPostings(result)
	return result
}

func (index *Index) AllPostings() []Posting {
	if index == nil {
		return nil
	}
	index.mu.RLock()
	defer index.mu.RUnlock()
	result := make([]Posting, 0, len(index.postings))
	for _, posting := range index.postings {
		result = append(result, posting)
	}
	sortPostings(result)
	return result
}

func postingMap(values []Posting) map[postingKey]Posting {
	result := make(map[postingKey]Posting, len(values))
	for _, value := range values {
		result[postingIdentity(value)] = value
	}
	return result
}

func postingDifference(current, next map[postingKey]Posting) (int, int) {
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

func documentKey(value normalizedDocument) objectKey {
	return objectKey{
		kind: value.Kind, databaseID: value.DatabaseID, tableID: value.TableID, objectID: value.ObjectID,
	}
}

func postingIdentity(value Posting) postingKey {
	return postingKey{
		term: value.Term, kind: value.Kind, databaseID: value.DatabaseID, tableID: value.TableID,
		objectID: value.ObjectID, fieldID: value.FieldID,
	}
}

func lessObjectKey(left, right objectKey) bool {
	if left.kind != right.kind {
		return left.kind < right.kind
	}
	if left.databaseID != right.databaseID {
		return left.databaseID < right.databaseID
	}
	if left.tableID != right.tableID {
		return left.tableID < right.tableID
	}
	return left.objectID < right.objectID
}

func sortPostings(values []Posting) {
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

func revisionError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrRevisionConflict, fmt.Sprintf(format, arguments...))
}
