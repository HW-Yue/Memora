package mechanicalindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	snapshotBucket            = "mechanical_index_snapshots"
	versionBucket             = "mechanical_index_versions"
	postingBucket             = "mechanical_index_postings"
	defaultMaxTerms           = 256
	defaultMaxInputCharacters = 20000
	maxLookup                 = 1000
)

type Service struct {
	store              store.Store
	disabled           bool
	maxTerms           int
	maxInputCharacters int
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store, options Options) *Service {
	if options.MaxTerms == 0 {
		options.MaxTerms = defaultMaxTerms
	}
	if options.MaxInputCharacters == 0 {
		options.MaxInputCharacters = defaultMaxInputCharacters
	}
	if options.MaxTerms < 1 {
		options.MaxTerms = 1
	}
	if options.MaxInputCharacters < 1 {
		options.MaxInputCharacters = 1
	}
	return &Service{
		store: database, disabled: options.Disabled,
		maxTerms: options.MaxTerms, maxInputCharacters: options.MaxInputCharacters,
	}
}

func (service *Service) ReplaceIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	fields []string,
) (Snapshot, error) {
	return service.replaceIn(ctx, tx, locator, fields, false)
}

func (service *Service) RebuildIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	fields []string,
) (Snapshot, error) {
	return service.replaceIn(ctx, tx, locator, fields, true)
}

func (service *Service) replaceIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	fields []string,
	rebuild bool,
) (Snapshot, error) {
	if err := validateLocator(locator); err != nil {
		return Snapshot{}, err
	}
	current, exists, err := loadSnapshot(ctx, tx, snapshotKey(locator))
	if err != nil {
		return Snapshot{}, err
	}
	if exists && (locator.Revision < current.Revision ||
		(locator.Revision == current.Revision && !rebuild)) {
		return Snapshot{}, indexError(
			result.CodeRevisionConflict,
			fmt.Sprintf("mechanical index revision is %d; requested Row revision is %d", current.Revision, locator.Revision),
		)
	}
	next := Snapshot{
		Version: Version, TokenizerVersion: TokenizerVersion,
		Locator: locator, Source: SourceMechanical, State: StateActive, Terms: []string{},
	}
	if service.disabled {
		next.State = StateDisabled
	} else {
		next.Terms, next.Truncated = tokenize(
			fields, service.maxTerms, service.maxInputCharacters,
		)
	}
	return service.swap(ctx, tx, current, exists, next)
}

func (service *Service) InvalidateIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
) (Snapshot, error) {
	if err := validateLocator(locator); err != nil {
		return Snapshot{}, err
	}
	current, exists, err := loadSnapshot(ctx, tx, snapshotKey(locator))
	if err != nil {
		return Snapshot{}, err
	}
	if exists && locator.Revision <= current.Revision {
		return Snapshot{}, indexError(result.CodeRevisionConflict, "mechanical index revision is stale")
	}
	next := Snapshot{
		Version: Version, TokenizerVersion: TokenizerVersion,
		Locator: locator, Source: SourceMechanical, State: StateInvalid, Terms: []string{},
	}
	return service.swap(ctx, tx, current, exists, next)
}

func (service *Service) swap(
	ctx context.Context,
	tx store.Tx,
	current Snapshot,
	exists bool,
	next Snapshot,
) (Snapshot, error) {
	if exists {
		for _, term := range current.Terms {
			if err := removePosting(ctx, tx, current.DatabaseID, term, current.Locator); err != nil {
				return Snapshot{}, err
			}
		}
	}
	for _, term := range next.Terms {
		if err := appendPosting(ctx, tx, next.DatabaseID, term, Posting{
			Locator: next.Locator, Source: SourceMechanical,
		}); err != nil {
			return Snapshot{}, err
		}
	}
	if err := putJSON(ctx, tx, snapshotBucket, snapshotKey(next.Locator), next); err != nil {
		return Snapshot{}, err
	}
	if err := putJSON(ctx, tx, versionBucket, versionKey(next.Locator), next); err != nil {
		return Snapshot{}, err
	}
	return next, nil
}

func (service *Service) Lookup(
	ctx context.Context,
	databaseID, term string,
	limit int,
) ([]Posting, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.LookupIn(ctx, tx, databaseID, term, limit)
}

func (service *Service) LookupIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
	limit int,
) ([]Posting, error) {
	if strings.TrimSpace(databaseID) == "" || strings.TrimSpace(term) == "" {
		return nil, indexError(result.CodeValidation, "mechanical lookup requires database ID and term")
	}
	if limit < 1 || limit > maxLookup {
		return nil, indexError(result.CodeValidation, "mechanical lookup limit must be between 1 and 1000")
	}
	postings, err := loadPostings(ctx, tx, databaseID, strings.ToLower(term))
	if err != nil {
		return nil, err
	}
	if len(postings) > limit {
		postings = postings[:limit]
	}
	return postings, nil
}

func appendPosting(ctx context.Context, tx store.Tx, databaseID, term string, posting Posting) error {
	postings, err := loadPostings(ctx, tx, databaseID, term)
	if err != nil {
		return err
	}
	for _, existing := range postings {
		if sameRow(existing.Locator, posting.Locator) {
			return indexError(result.CodeAlreadyExists, "mechanical posting already exists")
		}
	}
	postings = append(postings, posting)
	sort.Slice(postings, func(left, right int) bool {
		if postings[left].TableID != postings[right].TableID {
			return postings[left].TableID < postings[right].TableID
		}
		return postings[left].RowID < postings[right].RowID
	})
	return savePostings(ctx, tx, databaseID, term, postings)
}

func removePosting(ctx context.Context, tx store.Tx, databaseID, term string, locator Locator) error {
	postings, err := loadPostings(ctx, tx, databaseID, term)
	if err != nil {
		return err
	}
	filtered := postings[:0]
	for _, posting := range postings {
		if !sameRow(posting.Locator, locator) {
			filtered = append(filtered, posting)
		}
	}
	if len(filtered) == len(postings) {
		return indexError(result.CodeInternal, "mechanical snapshot references a missing posting")
	}
	return savePostings(ctx, tx, databaseID, term, filtered)
}

func loadSnapshot(ctx context.Context, tx store.Tx, key string) (Snapshot, bool, error) {
	encoded, err := tx.Get(ctx, snapshotBucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, stableError(err)
	}
	var value Snapshot
	if decodeJSON(encoded, &value) != nil || validateSnapshot(value) != nil {
		return Snapshot{}, false, indexError(result.CodeInternal, "mechanical snapshot is corrupt")
	}
	return value, true, nil
}

func loadPostings(ctx context.Context, tx store.Tx, databaseID, term string) ([]Posting, error) {
	encoded, err := tx.Get(ctx, postingBucket, postingKey(databaseID, term))
	if errors.Is(err, store.ErrNotFound) {
		return []Posting{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var record postingRecord
	if decodeJSON(encoded, &record) != nil || record.Version != Version || record.Postings == nil {
		return nil, indexError(result.CodeInternal, "mechanical posting list is corrupt")
	}
	seen := make(map[string]struct{}, len(record.Postings))
	for _, posting := range record.Postings {
		key := posting.TableID + "\x00" + posting.RowID
		if posting.Source != SourceMechanical || posting.DatabaseID != databaseID ||
			validateLocator(posting.Locator) != nil {
			return nil, indexError(result.CodeInternal, "mechanical posting list is corrupt")
		}
		if _, exists := seen[key]; exists {
			return nil, indexError(result.CodeInternal, "mechanical posting list is corrupt")
		}
		seen[key] = struct{}{}
	}
	return record.Postings, nil
}

func savePostings(ctx context.Context, tx store.Tx, databaseID, term string, postings []Posting) error {
	key := postingKey(databaseID, term)
	if len(postings) == 0 {
		if err := tx.Delete(ctx, postingBucket, key); err != nil && !errors.Is(err, store.ErrNotFound) {
			return stableError(err)
		}
		return nil
	}
	return putJSON(ctx, tx, postingBucket, key, postingRecord{
		Version: Version, Postings: postings,
	})
}

func validateLocator(locator Locator) error {
	if locator.DatabaseID == "" || locator.TableID == "" || locator.Revision == 0 {
		return indexError(result.CodeValidation, "mechanical index locator is incomplete")
	}
	if _, err := logical.Validate(logical.Constraint{
		Name: "row_id", Definition: logical.Definition{Kind: logical.KindRelationID},
	}, locator.RowID); err != nil {
		return err
	}
	return nil
}

func validateSnapshot(value Snapshot) error {
	if value.Version != Version || value.TokenizerVersion != TokenizerVersion ||
		value.Source != SourceMechanical || validateLocator(value.Locator) != nil ||
		value.Terms == nil ||
		(value.State != StateActive && value.State != StateInvalid && value.State != StateDisabled) ||
		(value.State != StateActive && len(value.Terms) != 0) {
		return errors.New("invalid mechanical snapshot")
	}
	seen := make(map[string]struct{}, len(value.Terms))
	for _, term := range value.Terms {
		if term == "" || term != strings.ToLower(term) {
			return errors.New("invalid mechanical terms")
		}
		if _, duplicate := seen[term]; duplicate {
			return errors.New("duplicate mechanical term")
		}
		seen[term] = struct{}{}
	}
	return nil
}

func sameRow(left, right Locator) bool {
	return left.DatabaseID == right.DatabaseID && left.TableID == right.TableID &&
		left.RowID == right.RowID
}

func putJSON(ctx context.Context, tx store.Tx, bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return indexError(result.CodeInternal, "mechanical index record could not be encoded")
	}
	if err := tx.Put(ctx, bucket, key, encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func decodeJSON(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	return decoder.Decode(target)
}

func snapshotKey(locator Locator) string {
	return locator.DatabaseID + "\x00" + locator.TableID + "\x00" + locator.RowID
}

func versionKey(locator Locator) string {
	return fmt.Sprintf("%s\x00%020d", snapshotKey(locator), locator.Revision)
}

func postingKey(databaseID, term string) string { return databaseID + "\x00" + term }

func indexError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func stableError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	switch {
	case errors.Is(err, context.Canceled):
		return indexError(result.CodeCancelled, "mechanical index operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return indexError(result.CodeDeadlineExceeded, "mechanical index operation exceeded its deadline")
	default:
		return indexError(result.CodeInternal, "mechanical index operation failed")
	}
}
