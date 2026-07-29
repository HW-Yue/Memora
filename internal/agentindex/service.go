package agentindex

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	snapshotBucket = "agent_index_snapshots"
	versionBucket  = "agent_index_versions"
	postingBucket  = "agent_index_postings"
	defaultTarget  = 24
	defaultMaximum = 64
	maxTermRunes   = 128
	maxLookup      = 1000
)

type Service struct {
	store       store.Store
	targetTerms int
	maxTerms    int
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store, options Options) *Service {
	if options.MaxTerms == 0 {
		options.MaxTerms = defaultMaximum
	}
	if options.TargetTerms == 0 {
		options.TargetTerms = defaultTarget
	}
	if options.MaxTerms < 1 {
		options.MaxTerms = 1
	}
	if options.TargetTerms < 1 {
		options.TargetTerms = 1
	}
	if options.TargetTerms > options.MaxTerms {
		options.TargetTerms = options.MaxTerms
	}
	return &Service{
		store: database, targetTerms: options.TargetTerms, maxTerms: options.MaxTerms,
	}
}

func (service *Service) TargetTerms() int { return service.targetTerms }
func (service *Service) MaxTerms() int    { return service.maxTerms }

func (service *Service) ReplaceIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	terms []string,
) (Snapshot, error) {
	if err := validateLocator(locator); err != nil {
		return Snapshot{}, err
	}
	normalized, err := normalizeTerms(terms)
	if err != nil {
		return Snapshot{}, err
	}
	if len(normalized) > service.maxTerms {
		return Snapshot{}, indexError(
			result.CodeConstraint,
			fmt.Sprintf("Agent index has %d terms; maximum is %d", len(normalized), service.maxTerms),
		)
	}
	return service.replaceIn(ctx, tx, Snapshot{
		Version: Version, Locator: locator, Source: SourceAgent,
		State: StateActive, Terms: normalized,
	})
}

func (service *Service) InvalidateIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
) (Snapshot, error) {
	if err := validateLocator(locator); err != nil {
		return Snapshot{}, err
	}
	return service.replaceIn(ctx, tx, Snapshot{
		Version: Version, Locator: locator, Source: SourceAgent,
		State: StateInvalid, Terms: []string{},
	})
}

func (service *Service) replaceIn(
	ctx context.Context,
	tx store.Tx,
	next Snapshot,
) (Snapshot, error) {
	current, exists, err := loadSnapshot(ctx, tx, snapshotKey(next.Locator))
	if err != nil {
		return Snapshot{}, err
	}
	if exists && next.Revision <= current.Revision {
		return Snapshot{}, indexError(
			result.CodeRevisionConflict,
			fmt.Sprintf("Agent index revision is %d; new Row revision is %d", current.Revision, next.Revision),
		)
	}
	if exists {
		for _, term := range current.Terms {
			if err := removePosting(ctx, tx, current.DatabaseID, term, current.Locator); err != nil {
				return Snapshot{}, err
			}
		}
	}
	for _, term := range next.Terms {
		if err := appendPosting(ctx, tx, next.DatabaseID, term, Posting{
			Locator: next.Locator, Source: SourceAgent,
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
	if strings.TrimSpace(databaseID) == "" {
		return nil, indexError(result.CodeValidation, "Agent index lookup requires database ID")
	}
	if limit < 1 || limit > maxLookup {
		return nil, indexError(result.CodeValidation, "Agent index lookup limit must be between 1 and 1000")
	}
	normalized, err := normalizeTerm(term)
	if err != nil {
		return nil, err
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.LookupIn(ctx, tx, databaseID, normalized, limit)
}

func (service *Service) LookupIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
	limit int,
) ([]Posting, error) {
	if strings.TrimSpace(databaseID) == "" {
		return nil, indexError(result.CodeValidation, "Agent index lookup requires database ID")
	}
	if limit < 1 || limit > maxLookup {
		return nil, indexError(result.CodeValidation, "Agent index lookup limit must be between 1 and 1000")
	}
	normalized, err := normalizeTerm(term)
	if err != nil {
		return nil, err
	}
	postings, err := loadPostings(ctx, tx, databaseID, normalized)
	if err != nil {
		return nil, err
	}
	if len(postings) > limit {
		postings = postings[:limit]
	}
	return postings, nil
}

func normalizeTerms(terms []string) ([]string, error) {
	seen := make(map[string]struct{}, len(terms))
	normalized := make([]string, 0, len(terms))
	for _, term := range terms {
		value, err := normalizeTerm(term)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	return normalized, nil
}

func normalizeTerm(term string) (string, error) {
	if !utf8.ValidString(term) {
		return "", indexError(result.CodeValidation, "Agent index term is not valid UTF-8")
	}
	normalized := strings.ToLower(strings.Join(strings.Fields(term), " "))
	if normalized == "" {
		return "", indexError(result.CodeValidation, "Agent index term is empty")
	}
	if utf8.RuneCountInString(normalized) > maxTermRunes {
		return "", indexError(result.CodeValueTooLong, "Agent index term exceeds 128 characters")
	}
	return normalized, nil
}

func validateLocator(locator Locator) error {
	if strings.TrimSpace(locator.DatabaseID) == "" || strings.TrimSpace(locator.TableID) == "" ||
		locator.Revision == 0 {
		return indexError(result.CodeValidation, "Agent index locator is incomplete")
	}
	if _, err := logical.Validate(logical.Constraint{
		Name: "row_id", Definition: logical.Definition{Kind: logical.KindRelationID},
	}, locator.RowID); err != nil {
		return err
	}
	return nil
}

func appendPosting(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
	posting Posting,
) error {
	postings, err := loadPostings(ctx, tx, databaseID, term)
	if err != nil {
		return err
	}
	for _, existing := range postings {
		if sameRow(existing.Locator, posting.Locator) {
			return indexError(result.CodeAlreadyExists, "Agent posting already exists")
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

func removePosting(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
	locator Locator,
) error {
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
		return indexError(result.CodeInternal, "Agent snapshot references a missing posting")
	}
	return savePostings(ctx, tx, databaseID, term, filtered)
}

func sameRow(left, right Locator) bool {
	return left.DatabaseID == right.DatabaseID && left.TableID == right.TableID &&
		left.RowID == right.RowID
}

func loadSnapshot(
	ctx context.Context,
	tx store.Tx,
	key string,
) (Snapshot, bool, error) {
	encoded, err := tx.Get(ctx, snapshotBucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return Snapshot{}, false, nil
	}
	if err != nil {
		return Snapshot{}, false, stableError(err)
	}
	var snapshot Snapshot
	if err := decodeJSON(encoded, &snapshot); err != nil || validateSnapshot(snapshot) != nil {
		return Snapshot{}, false, indexError(result.CodeInternal, "Agent index snapshot is corrupt")
	}
	return snapshot, true, nil
}

func loadPostings(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
) ([]Posting, error) {
	encoded, err := tx.Get(ctx, postingBucket, postingKey(databaseID, term))
	if errors.Is(err, store.ErrNotFound) {
		return []Posting{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var record postingRecord
	if err := decodeJSON(encoded, &record); err != nil ||
		record.Version != Version || record.Postings == nil {
		return nil, indexError(result.CodeInternal, "Agent posting list is corrupt")
	}
	seen := make(map[string]struct{}, len(record.Postings))
	for _, posting := range record.Postings {
		if posting.Source != SourceAgent || validateLocator(posting.Locator) != nil ||
			posting.DatabaseID != databaseID {
			return nil, indexError(result.CodeInternal, "Agent posting list is corrupt")
		}
		key := posting.TableID + "\x00" + posting.RowID
		if _, duplicate := seen[key]; duplicate {
			return nil, indexError(result.CodeInternal, "Agent posting list is corrupt")
		}
		seen[key] = struct{}{}
	}
	return record.Postings, nil
}

func savePostings(
	ctx context.Context,
	tx store.Tx,
	databaseID, term string,
	postings []Posting,
) error {
	key := postingKey(databaseID, term)
	if len(postings) == 0 {
		if err := tx.Delete(ctx, postingBucket, key); err != nil &&
			!errors.Is(err, store.ErrNotFound) {
			return stableError(err)
		}
		return nil
	}
	return putJSON(ctx, tx, postingBucket, key, postingRecord{
		Version: Version, Postings: postings,
	})
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Version != Version || snapshot.Source != SourceAgent ||
		(snapshot.State != StateActive && snapshot.State != StateInvalid) ||
		validateLocator(snapshot.Locator) != nil || snapshot.Terms == nil ||
		(snapshot.State == StateInvalid && len(snapshot.Terms) != 0) {
		return errors.New("invalid snapshot")
	}
	normalized, err := normalizeTerms(snapshot.Terms)
	if err != nil || len(normalized) != len(snapshot.Terms) {
		return errors.New("invalid snapshot terms")
	}
	for index := range normalized {
		if normalized[index] != snapshot.Terms[index] {
			return errors.New("unnormalized snapshot terms")
		}
	}
	return nil
}

func putJSON(ctx context.Context, tx store.Tx, bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return indexError(result.CodeInternal, "Agent index record could not be encoded")
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

func postingKey(databaseID, term string) string {
	return databaseID + "\x00" + term
}

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
		return indexError(result.CodeCancelled, "Agent index operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return indexError(result.CodeDeadlineExceeded, "Agent index operation exceeded its deadline")
	default:
		return indexError(result.CodeInternal, "Agent index operation failed")
	}
}
