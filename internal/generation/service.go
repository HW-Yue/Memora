package generation

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	Version          = "memora.index-generation/v1"
	generationBucket = "index_generations"
	manifestBucket   = "index_generation_manifests"
	indexBucket      = "index_generation_index"
	indexKey         = "generations"
)

type Kind string

const (
	KindRouter     Kind = "router"
	KindAgent      Kind = "agent"
	KindMechanical Kind = "mechanical"
)

type State string

const (
	StateStaged  State = "staged"
	StateActive  State = "active"
	StateRetired State = "retired"
)

type Definition struct {
	DatabaseID            string
	Kind                  Kind
	ID                    string
	CoveredCommitSequence uint64
	Checksum              string
}

type Generation struct {
	Version               string `json:"version"`
	DatabaseID            string `json:"database_id"`
	Kind                  Kind   `json:"kind"`
	ID                    string `json:"generation_id"`
	CoveredCommitSequence uint64 `json:"covered_commit_sequence"`
	Checksum              string `json:"checksum"`
	Validated             bool   `json:"validated"`
	State                 State  `json:"state"`
}

type Active struct {
	ID                    string `json:"generation_id"`
	CoveredCommitSequence uint64 `json:"covered_commit_sequence"`
	Checksum              string `json:"checksum"`
}

type Manifest struct {
	Version    string          `json:"version"`
	DatabaseID string          `json:"database_id"`
	Revision   uint64          `json:"revision"`
	Active     map[Kind]Active `json:"active"`
}

type Service struct {
	store store.Store
	mu    sync.Mutex
	pins  map[string]int
}

type Pin struct {
	service  *Service
	manifest Manifest
	once     sync.Once
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New(database store.Store) *Service {
	return &Service{store: database, pins: make(map[string]int)}
}

func (service *Service) Stage(
	ctx context.Context,
	definition Definition,
) (Generation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Generation{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	value, err := service.StageIn(ctx, tx, definition)
	if err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Generation{}, stableError(err)
	}
	return value, nil
}

func (service *Service) StageIn(
	ctx context.Context,
	tx store.Tx,
	definition Definition,
) (Generation, error) {
	if err := validateDefinition(definition); err != nil {
		return Generation{}, err
	}
	key := generationKey(definition.DatabaseID, definition.Kind, definition.ID)
	if current, found, err := loadGeneration(ctx, tx, key); err != nil {
		return Generation{}, err
	} else if found {
		if current.CoveredCommitSequence == definition.CoveredCommitSequence &&
			current.Checksum == definition.Checksum {
			return current, nil
		}
		return Generation{}, generationError(result.CodeAlreadyExists, "generation ID already exists")
	}
	value := Generation{
		Version: Version, DatabaseID: definition.DatabaseID,
		Kind: definition.Kind, ID: definition.ID,
		CoveredCommitSequence: definition.CoveredCommitSequence,
		Checksum:              definition.Checksum, State: StateStaged,
	}
	if err := saveGeneration(ctx, tx, key, value); err != nil {
		return Generation{}, err
	}
	keys, err := loadIndex(ctx, tx)
	if err != nil {
		return Generation{}, err
	}
	keys = append(keys, key)
	sort.Strings(keys)
	return value, saveIndex(ctx, tx, keys)
}

func (service *Service) Validate(
	ctx context.Context,
	databaseID string,
	kind Kind,
	id, checksum string,
) (Generation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Generation{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	value, err := service.ValidateIn(ctx, tx, databaseID, kind, id, checksum)
	if err != nil {
		return Generation{}, err
	}
	if err := tx.Commit(); err != nil {
		return Generation{}, stableError(err)
	}
	return value, nil
}

func (service *Service) ValidateIn(
	ctx context.Context,
	tx store.Tx,
	databaseID string,
	kind Kind,
	id, checksum string,
) (Generation, error) {
	value, found, err := loadGeneration(ctx, tx, generationKey(databaseID, kind, id))
	if err != nil {
		return Generation{}, err
	}
	if !found {
		return Generation{}, generationError(result.CodeNotFound, "generation was not found")
	}
	if strings.TrimSpace(checksum) == "" || value.Checksum != checksum {
		return Generation{}, generationError(result.CodeConstraint, "generation checksum validation failed")
	}
	value.Validated = true
	return value, saveGeneration(ctx, tx, generationKey(databaseID, kind, id), value)
}

func (service *Service) Publish(
	ctx context.Context,
	databaseID string,
	expectedRevision uint64,
	updates map[Kind]string,
) (Manifest, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Manifest{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	manifest, err := service.PublishIn(ctx, tx, databaseID, expectedRevision, updates)
	if err != nil {
		return Manifest{}, err
	}
	if err := tx.Commit(); err != nil {
		return Manifest{}, stableError(err)
	}
	return manifest, nil
}

func (service *Service) PublishIn(
	ctx context.Context,
	tx store.Tx,
	databaseID string,
	expectedRevision uint64,
	updates map[Kind]string,
) (Manifest, error) {
	if strings.TrimSpace(databaseID) == "" || len(updates) == 0 {
		return Manifest{}, generationError(result.CodeValidation, "manifest publish requires database and updates")
	}
	current, found, err := loadManifest(ctx, tx, databaseID)
	if err != nil {
		return Manifest{}, err
	}
	if !found {
		current = Manifest{
			Version: Version, DatabaseID: databaseID,
			Active: make(map[Kind]Active),
		}
	}
	if current.Revision != expectedRevision {
		return Manifest{}, generationError(result.CodeRevisionConflict, "generation manifest revision conflict")
	}
	next := cloneManifest(current)
	for kind, id := range updates {
		if !validKind(kind) || strings.TrimSpace(id) == "" {
			return Manifest{}, generationError(result.CodeValidation, "manifest update is invalid")
		}
		value, found, err := loadGeneration(ctx, tx, generationKey(databaseID, kind, id))
		if err != nil {
			return Manifest{}, err
		}
		if !found {
			return Manifest{}, generationError(result.CodeNotFound, "published generation was not found")
		}
		if !value.Validated {
			return Manifest{}, generationError(result.CodeConstraint, "published generation is not validated")
		}
		if old, exists := next.Active[kind]; exists && old.ID != id {
			previous, found, err := loadGeneration(
				ctx, tx, generationKey(databaseID, kind, old.ID),
			)
			if err != nil {
				return Manifest{}, err
			}
			if !found {
				return Manifest{}, generationError(result.CodeInternal, "active generation record is missing")
			}
			previous.State = StateRetired
			if err := saveGeneration(
				ctx, tx, generationKey(databaseID, kind, old.ID), previous,
			); err != nil {
				return Manifest{}, err
			}
		}
		value.State = StateActive
		if err := saveGeneration(ctx, tx, generationKey(databaseID, kind, id), value); err != nil {
			return Manifest{}, err
		}
		next.Active[kind] = Active{
			ID: id, CoveredCommitSequence: value.CoveredCommitSequence,
			Checksum: value.Checksum,
		}
	}
	if len(next.Active) != 3 {
		return Manifest{}, generationError(
			result.CodeValidation,
			"initial manifest must publish router, agent, and mechanical generations",
		)
	}
	for _, kind := range allKinds() {
		if _, exists := next.Active[kind]; !exists {
			return Manifest{}, generationError(result.CodeValidation, "manifest active combination is incomplete")
		}
	}
	next.Revision++
	if err := saveManifest(ctx, tx, next); err != nil {
		return Manifest{}, err
	}
	return next, nil
}

func (service *Service) Pin(ctx context.Context, databaseID string) (*Pin, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	manifest, found, err := loadManifest(ctx, tx, databaseID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, generationError(result.CodeNotFound, "generation manifest was not found")
	}
	for kind, active := range manifest.Active {
		service.pins[generationKey(databaseID, kind, active.ID)]++
	}
	return &Pin{service: service, manifest: cloneManifest(manifest)}, nil
}

func (pin *Pin) Manifest() Manifest {
	if pin == nil {
		return Manifest{}
	}
	return cloneManifest(pin.manifest)
}

func (pin *Pin) Release() {
	if pin == nil || pin.service == nil {
		return
	}
	pin.once.Do(func() {
		pin.service.mu.Lock()
		defer pin.service.mu.Unlock()
		for kind, active := range pin.manifest.Active {
			key := generationKey(pin.manifest.DatabaseID, kind, active.ID)
			if pin.service.pins[key] <= 1 {
				delete(pin.service.pins, key)
			} else {
				pin.service.pins[key]--
			}
		}
	})
}

func (service *Service) GC(ctx context.Context, databaseID string) ([]Generation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return nil, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	manifest, found, err := loadManifest(ctx, tx, databaseID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, generationError(result.CodeNotFound, "generation manifest was not found")
	}
	keys, err := loadIndex(ctx, tx)
	if err != nil {
		return nil, err
	}
	kept := keys[:0]
	collected := make([]Generation, 0)
	for _, key := range keys {
		value, found, err := loadGeneration(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		active := manifest.Active[value.Kind]
		if value.DatabaseID == databaseID && value.State == StateRetired &&
			active.ID != value.ID && service.pins[key] == 0 {
			if err := tx.Delete(ctx, generationBucket, key); err != nil {
				return nil, stableError(err)
			}
			collected = append(collected, value)
			continue
		}
		kept = append(kept, key)
	}
	if err := saveIndex(ctx, tx, kept); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, stableError(err)
	}
	sort.Slice(collected, func(left, right int) bool {
		if collected[left].Kind != collected[right].Kind {
			return collected[left].Kind < collected[right].Kind
		}
		return collected[left].ID < collected[right].ID
	})
	return collected, nil
}

func (service *Service) Get(
	ctx context.Context,
	databaseID string,
	kind Kind,
	id string,
) (Generation, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Generation{}, stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	value, found, err := loadGeneration(ctx, tx, generationKey(databaseID, kind, id))
	if err != nil {
		return Generation{}, err
	}
	if !found {
		return Generation{}, generationError(result.CodeNotFound, "generation was not found")
	}
	return value, nil
}

func validateDefinition(definition Definition) error {
	if strings.TrimSpace(definition.DatabaseID) == "" ||
		!validKind(definition.Kind) || strings.TrimSpace(definition.ID) == "" ||
		definition.CoveredCommitSequence == 0 || strings.TrimSpace(definition.Checksum) == "" {
		return generationError(result.CodeValidation, "generation definition is incomplete")
	}
	return nil
}

func validKind(kind Kind) bool {
	return kind == KindRouter || kind == KindAgent || kind == KindMechanical
}

func allKinds() []Kind {
	return []Kind{KindRouter, KindAgent, KindMechanical}
}

func cloneManifest(value Manifest) Manifest {
	clone := value
	clone.Active = make(map[Kind]Active, len(value.Active))
	for kind, active := range value.Active {
		clone.Active[kind] = active
	}
	return clone
}

func generationKey(databaseID string, kind Kind, id string) string {
	return databaseID + "\x00" + string(kind) + "\x00" + id
}

type indexRecord struct {
	Version string   `json:"version"`
	Keys    []string `json:"keys"`
}

func loadGeneration(
	ctx context.Context,
	tx store.Tx,
	key string,
) (Generation, bool, error) {
	encoded, err := tx.Get(ctx, generationBucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return Generation{}, false, nil
	}
	if err != nil {
		return Generation{}, false, stableError(err)
	}
	var value Generation
	if json.Unmarshal(encoded, &value) != nil || !validGeneration(value) {
		return Generation{}, false, generationError(result.CodeInternal, "generation record is corrupt")
	}
	return value, true, nil
}

func saveGeneration(
	ctx context.Context,
	tx store.Tx,
	key string,
	value Generation,
) error {
	return putJSON(ctx, tx, generationBucket, key, value)
}

func loadManifest(
	ctx context.Context,
	tx store.Tx,
	databaseID string,
) (Manifest, bool, error) {
	encoded, err := tx.Get(ctx, manifestBucket, databaseID)
	if errors.Is(err, store.ErrNotFound) {
		return Manifest{}, false, nil
	}
	if err != nil {
		return Manifest{}, false, stableError(err)
	}
	var value Manifest
	if json.Unmarshal(encoded, &value) != nil || !validManifest(value, databaseID) {
		return Manifest{}, false, generationError(result.CodeInternal, "generation manifest is corrupt")
	}
	return value, true, nil
}

func saveManifest(ctx context.Context, tx store.Tx, value Manifest) error {
	return putJSON(ctx, tx, manifestBucket, value.DatabaseID, value)
}

func loadIndex(ctx context.Context, tx store.Tx) ([]string, error) {
	encoded, err := tx.Get(ctx, indexBucket, indexKey)
	if errors.Is(err, store.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, stableError(err)
	}
	var value indexRecord
	if json.Unmarshal(encoded, &value) != nil || value.Version != Version || value.Keys == nil {
		return nil, generationError(result.CodeInternal, "generation index is corrupt")
	}
	return value.Keys, nil
}

func saveIndex(ctx context.Context, tx store.Tx, keys []string) error {
	return putJSON(ctx, tx, indexBucket, indexKey, indexRecord{Version: Version, Keys: keys})
}

func putJSON(ctx context.Context, tx store.Tx, bucket, key string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return generationError(result.CodeInternal, "generation metadata could not be encoded")
	}
	return stableError(tx.Put(ctx, bucket, key, encoded))
}

func generationError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}

func validGeneration(value Generation) bool {
	return value.Version == Version &&
		strings.TrimSpace(value.DatabaseID) != "" &&
		validKind(value.Kind) &&
		strings.TrimSpace(value.ID) != "" &&
		value.CoveredCommitSequence > 0 &&
		strings.TrimSpace(value.Checksum) != "" &&
		(value.State == StateStaged || value.State == StateActive || value.State == StateRetired)
}

func validManifest(value Manifest, databaseID string) bool {
	if value.Version != Version || value.DatabaseID != databaseID ||
		value.Revision == 0 || len(value.Active) != len(allKinds()) {
		return false
	}
	for _, kind := range allKinds() {
		active, exists := value.Active[kind]
		if !exists || strings.TrimSpace(active.ID) == "" ||
			active.CoveredCommitSequence == 0 || strings.TrimSpace(active.Checksum) == "" {
			return false
		}
	}
	return true
}

func stableError(err error) error {
	if err == nil {
		return nil
	}
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return err
	}
	return generationError(result.CodeInternal, "generation store failed")
}
