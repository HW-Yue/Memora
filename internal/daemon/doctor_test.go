package daemon

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestDoctorReportsAuthorityCountsAndRejectsCorruptCatalog(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work", Scope: "Projects",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Notes", RowSemantics: "One note",
		Columns: []catalog.ColumnDefinition{
			{Name: "title", Type: "TEXT", Purpose: "Title"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(databaseStore, dictionary, row.Options{})
	if _, err := rows.Insert(ctx, "work", "notes", map[string]any{
		"title": "healthy",
	}, row.WriteOptions{ExpectedSchemaVersion: 1}); err != nil {
		t.Fatal(err)
	}
	handler := newDatabaseHandler(ctx, dictionary, rows, databaseStore)
	report, err := handler.doctor(ctx)
	if err != nil || report.Status != "healthy" || report.Databases != 1 ||
		report.Rows != 1 || report.History != 1 || report.SnapshotHash == "" ||
		report.Security.Status != security.DoctorHealthy ||
		report.Security.ExternalModels != security.ExternalModelsDisabled {
		t.Fatalf("doctor report = %#v, %v", report, err)
	}

	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(ctx, "catalog", "snapshot", []byte(`{"version":"corrupt"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.doctor(ctx); err == nil {
		t.Fatal("doctor accepted a corrupt Catalog")
	}
}

func TestDoctorRejectsCorruptSecurityAudit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := nativekvstore.Open(filepath.Join(t.TempDir(), "security-doctor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	dictionary := catalog.New(databaseStore, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(databaseStore, dictionary, row.Options{}), databaseStore)
	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(ctx, security.AuditBucket, "corrupt", []byte(`{"version":"bad"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.doctor(ctx); err == nil {
		t.Fatal("doctor accepted corrupt security audit")
	}
}
