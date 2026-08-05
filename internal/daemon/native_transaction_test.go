package daemon

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativemutation"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routetrace"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestNativeMSQLTransactionCommitsTwoRowsAtomically(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	server := ipc.NewServer(handler)
	t.Cleanup(func() {
		_ = server.Close()
		_ = handler.Close()
	})
	serverConnection, clientConnection := net.Pipe()
	done := make(chan struct{})
	go func() {
		server.ServeConnection(serverConnection)
		close(done)
	}()
	client := ipc.NewClient(clientConnection)

	var envelope result.Envelope
	err := client.Call(ctx, "msql.execute", executePayload{
		Source: "BEGIN; INSERT INTO work.notes (title) VALUES ('first'); INSERT INTO work.notes (title) VALUES ('second'); COMMIT",
		Statements: []executor.StatementInput{
			{},
			nativeInsertOptions(),
			nativeInsertOptions(),
			{},
		},
	}, &envelope)
	if err != nil {
		t.Fatalf("native transaction call: %v", err)
	}
	if !envelope.OK || len(envelope.Results) != 4 {
		t.Fatalf("native transaction envelope = %#v", envelope)
	}
	firstSequence := envelope.Results[1].CommitSequence
	secondSequence := envelope.Results[2].CommitSequence
	if firstSequence == nil || secondSequence == nil || *firstSequence == 0 || *firstSequence != *secondSequence {
		t.Fatalf("transaction commit sequences = %v, %v", firstSequence, secondSequence)
	}
	values, more, err := rows.ListPage(ctx, "work", "notes", 10)
	titles := map[any]bool{"first": false, "second": false}
	for _, value := range values {
		if _, ok := titles[value.Values["title"]]; ok {
			titles[value.Values["title"]] = true
		}
	}
	if err != nil || more || len(values) != 2 || !titles["first"] || !titles["second"] {
		t.Fatalf("committed native Rows = %#v, more=%v, err=%v", values, more, err)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestNativeMSQLTransactionRollsBackOnWriteFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	server := ipc.NewServer(handler)
	t.Cleanup(func() { _ = server.Close(); _ = handler.Close() })
	serverConnection, clientConnection := net.Pipe()
	done := make(chan struct{})
	go func() { server.ServeConnection(serverConnection); close(done) }()
	client := ipc.NewClient(clientConnection)
	var envelope result.Envelope
	err := client.Call(ctx, "msql.execute", executePayload{
		Source:     "BEGIN; INSERT INTO work.notes (title) VALUES ('kept out'); INSERT INTO work.notes (missing) VALUES ('must fail')",
		Statements: []executor.StatementInput{{}, nativeInsertOptions(), nativeInsertOptions()},
	}, &envelope)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.OK || len(envelope.Results) != 3 || envelope.Results[1].Status != result.StatusRolledBack || envelope.Results[2].Status != result.StatusFailed {
		t.Fatalf("failed native transaction envelope = %#v", envelope)
	}
	values, _, err := rows.ListPage(ctx, "work", "notes", 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("Rows after failed transaction = %#v, %v", values, err)
	}
	_ = client.Close()
	<-done
}

func TestNativeMSQLTransactionRollbackAndDisconnectLeaveNoRows(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	server := ipc.NewServer(handler)
	t.Cleanup(func() { _ = server.Close(); _ = handler.Close() })
	serverConnection, clientConnection := net.Pipe()
	done := make(chan struct{})
	go func() { server.ServeConnection(serverConnection); close(done) }()
	client := ipc.NewClient(clientConnection)
	var rolledBack result.Envelope
	if err := client.Call(ctx, "msql.execute", executePayload{
		Source:     "BEGIN; INSERT INTO work.notes (title) VALUES ('rolled back'); ROLLBACK",
		Statements: []executor.StatementInput{{}, nativeInsertOptions(), {}},
	}, &rolledBack); err != nil {
		t.Fatal(err)
	}
	if rolledBack.OK || rolledBack.Results[1].Status != result.StatusRolledBack || rolledBack.Results[2].Status != result.StatusSucceeded {
		t.Fatalf("rollback envelope = %#v", rolledBack)
	}
	values, _, err := rows.ListPage(ctx, "work", "notes", 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("Rows after explicit rollback = %#v, %v", values, err)
	}
	var pending result.Envelope
	if err := client.Call(ctx, "msql.execute", executePayload{
		Source:     "BEGIN; INSERT INTO work.notes (title) VALUES ('disconnect')",
		Statements: []executor.StatementInput{{}, nativeInsertOptions()},
	}, &pending); err != nil {
		t.Fatal(err)
	}
	if !pending.OK || pending.Results[1].Status != result.StatusSucceeded {
		t.Fatalf("pending envelope = %#v", pending)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	values, _, err = rows.ListPage(ctx, "work", "notes", 10)
	if err != nil || len(values) != 0 {
		t.Fatalf("Rows after disconnect rollback = %#v, %v", values, err)
	}
}

func TestNativeDaemonMultiStatementTransactionSurvivesReopen(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready

	create := func(source string) {
		envelope, err := Execute(context.Background(), dataDir, source, nil)
		if err != nil || !envelope.OK {
			t.Fatalf("setup %q = %#v, %v", source, envelope, err)
		}
	}
	create("CREATE DATABASE work PURPOSE 'Work' SCOPE 'Knowledge'")
	create("CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One claim' (title TEXT(200) NOT NULL PURPOSE 'Claim')")
	envelope, err := Execute(context.Background(), dataDir,
		"BEGIN; INSERT INTO work.notes (title) VALUES ('page-first'); INSERT INTO work.notes (title) VALUES ('page-second'); COMMIT",
		[]executor.StatementInput{{}, nativeInsertOptions(), nativeInsertOptions(), {}})
	if err != nil || !envelope.OK || len(envelope.Results) != 4 {
		t.Fatalf("page authority transaction = %#v, %v", envelope, err)
	}
	if envelope.Results[1].CommitSequence == nil || envelope.Results[2].CommitSequence == nil ||
		*envelope.Results[1].CommitSequence != *envelope.Results[2].CommitSequence {
		t.Fatalf("page authority sequences = %#v", envelope.Results)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	ready = make(chan State, 1)
	done = make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	selected, err := Execute(context.Background(), dataDir, "SELECT title FROM work.notes LIMIT 10", nil)
	if err != nil || !selected.OK || len(selected.Results) != 1 || len(selected.Results[0].Rows) != 2 {
		t.Fatalf("reopened page authority rows = %#v, %v", selected, err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestNativeMSQLTransactionsSerializeConcurrentConnections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	server := ipc.NewServer(handler)
	t.Cleanup(func() { _ = server.Close(); _ = handler.Close() })
	const writers = 8
	errorsCh := make(chan error, writers)
	var wait sync.WaitGroup
	for index := 0; index < writers; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			serverConnection, clientConnection := net.Pipe()
			done := make(chan struct{})
			go func() { server.ServeConnection(serverConnection); close(done) }()
			client := ipc.NewClient(clientConnection)
			var envelope result.Envelope
			err := client.Call(ctx, "msql.execute", executePayload{
				Source: "BEGIN; INSERT INTO work.notes (title) VALUES (:title); INSERT INTO work.notes (title) VALUES (:title2); COMMIT",
				Statements: []executor.StatementInput{{},
					{Parameters: executor.Parameters{Named: map[string]any{"title": fmt.Sprintf("writer-%d-a", index)}}, Mutation: nativeInsertOptions().Mutation},
					{Parameters: executor.Parameters{Named: map[string]any{"title2": fmt.Sprintf("writer-%d-b", index)}}, Mutation: nativeInsertOptions().Mutation}, {},
				},
			}, &envelope)
			if err != nil || !envelope.OK {
				errorsCh <- fmt.Errorf("writer %d: envelope=%#v err=%v", index, envelope, err)
			}
			_ = client.Close()
			<-done
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	values, _, err := rows.ListPage(ctx, "work", "notes", 100)
	if err != nil || len(values) != writers*2 {
		t.Fatalf("concurrent native Rows = %d, %v", len(values), err)
	}
}

func TestNativeMSQLTransactionCommitsRelationWithRowsInOneEnvelope(t *testing.T) {
	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	server := ipc.NewServer(handler)
	t.Cleanup(func() { _ = server.Close(); _ = handler.Close() })
	first, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "source"}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	second, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "target"}, row.WriteOptions{ExpectedSchemaVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	serverConnection, clientConnection := net.Pipe()
	done := make(chan struct{})
	go func() { server.ServeConnection(serverConnection); close(done) }()
	client := ipc.NewClient(clientConnection)
	var envelope result.Envelope
	err = client.Call(ctx, "msql.execute", executePayload{
		Source: "BEGIN; RELATE work.notes ROW :source TO work.notes ROW :target TYPE 'supports'; COMMIT",
		Statements: []executor.StatementInput{{}, {Parameters: executor.Parameters{Named: map[string]any{
			"source": first.ID, "target": second.ID,
		}}, Mutation: nativeInsertOptions().Mutation}, {}},
	}, &envelope)
	if err != nil || !envelope.OK || len(envelope.Results) != 3 || envelope.Results[1].AffectedRows != 1 {
		t.Fatalf("native relation transaction = %#v, %v", envelope, err)
	}
	relations, err := rows.ListOutgoingRelations(ctx, row.RelationEndpoint{Database: "work", Table: "notes", RowID: first.ID})
	if err != nil || len(relations) != 1 || relations[0].Target.RowID != second.ID {
		t.Fatalf("native relation after commit = %#v, %v", relations, err)
	}
	relationID := relations[0].ID
	var deleted result.Envelope
	err = client.Call(ctx, "msql.execute", executePayload{
		Source: "BEGIN; UNRELATE :relation; COMMIT",
		Statements: []executor.StatementInput{{}, {Parameters: executor.Parameters{Named: map[string]any{
			"relation": relationID,
		}}, Mutation: executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1}}, {}},
	}, &deleted)
	if err != nil || !deleted.OK || len(deleted.Results) != 3 || deleted.Results[1].Revision == nil || *deleted.Results[1].Revision != 2 {
		t.Fatalf("native UNRELATE transaction = %#v, %v", deleted, err)
	}
	relations, err = rows.ListOutgoingRelations(ctx, row.RelationEndpoint{Database: "work", Table: "notes", RowID: first.ID})
	if err != nil || len(relations) != 0 {
		t.Fatalf("native relation after UNRELATE = %#v, %v", relations, err)
	}
	_ = client.Close()
	<-done
}

func TestNativeMSQLTransactionMatchesCommitRollbackReferenceModel(t *testing.T) {
	ctx := context.Background()
	handler, rows := nativeTransactionFixture(t, ctx)
	t.Cleanup(func() { _ = handler.Close() })
	session, err := handler.msql.OpenSession("f205-reference-model")
	if err != nil {
		t.Fatal(err)
	}
	defer handler.msql.CloseSession("f205-reference-model")
	steps := []struct {
		source string
		inputs []executor.StatementInput
		want   int
	}{
		{"BEGIN; INSERT INTO work.notes (title) VALUES ('model-a'); INSERT INTO work.notes (title) VALUES ('model-b'); COMMIT", []executor.StatementInput{{}, nativeInsertOptions(), nativeInsertOptions(), {}}, 2},
		{"BEGIN; INSERT INTO work.notes (title) VALUES ('discarded'); ROLLBACK", []executor.StatementInput{{}, nativeInsertOptions(), {}}, 2},
		{"BEGIN; INSERT INTO work.notes (title) VALUES ('discarded'); INSERT INTO work.notes (missing) VALUES ('failure'); ROLLBACK", []executor.StatementInput{{}, nativeInsertOptions(), nativeInsertOptions(), {}}, 2},
		{"BEGIN; INSERT INTO work.notes (title) VALUES ('model-c'); COMMIT", []executor.StatementInput{{}, nativeInsertOptions(), {}}, 3},
	}
	for index, step := range steps {
		envelope := session.ExecuteBatch(ctx, executor.BatchRequest{RequestID: fmt.Sprintf("reference-%d", index), Source: step.source, Statements: step.inputs})
		if index == 0 || index == 3 {
			if !envelope.OK {
				t.Fatalf("reference step %d = %#v", index, envelope)
			}
		}
		values, _, listErr := rows.ListPage(ctx, "work", "notes", 20)
		if listErr != nil || len(values) != step.want {
			t.Fatalf("reference step %d rows=%d want=%d err=%v", index, len(values), step.want, listErr)
		}
	}
}

func nativeTransactionFixture(t *testing.T, ctx context.Context) (*databaseHandler, *nativemutation.Service) {
	t.Helper()
	directory := t.TempDir()
	file, err := nativestore.Create(filepath.Join(directory, "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Reviewed knowledge",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One semantic claim",
		Columns: []catalog.ColumnDefinition{{Name: "title", Type: "TEXT(200)", Purpose: "Claim"}},
	}); err != nil {
		t.Fatal(err)
	}
	rowRepository := nativerow.New(file)
	routeRepository := nativerouter.New(file)
	base := nativerow.NewService(rowRepository, dictionary, nativerow.ServiceOptions{})
	rows := nativemutation.NewService(
		base, dictionary, rowRepository, routeRepository,
		nativemutation.New(file, rowRepository, routeRepository),
	)
	auxiliary, err := nativekvstore.Open(filepath.Join(directory, "auxiliary.memora"))
	if err != nil {
		t.Fatal(err)
	}
	traces := routetrace.New(auxiliary)
	handler := newNativeDatabaseHandler(
		ctx, dictionary, rows, nil, nil, auxiliary,
		security.New(auxiliary, security.Options{}), traces,
		func(context.Context) ([]byte, error) { return nil, nil },
		func(callContext context.Context) (executor.ExplicitTransaction, error) {
			return rows.BeginExplicitTransaction(callContext)
		},
	)
	t.Cleanup(func() {
		_ = auxiliary.Close()
		_ = file.Close()
	})
	return handler, rows
}

func nativeInsertOptions() executor.StatementInput {
	return executor.StatementInput{Mutation: executor.MutationOptions{
		ExpectedSchemaVersion: 1,
		MaxAffectedRows:       1,
		Actor:                 "agent:f205",
		Source:                "fixture:f205",
		Reason:                "native atomic transaction",
	}}
}
