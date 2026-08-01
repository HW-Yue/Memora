package routetrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/store"
	"github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestRecordCreatesCanonicalSequenceAndIDLocator(t *testing.T) {
	ctx := context.Background()
	database, service := newTestService(t)
	want := draft(1)
	created, err := service.Record(ctx, want)
	if err != nil || created.Sequence != 1 || created.Checksum == "" || created.Validate() != nil {
		t.Fatalf("Record() = %#v, %v", created, err)
	}
	got, err := service.Get(ctx, created.TraceID, "db_work")
	if err != nil || !reflect.DeepEqual(got, created) {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
	retried, err := service.Record(ctx, want)
	if err != nil || !reflect.DeepEqual(retried, created) {
		t.Fatalf("idempotent Record() = %#v, %v", retried, err)
	}
	if _, err := service.Get(ctx, created.TraceID, "db_private"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope Get error = %v", err)
	}
	read, err := database.Begin(ctx, store.ReadOnly)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	entries, err := read.Scan(ctx, bodyBucket)
	if err != nil || len(entries) != 1 {
		t.Fatalf("stored bodies = %d, %v", len(entries), err)
	}
	var raw map[string]any
	if err := json.Unmarshal(entries[0].Value, &raw); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "question", "values", "purpose", "description", "reasoning"} {
		if _, exists := raw[forbidden]; exists {
			t.Fatalf("trace persisted forbidden field %q", forbidden)
		}
	}
}

func TestListPinsHighWaterAndPruneInvalidatesOldSnapshot(t *testing.T) {
	ctx := context.Background()
	_, service := newTestService(t)
	for sequence := 1; sequence <= 3; sequence++ {
		if _, err := service.Record(ctx, draft(sequence)); err != nil {
			t.Fatal(err)
		}
	}
	first, snapshot, more, err := service.List(ctx, "db_work", 0, Snapshot{}, 1)
	if err != nil || !more || len(first) != 1 || snapshot.HighWater != 3 || snapshot.RetentionEpoch != 1 {
		t.Fatalf("first List = %#v, snapshot %#v, more %v, %v", first, snapshot, more, err)
	}
	if _, err := service.Record(ctx, draft(4)); err != nil {
		t.Fatal(err)
	}
	tail, continued, more, err := service.List(ctx, "db_work", first[0].Sequence, snapshot, 10)
	if err != nil || more || continued != snapshot || len(tail) != 2 || tail[1].Sequence != 3 {
		t.Fatalf("continued List = %#v, snapshot %#v, more %v, %v", tail, continued, more, err)
	}
	deleted, err := service.PruneExpired(ctx, time.Unix(10_000, 0).UTC())
	if err != nil || deleted != 4 {
		t.Fatalf("PruneExpired() = %d, %v", deleted, err)
	}
	if _, _, _, err := service.List(ctx, "db_work", first[0].Sequence, snapshot, 10); !errors.Is(err, ErrSnapshotExpired) {
		t.Fatalf("List(old snapshot after prune) error = %v", err)
	}
	fresh, freshSnapshot, more, err := service.List(ctx, "", 0, Snapshot{}, 10)
	if err != nil || more || len(fresh) != 0 || freshSnapshot.HighWater != 4 || freshSnapshot.RetentionEpoch != 2 {
		t.Fatalf("fresh List after prune = %#v, snapshot %#v, %v, %v", fresh, freshSnapshot, more, err)
	}
}

func TestReopenPreservesTraceAndSequence(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "traces.memora")
	database, err := nativekv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := New(database).Record(ctx, draft(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativekv.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service := New(reopened)
	got, err := service.Get(ctx, first.TraceID, "")
	if err != nil || got.Checksum != first.Checksum {
		t.Fatalf("reopened Get = %#v, %v", got, err)
	}
	second, err := service.Record(ctx, draft(2))
	if err != nil || second.Sequence != 2 {
		t.Fatalf("reopened Record = %#v, %v", second, err)
	}
}

func TestRecordRejectsBudgetAndIdentityConflicts(t *testing.T) {
	ctx := context.Background()
	_, service := newTestService(t)
	value := draft(1)
	if _, err := service.Record(ctx, value); err != nil {
		t.Fatal(err)
	}
	changed := value
	changed.Status = StatusPartial
	if _, err := service.Record(ctx, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed retry error = %v", err)
	}
	tooMany := draft(2)
	tooMany.Steps[0].CandidateRouteIDs = make([]string, MaxCandidates+1)
	for index := range tooMany.Steps[0].CandidateRouteIDs {
		tooMany.Steps[0].CandidateRouteIDs[index] = fmt.Sprintf("route_%03d", index)
	}
	if _, err := service.Record(ctx, tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("over-budget Record error = %v", err)
	}
}

func TestCorruptBodyOrIDLocatorFailsClosed(t *testing.T) {
	ctx := context.Background()
	for _, corrupt := range []func(context.Context, store.Store, Trace) error{
		func(ctx context.Context, database store.Store, trace Trace) error {
			tx, err := database.Begin(ctx, store.ReadWrite)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := tx.Put(ctx, bodyBucket, bodyKey(trace.Sequence), []byte(`{}`)); err != nil {
				return err
			}
			return tx.Commit()
		},
		func(ctx context.Context, database store.Store, trace Trace) error {
			tx, err := database.Begin(ctx, store.ReadWrite)
			if err != nil {
				return err
			}
			defer tx.Rollback()
			if err := tx.Put(ctx, idBucket, trace.TraceID, []byte(bodyKey(trace.Sequence+1))); err != nil {
				return err
			}
			return tx.Commit()
		},
	} {
		database, service := newTestService(t)
		created, err := service.Record(ctx, draft(1))
		if err != nil {
			t.Fatal(err)
		}
		if err := corrupt(ctx, database, created); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Get(ctx, created.TraceID, ""); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("Get(corrupt) error = %v", err)
		}
	}
}

func TestConcurrentRecordAllocatesGaplessSequence(t *testing.T) {
	ctx := context.Background()
	_, service := newTestService(t)
	const writers = 32
	results := make(chan Trace, writers)
	errorsByWriter := make(chan error, writers)
	var wait sync.WaitGroup
	for number := 1; number <= writers; number++ {
		wait.Add(1)
		go func(number int) {
			defer wait.Done()
			created, err := service.Record(ctx, draft(number))
			if err != nil {
				errorsByWriter <- err
				return
			}
			results <- created
		}(number)
	}
	wait.Wait()
	close(results)
	close(errorsByWriter)
	for err := range errorsByWriter {
		t.Fatal(err)
	}
	seen := make(map[uint64]bool, writers)
	for value := range results {
		seen[value.Sequence] = true
	}
	for sequence := uint64(1); sequence <= writers; sequence++ {
		if !seen[sequence] {
			t.Fatalf("sequence %d was not allocated: %#v", sequence, seen)
		}
	}
}

func TestRecordCommitFaultPublishesNoPartialLocatorOrSequence(t *testing.T) {
	ctx := context.Background()
	database, service := newTestService(t)
	faulted := New(&commitFaultStore{Store: database, fail: true})
	if _, err := faulted.Record(ctx, draft(1)); err == nil {
		t.Fatal("faulted Record succeeded")
	}
	if _, err := service.Get(ctx, draft(1).TraceID, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after commit fault error = %v", err)
	}
	created, err := service.Record(ctx, draft(1))
	if err != nil || created.Sequence != 1 {
		t.Fatalf("Record retry = %#v, %v", created, err)
	}
}

func draft(number int) Draft {
	recorded := time.Unix(int64(number), 0).UTC()
	return Draft{
		TraceID: fmt.Sprintf("trace_%032x", number), RecordedAt: recorded,
		ExpiresAt: recorded.Add(time.Hour), Actor: "agent:test",
		DatabaseID: "db_work", TableID: "tbl_notes", Status: StatusComplete,
		Steps: []Step{
			{Ordinal: 1, Operation: OperationFrame, ParentRouteID: "route_root",
				CandidateRouteIDs: []string{"route_arch", "route_other"}, SelectedRouteID: "route_arch",
				Outcome: OutcomeSucceeded, ElapsedMillis: 3, RemainingBudget: 11},
			{Ordinal: 2, Operation: OperationOpen, SelectedRouteID: "route_arch",
				Locators: []Locator{{RowID: fmt.Sprintf("row_%d", number), Revision: 2}},
				Outcome:  OutcomeSucceeded, ElapsedMillis: 2, RemainingBudget: 9},
		},
	}
}

func newTestService(t *testing.T) (store.Store, *Service) {
	t.Helper()
	database, err := nativekv.Open(filepath.Join(t.TempDir(), "traces.memora"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database, New(database)
}

type commitFaultStore struct {
	store.Store
	fail bool
}

func (database *commitFaultStore) Begin(ctx context.Context, mode store.Mode) (store.Tx, error) {
	tx, err := database.Store.Begin(ctx, mode)
	if err != nil {
		return nil, err
	}
	return &commitFaultTx{Tx: tx, fail: database.fail}, nil
}

type commitFaultTx struct {
	store.Tx
	fail bool
}

func (tx *commitFaultTx) Commit() error {
	if tx.fail {
		return errors.New("injected commit fault")
	}
	return tx.Tx.Commit()
}
