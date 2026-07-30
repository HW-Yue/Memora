package nativecatalog

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestCatalogRoundTripThroughNativeFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	repository := New(file)
	want := catalogFixture()
	if err := repository.Write(want); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := New(reopened).Read()
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Read() mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestCatalogEncodingIsDeterministic(t *testing.T) {
	t.Parallel()

	fixture := catalogFixture()
	encoded := make([][]byte, 2)
	for index := range encoded {
		path := filepath.Join(t.TempDir(), "database.memora")
		file, err := nativestore.Create(path, nativestore.FileKindDatabase)
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
		if err := New(file).Write(fixture); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		encoded[index], err = os.ReadFile(path)
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
	}

	// The 32-byte file header contains a unique file ID. Record bytes after it
	// must depend only on the logical Catalog value.
	if !reflect.DeepEqual(encoded[0][32:], encoded[1][32:]) {
		t.Fatal("catalog record bytes are not deterministic")
	}
}

func TestDecodeDatabaseIgnoresUnknownField(t *testing.T) {
	t.Parallel()

	value := catalogFixture()[0]
	payload, err := encodeObject([]fieldValue{
		{1, value.ID}, {2, uint64(0)}, {3, value.Name}, {4, value.Aliases},
		{5, value.Purpose}, {6, value.Scope}, {7, value.AntiScope},
		{8, value.SchemaVersion}, {9, value.CreatedAt.UnixNano()},
		{10, value.UpdatedAt.UnixNano()}, {63, "future-field"},
	})
	if err != nil {
		t.Fatalf("encodeObject() error = %v", err)
	}
	record, err := decodeDatabase(payload)
	if err != nil {
		t.Fatalf("decodeDatabase() error = %v", err)
	}
	value.Tables = nil
	if !reflect.DeepEqual(record.value, value) {
		t.Fatalf("decoded database = %#v, want %#v", record.value, value)
	}
}

func TestCatalogWriteRejectsWrongParent(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	fixture := catalogFixture()
	fixture[0].Tables[0].DatabaseID = "db_other"
	if err := New(file).Write(fixture); err == nil {
		t.Fatal("Write() unexpectedly accepted a table with the wrong parent")
	}
}

func TestCatalogMutationAppendsAndReadsLatestSchemaVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	first := catalogFixture()
	if err := repository.Write(first); err != nil {
		t.Fatal(err)
	}
	second := catalogFixture()
	second[0].SchemaVersion++
	second[0].UpdatedAt = second[0].UpdatedAt.Add(time.Minute)
	second[0].Name = "Memora 知识库"
	second[0].Aliases = append(second[0].Aliases, first[0].Name)
	if err := repository.Write(second); err != nil {
		t.Fatalf("Write(updated) error = %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, err := New(reopened).Read()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Fatalf("Read() = %#v, want latest %#v", got, second)
	}
}

func catalogFixture() []catalog.Database {
	created := time.Date(2026, 7, 30, 10, 11, 12, 123456789, time.UTC)
	updated := created.Add(2 * time.Hour)
	return []catalog.Database{
		{
			ID:            "db_project_memora",
			Name:          "Memora 项目",
			Aliases:       []string{"memora", "记忆数据库"},
			Purpose:       "维护 Memora 的产品与实现知识",
			Scope:         "产品、架构和开发决策",
			AntiScope:     "其他项目",
			SchemaVersion: 3,
			CreatedAt:     created,
			UpdatedAt:     updated,
			Tables: []catalog.Table{
				{
					ID:            "tbl_decisions",
					DatabaseID:    "db_project_memora",
					Name:          "decisions",
					Aliases:       []string{"决策"},
					Purpose:       "保存稳定产品决策",
					Scope:         "已确认决策",
					AntiScope:     "发散想法",
					RowSemantics:  "一条可独立修改的决策",
					SchemaVersion: 2,
					CreatedAt:     created,
					UpdatedAt:     updated,
					Columns: []catalog.Column{
						{
							ID:            "col_title",
							Name:          "title",
							Aliases:       []string{"标题"},
							Type:          "TEXT",
							MaxCharacters: 120,
							Purpose:       "决策标题",
							SchemaVersion: 1,
							CreatedAt:     created,
							UpdatedAt:     updated,
						},
						{
							ID:            "col_active",
							Name:          "active",
							Type:          "BOOLEAN",
							Nullable:      true,
							Purpose:       "是否仍然有效",
							SchemaVersion: 1,
							CreatedAt:     created,
							UpdatedAt:     updated,
						},
					},
				},
			},
		},
	}
}
