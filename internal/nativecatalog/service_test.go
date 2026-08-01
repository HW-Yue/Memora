package nativecatalog

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestNativeCatalogServiceCreatesAndDiscoversAfterReopen(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	clock := fixedClock{value: time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)}
	service := NewService(New(file), ServiceOptions{IDs: &sequenceIDs{values: []string{"database", "table", "column"}}, Clock: clock})
	if _, err := service.CreateDatabase(ctx, catalog.DatabaseDefinition{Name: "work", Purpose: "工作知识", Scope: "项目"}); err != nil {
		t.Fatalf("CreateDatabase() error = %v", err)
	}
	if _, err := service.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "项目笔记", RowSemantics: "一条完整笔记",
		Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT(3000)", Purpose: "正文", SemanticRole: "summary"}},
	}); err != nil {
		t.Fatalf("CreateTable() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	service = NewService(New(reopened), ServiceOptions{})
	databases, err := service.ShowDatabases(ctx)
	if err != nil || len(databases) != 1 || databases[0].Name != "work" {
		t.Fatalf("ShowDatabases() = %#v, %v", databases, err)
	}
	tables, err := service.ShowTables(ctx, "work")
	if err != nil || len(tables) != 1 || tables[0].Name != "notes" {
		t.Fatalf("ShowTables() = %#v, %v", tables, err)
	}
	table, err := service.DescribeTable(ctx, "work", "notes")
	if err != nil || len(table.Columns) != 1 || table.Columns[0].MaxCharacters != 3000 ||
		table.Columns[0].SemanticRole != "summary" {
		t.Fatalf("DescribeTable() = %#v, %v", table, err)
	}
}

type sequenceIDs struct {
	values []string
	index  int
}

func (source *sequenceIDs) Next() (string, error) {
	value := source.values[source.index]
	source.index++
	return value, nil
}

type fixedClock struct{ value time.Time }

func (clock fixedClock) Now() time.Time { return clock.value }
