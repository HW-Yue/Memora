package relation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	"github.com/google/uuid"
)

const (
	currentBucket = "relation_current"
	versionBucket = "relation_versions"
	forwardBucket = "relation_forward"
	reverseBucket = "relation_reverse"

	maxTypeCharacters        = 128
	maxDescriptionCharacters = 1200
)

type IDSource interface {
	Next() (string, error)
}

type Clock interface {
	Now() time.Time
}

type Options struct {
	IDs   IDSource
	Clock Clock
}

type Service struct {
	store store.Store
	ids   IDSource
	clock Clock
	mu    sync.Mutex
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store, options Options) *Service {
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	return &Service{store: database, ids: options.IDs, clock: options.Clock}
}

func (service *Service) CreateIn(
	ctx context.Context,
	tx store.Tx,
	definition Definition,
) (Relation, error) {
	if err := validateDefinition(definition); err != nil {
		return Relation{}, err
	}
	service.mu.Lock()
	idValue, err := service.ids.Next()
	now := service.clock.Now().UTC()
	service.mu.Unlock()
	if err != nil || strings.TrimSpace(idValue) == "" {
		return Relation{}, relationError(result.CodeInternal, "relation ID allocation failed")
	}
	relation := Relation{
		Version: Version, ID: "rel_" + idValue,
		Source: definition.Source, Type: strings.TrimSpace(definition.Type),
		Target: definition.Target, Description: definition.Description,
		Revision: 1, CommitSequence: definition.CommitSequence, State: StateLive,
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := tx.Get(ctx, currentBucket, relation.ID); err == nil {
		return Relation{}, relationError(result.CodeAlreadyExists, "allocated relation ID already exists")
	} else if !errors.Is(err, store.ErrNotFound) {
		return Relation{}, stableError(err)
	}
	if err := putRelation(ctx, tx, currentBucket, relation.ID, relation); err != nil {
		return Relation{}, err
	}
	if err := putRelation(ctx, tx, versionBucket, versionKey(relation.ID, 1), relation); err != nil {
		return Relation{}, err
	}
	if err := appendIndex(ctx, tx, forwardBucket, endpointKey(relation.Source), relation.ID); err != nil {
		return Relation{}, err
	}
	if err := appendIndex(ctx, tx, reverseBucket, endpointKey(relation.Target), relation.ID); err != nil {
		return Relation{}, err
	}
	return relation, nil
}

func (service *Service) DeleteIn(
	ctx context.Context,
	tx store.Tx,
	relationID string,
	expectedRevision, commitSequence uint64,
) (Relation, error) {
	if expectedRevision == 0 || commitSequence == 0 {
		return Relation{}, relationError(result.CodeValidation, "relation delete requires revision and commit sequence")
	}
	current, err := getRelation(ctx, tx, currentBucket, relationID)
	if err != nil {
		return Relation{}, err
	}
	if current.State == StateDeleted {
		return Relation{}, relationError(result.CodeNotFound, "relation was not found")
	}
	if current.Revision != expectedRevision {
		return Relation{}, relationError(
			result.CodeRevisionConflict,
			fmt.Sprintf("relation revision is %d; expected %d", current.Revision, expectedRevision),
		)
	}
	current.Revision++
	current.CommitSequence = commitSequence
	current.State = StateDeleted
	service.mu.Lock()
	current.UpdatedAt = service.clock.Now().UTC()
	service.mu.Unlock()
	if err := putRelation(ctx, tx, currentBucket, current.ID, current); err != nil {
		return Relation{}, err
	}
	if err := putRelation(ctx, tx, versionBucket, versionKey(current.ID, current.Revision), current); err != nil {
		return Relation{}, err
	}
	return current, nil
}

func (service *Service) Get(ctx context.Context, relationID string) (Relation, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Relation{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.GetIn(ctx, tx, relationID)
}

func (service *Service) GetIn(ctx context.Context, tx store.Tx, relationID string) (Relation, error) {
	return getRelation(ctx, tx, currentBucket, relationID)
}

func (service *Service) ListOutgoing(ctx context.Context, endpoint Endpoint) ([]Relation, error) {
	return service.list(ctx, forwardBucket, endpoint)
}

func (service *Service) ListIncoming(ctx context.Context, endpoint Endpoint) ([]Relation, error) {
	return service.list(ctx, reverseBucket, endpoint)
}

func (service *Service) ListOutgoingIn(ctx context.Context, tx store.Tx, endpoint Endpoint) ([]Relation, error) {
	return service.listIn(ctx, tx, forwardBucket, endpoint)
}

func (service *Service) ListIncomingIn(ctx context.Context, tx store.Tx, endpoint Endpoint) ([]Relation, error) {
	return service.listIn(ctx, tx, reverseBucket, endpoint)
}

func (service *Service) list(ctx context.Context, bucket string, endpoint Endpoint) ([]Relation, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	return service.listIn(ctx, tx, bucket, endpoint)
}

func (service *Service) listIn(
	ctx context.Context,
	tx store.Tx,
	bucket string,
	endpoint Endpoint,
) ([]Relation, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	ids, err := loadIndex(ctx, tx, bucket, endpointKey(endpoint))
	if err != nil {
		return nil, err
	}
	relations := make([]Relation, 0, len(ids))
	for _, id := range ids {
		value, err := getRelation(ctx, tx, currentBucket, id)
		if err != nil {
			return nil, err
		}
		if value.State == StateLive {
			relations = append(relations, value)
		}
	}
	return relations, nil
}

func (service *Service) History(ctx context.Context, relationID string) ([]Relation, error) {
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getRelation(ctx, tx, currentBucket, relationID)
	if err != nil {
		return nil, err
	}
	versions := []Relation{}
	for revision := uint64(1); revision <= current.Revision; revision++ {
		value, err := getRelation(ctx, tx, versionBucket, versionKey(relationID, revision))
		if err != nil {
			return nil, err
		}
		versions = append(versions, value)
	}
	return versions, nil
}

func validateDefinition(definition Definition) error {
	if err := validateEndpoint(definition.Source); err != nil {
		return err
	}
	if err := validateEndpoint(definition.Target); err != nil {
		return err
	}
	relationType := strings.TrimSpace(definition.Type)
	if relationType == "" || !utf8.ValidString(relationType) ||
		utf8.RuneCountInString(relationType) > maxTypeCharacters {
		return relationError(result.CodeValidation, "relation type must contain 1–128 characters")
	}
	if !utf8.ValidString(definition.Description) ||
		utf8.RuneCountInString(definition.Description) > maxDescriptionCharacters {
		return relationError(result.CodeValueTooLong, "relation description exceeds 1200 characters")
	}
	if definition.CommitSequence == 0 {
		return relationError(result.CodeValidation, "relation requires a commit sequence")
	}
	return nil
}

func validateEndpoint(endpoint Endpoint) error {
	if endpoint.DatabaseID == "" || endpoint.TableID == "" {
		return relationError(result.CodeValidation, "relation endpoint identity is incomplete")
	}
	if _, err := logical.Validate(logical.Constraint{
		Name: "row_id", Definition: logical.Definition{Kind: logical.KindRelationID},
	}, endpoint.RowID); err != nil {
		return err
	}
	return nil
}

func appendIndex(ctx context.Context, tx store.Tx, bucket, key, relationID string) error {
	ids, err := loadIndex(ctx, tx, bucket, key)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if id == relationID {
			return relationError(result.CodeAlreadyExists, "relation index entry already exists")
		}
	}
	ids = append(ids, relationID)
	encoded, err := json.Marshal(indexRecord{Version: indexVersion, RelationIDs: ids})
	if err != nil {
		return relationError(result.CodeInternal, "relation index could not be encoded")
	}
	if err := tx.Put(ctx, bucket, key, encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func loadIndex(ctx context.Context, tx store.Tx, bucket, key string) ([]string, error) {
	encoded, err := tx.Get(ctx, bucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var record indexRecord
	if err := json.Unmarshal(encoded, &record); err != nil ||
		record.Version != indexVersion || record.RelationIDs == nil {
		return nil, relationError(result.CodeInternal, "relation index is corrupt")
	}
	seen := make(map[string]struct{}, len(record.RelationIDs))
	for _, id := range record.RelationIDs {
		if !strings.HasPrefix(id, "rel_") || strings.TrimPrefix(id, "rel_") == "" {
			return nil, relationError(result.CodeInternal, "relation index is corrupt")
		}
		if _, exists := seen[id]; exists {
			return nil, relationError(result.CodeInternal, "relation index is corrupt")
		}
		seen[id] = struct{}{}
	}
	return record.RelationIDs, nil
}

func putRelation(ctx context.Context, tx store.Tx, bucket, key string, relation Relation) error {
	encoded, err := json.Marshal(relation)
	if err != nil {
		return relationError(result.CodeInternal, "relation could not be encoded")
	}
	if err := tx.Put(ctx, bucket, key, encoded); err != nil {
		return stableError(err)
	}
	return nil
}

func getRelation(ctx context.Context, tx store.Tx, bucket, key string) (Relation, error) {
	encoded, err := tx.Get(ctx, bucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return Relation{}, relationError(result.CodeNotFound, "relation was not found")
	}
	if err != nil {
		return Relation{}, stableError(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var value Relation
	if err := decoder.Decode(&value); err != nil || validateStoredRelation(value) != nil {
		return Relation{}, relationError(result.CodeInternal, "relation record is corrupt")
	}
	return value, nil
}

func validateStoredRelation(value Relation) error {
	if value.Version != Version || !strings.HasPrefix(value.ID, "rel_") ||
		strings.TrimPrefix(value.ID, "rel_") == "" || value.Revision == 0 ||
		value.CommitSequence == 0 || value.CreatedAt.IsZero() || value.UpdatedAt.IsZero() ||
		value.UpdatedAt.Before(value.CreatedAt) ||
		(value.State != StateLive && value.State != StateDeleted) {
		return errors.New("invalid relation envelope")
	}
	if err := validateEndpoint(value.Source); err != nil {
		return err
	}
	if err := validateEndpoint(value.Target); err != nil {
		return err
	}
	return validateDefinition(Definition{
		Source: value.Source, Type: value.Type, Target: value.Target,
		Description: value.Description, CommitSequence: value.CommitSequence,
	})
}

func endpointKey(endpoint Endpoint) string {
	return endpoint.DatabaseID + "\x00" + endpoint.TableID + "\x00" + endpoint.RowID
}

func versionKey(id string, revision uint64) string {
	return fmt.Sprintf("%s\x00%020d", id, revision)
}

func relationError(code result.Code, message string) error {
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
		return relationError(result.CodeCancelled, "relation operation was cancelled")
	case errors.Is(err, context.DeadlineExceeded):
		return relationError(result.CodeDeadlineExceeded, "relation operation exceeded its deadline")
	default:
		return relationError(result.CodeInternal, "relation operation failed")
	}
}

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }
