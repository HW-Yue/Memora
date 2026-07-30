package nativerow

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRowRoundTripThroughNativeFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	catalogValue, rowValue := rowFixture()
	if err := nativecatalog.New(file).Write(catalogValue); err != nil {
		t.Fatalf("catalog Write() error = %v", err)
	}
	if err := New(file).Write(rowValue); err != nil {
		t.Fatalf("row Write() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := New(reopened).Read(rowValue.ID)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, rowValue) {
		t.Fatalf("Read() mismatch\n got: %#v\nwant: %#v", got, rowValue)
	}
}

func TestRowWriteRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*row.Row)
	}{
		{"text over budget", func(value *row.Row) { value.Values["col_text"] = "超过六个字符限制" }},
		{"bad relation", func(value *row.Row) { value.Values["col_relation"] = "not-a-row" }},
		{"unknown column", func(value *row.Row) { value.Values["col_unknown"] = "value" }},
		{"missing required", func(value *row.Row) { delete(value.Values, "col_text") }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "database.memora")
			file, err := nativestore.Create(path, nativestore.FileKindDatabase)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = file.Close() })
			catalogValue, rowValue := rowFixture()
			if err := nativecatalog.New(file).Write(catalogValue); err != nil {
				t.Fatal(err)
			}
			test.mutate(&rowValue)
			if err := New(file).Write(rowValue); err == nil {
				t.Fatal("Write() unexpectedly accepted invalid Row")
			}
		})
	}
}

func TestDecodeRowRejectsEveryTruncation(t *testing.T) {
	t.Parallel()

	catalogValue, rowValue := rowFixture()
	payload, err := encode(rowValue, catalogValue[0].Tables[0])
	if err != nil {
		t.Fatal(err)
	}
	for length := 0; length < len(payload); length++ {
		if _, err := decode(payload[:length]); err == nil {
			t.Fatalf("decode(payload[:%d]) unexpectedly succeeded", length)
		}
	}
}

func rowFixture() ([]catalog.Database, row.Row) {
	created := time.Date(2026, 7, 30, 11, 12, 13, 123456789, time.UTC)
	columns := []catalog.Column{
		{ID: "col_text", Name: "text", Type: "TEXT", MaxCharacters: 6, Purpose: "文本", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
		{ID: "col_integer", Name: "integer", Type: "INTEGER", Purpose: "整数", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
		{ID: "col_boolean", Name: "boolean", Type: "BOOLEAN", Purpose: "布尔", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
		{ID: "col_timestamp", Name: "timestamp", Type: "TIMESTAMP", Purpose: "时间", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
		{ID: "col_relation", Name: "relation", Type: "RELATION_ID", Purpose: "关系", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
		{ID: "col_optional", Name: "optional", Type: "TEXT", MaxCharacters: 6, Nullable: true, Purpose: "可空", SchemaVersion: 1, CreatedAt: created, UpdatedAt: created},
	}
	table := catalog.Table{ID: "tbl_memory", DatabaseID: "db_personal", Name: "memory", Purpose: "知识", RowSemantics: "一条知识", SchemaVersion: 4, CreatedAt: created, UpdatedAt: created, Columns: columns}
	database := catalog.Database{ID: "db_personal", Name: "个人库", Purpose: "个人知识", Scope: "私人", SchemaVersion: 4, CreatedAt: created, UpdatedAt: created, Tables: []catalog.Table{table}}
	value := row.Row{
		ID: "row_fixture", DatabaseID: database.ID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 1, State: row.StateLive,
		Values: map[string]any{
			"col_text": "记忆🧠", "col_integer": int64(-42), "col_boolean": true,
			"col_timestamp": created.Add(time.Hour), "col_relation": "row_parent", "col_optional": nil,
		},
		CreatedAt: created, UpdatedAt: created,
	}
	return []catalog.Database{database}, value
}
