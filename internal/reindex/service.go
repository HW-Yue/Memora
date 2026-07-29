package reindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	Version     = "memora.reindex/v1"
	taskBucket  = "pending_reindex"
	indexBucket = "pending_reindex_index"
	indexKey    = "tasks"
)

type State string

const (
	StatePending   State = "pending"
	StateLeased    State = "leased"
	StateFailed    State = "failed"
	StateCompleted State = "completed"
)

type Locator struct {
	DatabaseID string
	TableID    string
	RowID      string
	Revision   uint64
}

type Task struct {
	Version    string    `json:"version"`
	DatabaseID string    `json:"database_id"`
	TableID    string    `json:"table_id"`
	RowID      string    `json:"row_id"`
	Revision   uint64    `json:"revision"`
	NeedAgent  bool      `json:"need_agent"`
	NeedRouter bool      `json:"need_router"`
	State      State     `json:"state"`
	Attempts   uint64    `json:"attempts"`
	LeaseOwner string    `json:"lease_owner,omitempty"`
	LeaseToken string    `json:"lease_token,omitempty"`
	LeaseUntil time.Time `json:"lease_until,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Service struct{}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

func New() *Service { return &Service{} }

func (service *Service) EnqueueIn(
	ctx context.Context,
	tx store.Tx,
	locator Locator,
	needAgent, needRouter bool,
	now time.Time,
) (Task, error) {
	if err := validateLocator(locator); err != nil {
		return Task{}, err
	}
	if !needAgent && !needRouter {
		return Task{}, queueError(result.CodeValidation, "reindex task has no requested work")
	}
	key := locatorKey(locator)
	existing, found, err := loadTask(ctx, tx, key)
	if err != nil {
		return Task{}, err
	}
	if found && existing.Revision > locator.Revision {
		return Task{}, queueError(result.CodeRevisionConflict, "reindex task revision is stale")
	}
	if found && existing.Revision == locator.Revision {
		existing.NeedAgent = existing.NeedAgent || needAgent
		existing.NeedRouter = existing.NeedRouter || needRouter
		if err := saveTask(ctx, tx, key, existing); err != nil {
			return Task{}, err
		}
		return existing, nil
	}
	task := Task{
		Version: Version, DatabaseID: locator.DatabaseID, TableID: locator.TableID,
		RowID: locator.RowID, Revision: locator.Revision,
		NeedAgent: needAgent, NeedRouter: needRouter,
		State: StatePending, UpdatedAt: now.UTC(),
	}
	if err := saveTask(ctx, tx, key, task); err != nil {
		return Task{}, err
	}
	keys, err := loadIndex(ctx, tx)
	if err != nil {
		return Task{}, err
	}
	if !contains(keys, key) {
		keys = append(keys, key)
		sort.Strings(keys)
	}
	return task, saveIndex(ctx, tx, keys)
}

func (service *Service) ListIn(ctx context.Context, tx store.Tx) ([]Task, error) {
	keys, err := loadIndex(ctx, tx)
	if err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(keys))
	for _, key := range keys {
		task, found, err := loadTask(ctx, tx, key)
		if err != nil {
			return nil, err
		}
		if found {
			tasks = append(tasks, task)
		}
	}
	return tasks, nil
}

func (service *Service) ClaimIn(
	ctx context.Context,
	tx store.Tx,
	owner string,
	now time.Time,
	lease time.Duration,
) (Task, error) {
	if strings.TrimSpace(owner) == "" || lease <= 0 {
		return Task{}, queueError(result.CodeValidation, "reindex lease requires owner and positive duration")
	}
	tasks, err := service.ListIn(ctx, tx)
	if err != nil {
		return Task{}, err
	}
	for _, task := range tasks {
		if task.State != StatePending &&
			!(task.State == StateLeased && !task.LeaseUntil.After(now)) {
			continue
		}
		task.State = StateLeased
		task.Attempts++
		task.LeaseOwner = owner
		task.LeaseToken = owner + ":" + taskKeyRevision(task)
		task.LeaseUntil = now.UTC().Add(lease)
		task.LastError = ""
		task.UpdatedAt = now.UTC()
		if err := saveTask(ctx, tx, locatorKey(taskLocator(task)), task); err != nil {
			return Task{}, err
		}
		return task, nil
	}
	return Task{}, queueError(result.CodeNotFound, "no pending reindex task is available")
}

func (service *Service) ValidateClaimIn(
	ctx context.Context,
	tx store.Tx,
	claim Task,
	now time.Time,
) (Task, error) {
	current, found, err := loadTask(ctx, tx, locatorKey(taskLocator(claim)))
	if err != nil {
		return Task{}, err
	}
	if !found || current.Revision != claim.Revision {
		return Task{}, queueError(result.CodeRevisionConflict, "reindex worker result is stale")
	}
	if current.State == StateCompleted && current.LeaseToken == claim.LeaseToken {
		return current, nil
	}
	if current.State != StateLeased || current.LeaseToken == "" ||
		current.LeaseToken != claim.LeaseToken {
		return Task{}, queueError(result.CodeRevisionConflict, "reindex lease is stale")
	}
	if !current.LeaseUntil.After(now) {
		return Task{}, queueError(result.CodeRevisionConflict, "reindex lease has expired")
	}
	return current, nil
}

func (service *Service) CompleteIn(
	ctx context.Context,
	tx store.Tx,
	claim Task,
	now time.Time,
) error {
	current, err := service.ValidateClaimIn(ctx, tx, claim, now)
	if err != nil {
		return err
	}
	if current.State == StateCompleted {
		return nil
	}
	current.State = StateCompleted
	current.LeaseOwner = ""
	current.LeaseUntil = time.Time{}
	current.LastError = ""
	current.UpdatedAt = now.UTC()
	return saveTask(ctx, tx, locatorKey(taskLocator(current)), current)
}

func (service *Service) FailIn(
	ctx context.Context,
	tx store.Tx,
	claim Task,
	message string,
	now time.Time,
) error {
	current, err := service.ValidateClaimIn(ctx, tx, claim, now)
	if err != nil {
		return err
	}
	if current.State == StateCompleted {
		return nil
	}
	current.State = StateFailed
	current.LeaseOwner = ""
	current.LeaseUntil = time.Time{}
	current.LastError = strings.TrimSpace(message)
	current.UpdatedAt = now.UTC()
	return saveTask(ctx, tx, locatorKey(taskLocator(current)), current)
}

func (service *Service) RetryIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, tableID, rowID string,
	now time.Time,
) error {
	locator := Locator{DatabaseID: databaseID, TableID: tableID, RowID: rowID, Revision: 1}
	current, found, err := loadTask(ctx, tx, locatorKey(locator))
	if err != nil {
		return err
	}
	if !found {
		return queueError(result.CodeNotFound, "reindex task was not found")
	}
	if current.State == StateCompleted {
		return nil
	}
	current.State = StatePending
	current.LeaseOwner, current.LeaseToken = "", ""
	current.LeaseUntil = time.Time{}
	current.UpdatedAt = now.UTC()
	return saveTask(ctx, tx, locatorKey(locator), current)
}

func (service *Service) RemoveIn(
	ctx context.Context,
	tx store.Tx,
	databaseID, tableID, rowID string,
) error {
	locator := Locator{DatabaseID: databaseID, TableID: tableID, RowID: rowID, Revision: 1}
	key := locatorKey(locator)
	if err := tx.Delete(ctx, taskBucket, key); err != nil &&
		!errors.Is(err, store.ErrNotFound) {
		return err
	}
	keys, err := loadIndex(ctx, tx)
	if err != nil {
		return err
	}
	filtered := keys[:0]
	for _, value := range keys {
		if value != key {
			filtered = append(filtered, value)
		}
	}
	return saveIndex(ctx, tx, filtered)
}

func validateLocator(locator Locator) error {
	if locator.DatabaseID == "" || locator.TableID == "" ||
		locator.RowID == "" || locator.Revision == 0 {
		return queueError(result.CodeValidation, "reindex locator is incomplete")
	}
	return nil
}

func taskLocator(task Task) Locator {
	return Locator{DatabaseID: task.DatabaseID, TableID: task.TableID, RowID: task.RowID, Revision: task.Revision}
}

func locatorKey(locator Locator) string {
	return locator.DatabaseID + "\x00" + locator.TableID + "\x00" + locator.RowID
}

func taskKeyRevision(task Task) string {
	return fmt.Sprintf(
		"%s:%s:%s:%d:%d",
		task.DatabaseID, task.TableID, task.RowID, task.Revision, task.Attempts,
	)
}

type indexRecord struct {
	Version string   `json:"version"`
	Keys    []string `json:"keys"`
}

func loadTask(ctx context.Context, tx store.Tx, key string) (Task, bool, error) {
	encoded, err := tx.Get(ctx, taskBucket, key)
	if errors.Is(err, store.ErrNotFound) {
		return Task{}, false, nil
	}
	if err != nil {
		return Task{}, false, err
	}
	var task Task
	if json.Unmarshal(encoded, &task) != nil || task.Version != Version {
		return Task{}, false, queueError(result.CodeInternal, "reindex task is corrupt")
	}
	return task, true, nil
}

func saveTask(ctx context.Context, tx store.Tx, key string, task Task) error {
	encoded, err := json.Marshal(task)
	if err != nil {
		return queueError(result.CodeInternal, "reindex task could not be encoded")
	}
	return tx.Put(ctx, taskBucket, key, encoded)
}

func loadIndex(ctx context.Context, tx store.Tx) ([]string, error) {
	encoded, err := tx.Get(ctx, indexBucket, indexKey)
	if errors.Is(err, store.ErrNotFound) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	var record indexRecord
	if json.Unmarshal(encoded, &record) != nil || record.Version != Version || record.Keys == nil {
		return nil, queueError(result.CodeInternal, "reindex task index is corrupt")
	}
	return record.Keys, nil
}

func saveIndex(ctx context.Context, tx store.Tx, keys []string) error {
	encoded, err := json.Marshal(indexRecord{Version: Version, Keys: keys})
	if err != nil {
		return queueError(result.CodeInternal, "reindex task index could not be encoded")
	}
	return tx.Put(ctx, indexBucket, indexKey, encoded)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func queueError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
