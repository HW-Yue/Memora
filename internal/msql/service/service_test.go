package service_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	msqlservice "github.com/HW-Yue/Memora/internal/msql/service"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestExecuteMSQLRunsAuthorizedProtocolRequest(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, rows, closeFixture := newFixture(t, ctx)
	defer closeFixture()
	session, err := service.OpenSession("agent:query")
	if err != nil {
		t.Fatal(err)
	}

	envelope, err := session.ExecuteMSQL(ctx, request(
		"INSERT INTO work.notes (title, count) VALUES (:title, :count)",
		protocolmsql.StatementInput{
			Parameters: protocolmsql.Parameters{Named: map[string]any{
				"title": "shared service", "count": int64(7),
			}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1,
				MaxAffectedRows:       1,
				Actor:                 "agent:test",
				Source:                "service:test",
				Reason:                "verify neutral adapter",
				SourceKind:            protocolmsql.SourceConversationAssertion,
			},
			Authorization: authorization(protocolmsql.LevelWrite),
		},
	))
	if err != nil {
		t.Fatalf("ExecuteMSQL() error = %v", err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("Envelope.Validate() error = %v: %#v", err, envelope)
	}
	if !envelope.OK || len(envelope.Results) != 1 ||
		envelope.Results[0].Status != protocolmsql.Status(result.StatusSucceeded) ||
		envelope.Results[0].AffectedRows != 1 || envelope.Results[0].Revision == nil ||
		*envelope.Results[0].Revision != 1 || envelope.Results[0].CommitSequence == nil {
		t.Fatalf("envelope = %#v", envelope)
	}

	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 1 || persisted[0].Values["title"] != "shared service" ||
		persisted[0].Values["count"] != int64(7) {
		t.Fatalf("persisted rows = %#v, %v", persisted, err)
	}
}

func TestSessionsOwnIndependentTransactionStateAndCloseRollsBack(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, rows, closeFixture := newFixture(t, ctx)
	defer closeFixture()
	first, err := service.OpenSession("agent:first")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.OpenSession("agent:second")
	if err != nil {
		t.Fatal(err)
	}

	started := first.ExecuteBatch(ctx, batchRequest(
		"begin-insert",
		"BEGIN; INSERT INTO work.notes (title) VALUES ('pending')",
		2,
	))
	if len(started.Results) != 2 || started.Results[1].Status != result.StatusSucceeded {
		t.Fatalf("started = %#v", started)
	}
	otherCommit := second.ExecuteBatch(ctx, batchRequest("other-commit", "COMMIT", 1))
	if len(otherCommit.Results) != 1 || otherCommit.Results[0].Error == nil ||
		otherCommit.Results[0].Error.Code != result.CodeInvalidTransaction {
		t.Fatalf("other session inherited transaction state: %#v", otherCommit)
	}

	if err := service.CloseSession("agent:first"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	persisted, err := rows.List(ctx, "work", "notes", 10)
	if err != nil || len(persisted) != 0 {
		t.Fatalf("rows after session close = %#v, %v", persisted, err)
	}
	if _, err := service.OpenSession("agent:third"); err != nil {
		t.Fatalf("service rejected a new session after one close: %v", err)
	}
}

func TestQueuedCallObservesItsOwnCancellation(t *testing.T) {
	t.Parallel()

	var blocking *blockingCatalog
	service, _, closeFixture := newFixtureWithCatalog(t, context.Background(), func(base executor.Catalog) executor.Catalog {
		blocking = newBlockingCatalog(base)
		return blocking
	})
	defer closeFixture()
	session, err := service.OpenSession("agent:serial")
	if err != nil {
		t.Fatal(err)
	}

	activeDone := make(chan protocolmsql.Envelope, 1)
	go func() {
		envelope, _ := session.ExecuteMSQL(context.Background(), request(
			"SELECT title FROM work.notes LIMIT 1", protocolmsql.StatementInput{
				Authorization: authorization(protocolmsql.LevelRead),
			},
		))
		activeDone <- envelope
	}()
	blocking.waitStarted(t)

	queuedContext, cancelQueued := context.WithCancel(context.Background())
	cancelQueued()
	started := time.Now()
	_, err = session.ExecuteMSQL(queuedContext, request(
		"SELECT title FROM work.notes LIMIT 1", protocolmsql.StatementInput{
			Authorization: authorization(protocolmsql.LevelRead),
		},
	))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("queued ExecuteMSQL() error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("queued cancellation took %s", elapsed)
	}

	blocking.release()
	select {
	case <-activeDone:
	case <-time.After(3 * time.Second):
		t.Fatal("active call did not finish")
	}
}

func TestSessionAndServiceCloseCancelActiveCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		close func(*msqlservice.Service, string) error
	}{
		{name: "session", close: func(service *msqlservice.Service, id string) error {
			return service.CloseSession(id)
		}},
		{name: "service", close: func(service *msqlservice.Service, _ string) error {
			return service.Close()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var blocking *blockingCatalog
			service, _, closeFixture := newFixtureWithCatalog(t, context.Background(), func(base executor.Catalog) executor.Catalog {
				blocking = newBlockingCatalog(base)
				return blocking
			})
			defer closeFixture()
			sessionID := "agent:" + test.name
			session, err := service.OpenSession(sessionID)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan protocolmsql.Envelope, 1)
			go func() {
				envelope, _ := session.ExecuteMSQL(context.Background(), request(
					"SELECT title FROM work.notes LIMIT 1", protocolmsql.StatementInput{
						Authorization: authorization(protocolmsql.LevelRead),
					},
				))
				done <- envelope
			}()
			blocking.waitStarted(t)

			closeDone := make(chan error, 1)
			go func() { closeDone <- test.close(service, sessionID) }()
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("close error = %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("close did not cancel active call")
			}
			select {
			case envelope := <-done:
				if len(envelope.Results) != 1 || envelope.Results[0].Error == nil ||
					envelope.Results[0].Error.Code != protocolmsql.Code(result.CodeCancelled) {
					t.Fatalf("cancelled envelope = %#v", envelope)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("active call did not return after close")
			}
			if _, err := session.ExecuteMSQL(context.Background(), request(
				"SELECT title FROM work.notes LIMIT 1", protocolmsql.StatementInput{
					Authorization: authorization(protocolmsql.LevelRead),
				},
			)); !errors.Is(err, msqlservice.ErrSessionClosed) {
				t.Fatalf("closed session error = %v", err)
			}
			_ = service.Close()
		})
	}
}

func TestServiceCloseRejectsNewSessions(t *testing.T) {
	t.Parallel()

	service := msqlservice.New(context.Background(), msqlservice.Config{})
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenSession("agent:late"); !errors.Is(err, msqlservice.ErrServiceClosed) {
		t.Fatalf("OpenSession() error = %v, want ErrServiceClosed", err)
	}
}

func TestExecuteMSQLRejectsProtocolRequestBeforeTrustedBatchCompatibility(t *testing.T) {
	t.Parallel()

	service := msqlservice.New(context.Background(), msqlservice.Config{})
	defer service.Close()
	session, err := service.OpenSession("agent:validation")
	if err != nil {
		t.Fatal(err)
	}
	_, err = session.ExecuteMSQL(context.Background(), protocolmsql.Request{
		Version: protocolmsql.RequestVersion,
		Source:  "SELECT * FROM work.notes LIMIT 1",
	})
	if !errors.Is(err, protocolmsql.ErrInvalidRequest) {
		t.Fatalf("ExecuteMSQL() error = %v, want ErrInvalidRequest", err)
	}
}

func TestConcurrentExpectedRevisionWritesAcrossSessionsHaveOneWinner(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	service, rows, closeFixture := newFixture(t, ctx)
	defer closeFixture()
	inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "initial"}, row.WriteOptions{
		ExpectedSchemaVersion: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	sessions := make([]*msqlservice.Session, 2)
	for index := range sessions {
		sessions[index], err = service.OpenSession("agent:writer-" + string(rune('a'+index)))
		if err != nil {
			t.Fatal(err)
		}
	}
	start := make(chan struct{})
	results := make(chan protocolmsql.Envelope, len(sessions))
	for index, session := range sessions {
		index, session := index, session
		go func() {
			<-start
			envelope, executeErr := session.ExecuteMSQL(ctx, request(
				"UPDATE work.notes SET title = :title WHERE row_id = :row_id",
				protocolmsql.StatementInput{
					Parameters: protocolmsql.Parameters{Named: map[string]any{
						"title": "writer-" + string(rune('a'+index)), "row_id": inserted.ID,
					}},
					Mutation: protocolmsql.MutationOptions{
						ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1,
					},
					Authorization: authorization(protocolmsql.LevelWrite),
				},
			))
			if executeErr != nil {
				t.Errorf("ExecuteMSQL() error = %v", executeErr)
			}
			results <- envelope
		}()
	}
	close(start)
	succeeded, conflicted := 0, 0
	for range sessions {
		envelope := <-results
		if len(envelope.Results) != 1 {
			t.Fatalf("envelope = %#v", envelope)
		}
		switch envelope.Results[0].Status {
		case protocolmsql.Status(result.StatusSucceeded):
			succeeded++
		case protocolmsql.Status(result.StatusFailed):
			if envelope.Results[0].Error == nil ||
				envelope.Results[0].Error.Code != protocolmsql.Code(result.CodeRevisionConflict) {
				t.Fatalf("failed write = %#v", envelope.Results[0])
			}
			conflicted++
		default:
			t.Fatalf("write status = %q", envelope.Results[0].Status)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded = %d, conflicted = %d", succeeded, conflicted)
	}
	current, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || current.Revision != 2 {
		t.Fatalf("current row = %#v, %v", current, err)
	}
}

func newFixture(t *testing.T, ctx context.Context) (*msqlservice.Service, *row.Service, func()) {
	return newFixtureWithCatalog(t, ctx, nil)
}

func newFixtureWithCatalog(
	t *testing.T,
	ctx context.Context,
	decorate func(executor.Catalog) executor.Catalog,
) (*msqlservice.Service, *row.Service, func()) {
	t.Helper()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT(100)", Purpose: "Title"},
			{Name: "count", Type: "INTEGER", Nullable: true, Purpose: "Count"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	var configuredCatalog executor.Catalog = dictionary
	if decorate != nil {
		configuredCatalog = decorate(dictionary)
	}
	service := msqlservice.New(ctx, msqlservice.Config{Catalog: configuredCatalog, Rows: rows})
	return service, rows, func() {
		if err := service.Close(); err != nil {
			t.Errorf("Service.Close() error = %v", err)
		}
		if err := databaseStore.Close(); err != nil {
			t.Errorf("store.Close() error = %v", err)
		}
	}
}

func request(source string, inputs ...protocolmsql.StatementInput) protocolmsql.Request {
	return protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: source, Statements: inputs,
	}
}

func authorization(level protocolmsql.RiskLevel) protocolmsql.Authorization {
	return protocolmsql.Authorization{
		Version: protocolmsql.AuthorizationVersion, Actor: "agent:test",
		AuthorizedDatabases: []string{"work"}, DefaultLevel: level,
	}
}

func batchRequest(id, source string, count int) executor.BatchRequest {
	statements := make([]executor.StatementInput, count)
	for index := range statements {
		statements[index].Authorization = security.Authorization{
			Version: "memora.authorization/v2", Actor: "agent:test",
			AuthorizedDatabases: []string{"work"}, DefaultLevel: security.LevelWrite,
		}
	}
	if count > 1 {
		statements[1].Mutation.ExpectedSchemaVersion = 1
		statements[1].Mutation.MaxAffectedRows = 1
	}
	return executor.BatchRequest{RequestID: id, Source: source, Statements: statements}
}

type blockingCatalog struct {
	delegate executor.Catalog
	started  chan struct{}
	unblock  chan struct{}
	once     sync.Once
}

func newBlockingCatalog(delegate executor.Catalog) *blockingCatalog {
	return &blockingCatalog{delegate: delegate, started: make(chan struct{}), unblock: make(chan struct{})}
}

func (blocking *blockingCatalog) DescribeDatabase(ctx context.Context, name string) (catalog.Database, error) {
	return blocking.delegate.DescribeDatabase(ctx, name)
}

func (blocking *blockingCatalog) DescribeTable(ctx context.Context, database, table string) (catalog.Table, error) {
	blocking.once.Do(func() { close(blocking.started) })
	select {
	case <-ctx.Done():
		return catalog.Table{}, ctx.Err()
	case <-blocking.unblock:
		return blocking.delegate.DescribeTable(ctx, database, table)
	}
}

func (blocking *blockingCatalog) waitStarted(t *testing.T) {
	t.Helper()
	select {
	case <-blocking.started:
	case <-time.After(3 * time.Second):
		t.Fatal("MSQL call did not reach blocking catalog")
	}
}

func (blocking *blockingCatalog) release() {
	select {
	case <-blocking.unblock:
	default:
		close(blocking.unblock)
	}
}

// TestOpenSessionIsBounded pins the cap on the session registry. Sessions are
// created on demand by ID and removed only by an explicit close, so an
// unbounded registry is a memory leak any caller can trigger by inventing IDs.
func TestOpenSessionIsBounded(t *testing.T) {
	t.Parallel()

	service := msqlservice.New(context.Background(), msqlservice.Config{MaxSessions: 2})
	t.Cleanup(func() { _ = service.Close() })
	for _, id := range []string{"first", "second"} {
		if _, err := service.OpenSession(id); err != nil {
			t.Fatalf("OpenSession(%s) = %v", id, err)
		}
	}
	if _, err := service.OpenSession("third"); !errors.Is(err, msqlservice.ErrSessionLimit) {
		t.Fatalf("OpenSession over the cap = %v, want ErrSessionLimit", err)
	}
	// An already-open session is still reachable: the cap refuses new ones, it
	// does not make the service unusable.
	if _, err := service.OpenSession("first"); err != nil {
		t.Fatalf("OpenSession(existing) = %v", err)
	}
	if err := service.CloseSession("first"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.OpenSession("third"); err != nil {
		t.Fatalf("OpenSession after a close = %v", err)
	}
}
