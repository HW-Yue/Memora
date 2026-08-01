package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
)

func TestAcquireEnforcesSingleDaemon(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	first, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	defer func() { _ = first.Close() }()

	if _, err := Acquire(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Acquire() error = %v, want %v", err, ErrAlreadyRunning)
	}
	state, err := Inspect(dataDir)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if !state.Running || state.PID != os.Getpid() {
		t.Fatalf("Inspect() = %#v, want running PID %d", state, os.Getpid())
	}
}

func TestAcquireMaintenanceExcludesDaemonWithoutPublishingPID(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	lease, err := AcquireMaintenance(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lease.Close() }()
	if _, err := Acquire(dataDir); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("Acquire() during maintenance = %v", err)
	}
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("maintenance lease published a PID file: %v", err)
	}
}

func TestInspectCleansStalePID(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	paths, err := RuntimePaths(dataDir)
	if err != nil {
		t.Fatalf("RuntimePaths() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.PIDFile), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(paths.PIDFile, []byte("999999\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	state, err := Inspect(dataDir)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if state.Running {
		t.Fatalf("Inspect() = %#v, want stopped", state)
	}
	if _, err := os.Stat(paths.PIDFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale PID file remains: %v", err)
	}
}

func TestRunReleasesLeaseAfterCancellation(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, dataDir, ready)
	}()
	state := <-ready
	if !state.Running {
		t.Fatalf("ready state = %#v, want running", state)
	}
	databaseDirectory := filepath.Join(dataDir, "databases")
	if _, err := os.Stat(filepath.Join(databaseDirectory, "page-index-v1")); err != nil {
		t.Fatalf("Page Store generation was not activated before ready: %v", err)
	}
	if _, err := os.Stat(filepath.Join(databaseDirectory, "page-authority-v1.json")); err != nil {
		t.Fatalf("Page Store authority marker was not durable before ready: %v", err)
	}
	envelope, err := Execute(context.Background(), dataDir, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", nil)
	if err != nil || !envelope.OK {
		t.Fatalf("native daemon CREATE DATABASE = %#v, %v", envelope, err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "databases", "database.memora")); err != nil {
		t.Fatalf("native authority was not created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "databases", "prototype.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("new daemon created legacy authority: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	lease, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire() after cancellation error = %v", err)
	}
	if err := lease.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestInspectRejectsCorruptPIDWhileLocked(t *testing.T) {
	t.Parallel()

	dataDir := filepath.Join(t.TempDir(), "instance")
	lease, err := Acquire(dataDir)
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	defer func() { _ = lease.Close() }()
	if err := os.WriteFile(lease.paths.PIDFile, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Inspect(dataDir); !errors.Is(err, ErrCorruptPID) {
		t.Fatalf("Inspect() error = %v, want %v", err, ErrCorruptPID)
	}
}

func TestRunPageAuthorityPublishesRowsAndReopens(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan State, 1)
	done := make(chan error, 1)
	go func() { done <- Run(ctx, dataDir, ready) }()
	<-ready
	execute := func(source string, statements []executor.StatementInput) executorResult {
		envelope, err := Execute(context.Background(), dataDir, source, statements)
		if err != nil || !envelope.OK || len(envelope.Results) != 1 {
			t.Fatalf("Execute(%q) = %#v, %v", source, envelope, err)
		}
		return executorResult{
			rows: envelope.Results[0].Rows, rowDetail: envelope.Results[0].RowDetail,
			page: envelope.Results[0].Page, nextCursor: envelope.Results[0].NextCursor,
		}
	}
	execute("CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'", nil)
	execute("CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(40) NOT NULL PURPOSE 'Title' ROLE title)", nil)
	inserted := execute(
		"INSERT INTO work.notes (title) VALUES ('Page authority')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1,
		}}},
	)
	rowID, ok := inserted.rows[0]["row_id"].(string)
	if !ok || rowID == "" {
		t.Fatalf("INSERT rows = %#v", inserted.rows)
	}
	selected := execute(
		"SELECT title FROM work.notes WHERE row_id = :row LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	)
	if len(selected.rows) != 1 || selected.rows[0]["title"] != "Page authority" ||
		selected.rowDetail == nil || selected.rowDetail.Display.TitleColumn != "title" {
		t.Fatalf("live Page SELECT = %#v", selected.rows)
	}
	execute(
		"UPDATE work.notes SET title = 'Page authority revised' WHERE row_id = :row",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"row": rowID}},
			Mutation:   executor.MutationOptions{ExpectedSchemaVersion: 1, ExpectedRevision: 1, MaxAffectedRows: 1},
		}},
	)
	historyPage := execute(
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	)
	if historyPage.page == nil || historyPage.nextCursor == "" || len(historyPage.rows) != 1 ||
		historyPage.rows[0]["values"] != nil {
		t.Fatalf("daemon History page = %#v", historyPage)
	}
	historyTail := execute(
		"SHOW HISTORY FROM work.notes FOR ROW :row CURSOR :cursor LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{
			"row": rowID, "cursor": historyPage.nextCursor,
		}}}},
	)
	if historyTail.page == nil || historyTail.nextCursor != "" || len(historyTail.rows) != 1 ||
		historyTail.rows[0]["operation"] != "INSERT" {
		t.Fatalf("daemon History tail = %#v", historyTail)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	reopenContext, stopReopened := context.WithCancel(context.Background())
	reopenedReady := make(chan State, 1)
	reopenedDone := make(chan error, 1)
	go func() { reopenedDone <- Run(reopenContext, dataDir, reopenedReady) }()
	<-reopenedReady
	selected = execute(
		"SELECT title FROM work.notes WHERE row_id = :row LIMIT 1",
		[]executor.StatementInput{{Parameters: executor.Parameters{Named: map[string]any{"row": rowID}}}},
	)
	if len(selected.rows) != 1 || selected.rows[0]["title"] != "Page authority revised" ||
		selected.rowDetail == nil || selected.rowDetail.Display.TitleColumn != "title" {
		t.Fatalf("reopened Page SELECT = %#v", selected.rows)
	}
	stopReopened()
	if err := <-reopenedDone; err != nil {
		t.Fatal(err)
	}
}

type executorResult struct {
	rows       []result.Row
	rowDetail  *result.RowDetail
	page       *result.ListPage
	nextCursor string
}
