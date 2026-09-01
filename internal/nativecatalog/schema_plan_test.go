package nativecatalog

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/schemachangeplan"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestSchemaPlanPublishFaultLeavesCatalogAndChangeLogUnchanged(t *testing.T) {
	t.Parallel()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	column := catalog.Column{ID: "col_title", Name: "title", Aliases: []string{}, Type: "TEXT",
		MaxCharacters: 100, Purpose: "Title", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now}
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{},
		Purpose: "Notes", RowSemantics: "One note", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now,
		Columns: []catalog.Column{column}}
	database := catalog.Database{ID: "db_work", Name: "work", Aliases: []string{}, Purpose: "Work",
		Scope: "Private", SchemaVersion: 1, CreatedAt: now, UpdatedAt: now, Tables: []catalog.Table{table}}
	repository := New(file)
	if err := repository.Write(nil, []catalog.Database{database}); err != nil {
		t.Fatal(err)
	}
	plan, err := schemachangeplan.Build(context.Background(), nil, database, table, schemachangeplan.Proposal{
		Version: schemachangeplan.ProposalVersion, ID: "proposal_rename", Actor: "agent:test",
		SourceEventID: "event:rename", Reason: "clarify heading", ExpectedTableRevision: 1,
		Changes: []schemachangeplan.ChangeProposal{{ID: "rename", Action: schemachangeplan.ActionRename,
			ColumnID: column.ID, ExpectedRevision: 1, NewName: "heading"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority := &faultCatalogAuthority{database: database, publishErr: errors.New("publish fault")}
	service := NewService(repository, ServiceOptions{Clock: fixedClock{value: now.Add(time.Minute)}, Authority: authority})
	if _, _, err := service.ApplySchemaChangePlan(context.Background(), "work", "notes", plan, nil); err == nil {
		t.Fatal("ApplySchemaChangePlan(publish fault) unexpectedly succeeded")
	}
	stored, err := repository.Read()
	if err != nil || stored[0].Tables[0].Columns[0].Name != "title" || stored[0].SchemaVersion != 1 {
		t.Fatalf("failed publish changed Catalog = %#v, %v", stored, err)
	}
	changes, more, err := nativechange.New(file).ListAfter(0, 10)
	if err != nil || more || len(changes) != 0 {
		t.Fatalf("failed publish changed log = %#v, %v, %v", changes, more, err)
	}
}

func TestCatalogChangeEnvelopeRecordsDroppedColumn(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	beforeDatabase := catalog.Database{ID: "db_work", SchemaVersion: 1, Tables: []catalog.Table{{
		ID: "tbl_notes", DatabaseID: "db_work", SchemaVersion: 1, Columns: []catalog.Column{
			{ID: "col_keep", SchemaVersion: 1}, {ID: "col_drop", SchemaVersion: 3},
		},
	}}}
	afterDatabase := beforeDatabase
	afterDatabase.SchemaVersion = 2
	afterDatabase.Tables = append([]catalog.Table(nil), beforeDatabase.Tables...)
	afterDatabase.Tables[0].SchemaVersion = 2
	afterDatabase.Tables[0].Columns = []catalog.Column{beforeDatabase.Tables[0].Columns[0]}
	envelope, err := changeEnvelope(1, now, catalogRevisionMap([]catalog.Database{beforeDatabase}),
		[]catalog.Database{afterDatabase}, change.Metadata{Actor: "agent:test", Source: "event:drop", Reason: "drop field"})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, entry := range envelope.Entries {
		if entry.ObjectKind == change.ObjectColumn && entry.ObjectID == "col_drop" {
			found = entry.Operation == change.OperationDelete && entry.BeforeRevision == 3 && entry.AfterRevision == 4
		}
	}
	if !found {
		t.Fatalf("drop envelope = %#v", envelope.Entries)
	}
}

type faultCatalogAuthority struct {
	database   catalog.Database
	publishErr error
}

func (authority *faultCatalogAuthority) BeginWrite(context.Context) (func(), error) {
	return func() {}, nil
}
func (authority *faultCatalogAuthority) NextChangeSequence(context.Context) (uint64, error) {
	return 1, nil
}
func (authority *faultCatalogAuthority) SnapshotCatalog(context.Context) ([]catalog.Database, error) {
	return []catalog.Database{authority.database}, nil
}
func (authority *faultCatalogAuthority) ShowDatabases(context.Context) ([]catalog.Database, error) {
	return []catalog.Database{authority.database}, nil
}
func (authority *faultCatalogAuthority) DescribeDatabase(context.Context, string) (catalog.Database, error) {
	return authority.database, nil
}
func (authority *faultCatalogAuthority) ShowTables(context.Context, string) ([]catalog.Table, error) {
	return authority.database.Tables, nil
}
func (authority *faultCatalogAuthority) DescribeTable(context.Context, string, string) (catalog.Table, error) {
	return authority.database.Tables[0], nil
}
func (authority *faultCatalogAuthority) PublishCatalog(_ context.Context, _ []catalog.Database, _ func() error) error {
	return authority.publishErr
}
