package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestNativeDaemonRebuildsLexicalIndexThroughMSQLAndReopens(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	runContext, stop := context.WithCancel(context.Background())
	ready, done := make(chan State, 1), make(chan error, 1)
	go func() { done <- Run(runContext, dataDir, ready) }()
	<-ready
	executeRebuildMSQL(t, dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", nil)
	executeRebuildMSQL(t, dataDir,
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(80) NOT NULL PURPOSE 'Title')", nil,
	)
	executeRebuildMSQL(t, dataDir, "INSERT INTO work.notes (title) VALUES ('rebuild survives')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{ExpectedSchemaVersion: 1, MaxAffectedRows: 1}}},
	)
	first := executeRebuildMSQL(t, dataDir, "REBUILD LEXICAL INDEX", nil)
	row := first.Results[0].Rows[0]
	if fmt.Sprint(row["epoch"]) != "1" || row["verified"] != true || row["parity"] != true ||
		row["snapshot_sha256"] == "" {
		t.Fatalf("first rebuild = %#v", first.Results[0])
	}
	firstSnapshot := row["snapshot_sha256"]
	stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	reopenContext, stopReopened := context.WithCancel(context.Background())
	reopenedReady, reopenedDone := make(chan State, 1), make(chan error, 1)
	go func() { reopenedDone <- Run(reopenContext, dataDir, reopenedReady) }()
	<-reopenedReady
	second := executeRebuildMSQL(t, dataDir, "REBUILD LEXICAL INDEX", nil)
	row = second.Results[0].Rows[0]
	if fmt.Sprint(row["epoch"]) != "2" || row["snapshot_sha256"] != firstSnapshot ||
		row["verified"] != true || row["parity"] != true {
		t.Fatalf("reopened rebuild = %#v", second.Results[0])
	}
	stopReopened()
	if err := <-reopenedDone; err != nil {
		t.Fatal(err)
	}
}

func executeRebuildMSQL(
	t *testing.T, dataDir, source string, statements []executor.StatementInput,
) result.Envelope {
	t.Helper()
	envelope, err := Execute(context.Background(), dataDir, source, statements)
	if err != nil || !envelope.OK || len(envelope.Results) != 1 {
		t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
	}
	return envelope
}
