package rowfulltext

import (
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/row"
)

func TestProjectCoversEveryLogicalValueKindByColumnID(t *testing.T) {
	t.Parallel()

	table := projectionTable()
	value := projectionRow()
	document, err := Project(table, value)
	if err != nil {
		t.Fatal(err)
	}
	index, err := fulltext.Build([]fulltext.Document{document})
	if err != nil {
		t.Fatal(err)
	}
	terms := map[string]string{
		"semantic": "col_text", "42": "col_integer", "true": "col_boolean",
		"2026": "col_timestamp", "target": "col_relation",
	}
	for term, fieldID := range terms {
		postings := index.Postings(term)
		if len(postings) != 1 || postings[0].ObjectID != value.ID || postings[0].FieldID != fieldID {
			t.Fatalf("Postings(%q) = %#v", term, postings)
		}
	}
	if document.SchemaRevision != value.SchemaVersion || document.Revision != value.Revision ||
		document.DatabaseID != value.DatabaseID || document.TableID != value.TableID {
		t.Fatalf("projected identity = %#v", document)
	}
}

func TestProjectSkipsNullAndAllowsEmptyLiveRow(t *testing.T) {
	t.Parallel()

	table := projectionTable()
	value := projectionRow()
	value.Values = map[string]any{"col_nullable": nil}
	document, err := Project(table, value)
	if err != nil || len(document.Fields) != 0 {
		t.Fatalf("Project(empty live) = %#v, %v", document, err)
	}
	index, err := fulltext.Build([]fulltext.Document{document})
	if err != nil || len(index.AllPostings()) != 0 {
		t.Fatalf("Build(empty live) = %#v, %v", index, err)
	}
}

func TestProjectInactiveRowCreatesTombstone(t *testing.T) {
	t.Parallel()

	for _, state := range []row.State{row.StateDeleted, row.StateSuperseded} {
		value := projectionRow()
		value.State = state
		document, err := Project(projectionTable(), value)
		if err != nil || len(document.Fields) != 0 || document.State != fulltext.State(state) {
			t.Fatalf("Project(%s) = %#v, %v", state, document, err)
		}
	}
}

func TestProjectRejectsScopeSchemaColumnAndTypeDrift(t *testing.T) {
	t.Parallel()

	tests := []func(catalog.Table, row.Row) (catalog.Table, row.Row){
		func(table catalog.Table, value row.Row) (catalog.Table, row.Row) {
			value.TableID = "other"
			return table, value
		},
		func(table catalog.Table, value row.Row) (catalog.Table, row.Row) {
			value.SchemaVersion++
			return table, value
		},
		func(table catalog.Table, value row.Row) (catalog.Table, row.Row) {
			value.Values["unknown"] = "value"
			return table, value
		},
		func(table catalog.Table, value row.Row) (catalog.Table, row.Row) {
			value.Values["col_integer"] = "42"
			return table, value
		},
	}
	for number, mutate := range tests {
		table, value := mutate(projectionTable(), projectionRow())
		if _, err := Project(table, value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d Project() error = %v", number, err)
		}
	}
}

func projectionTable() catalog.Table {
	return catalog.Table{
		ID: "tbl_notes", DatabaseID: "db_work", SchemaVersion: 3,
		Columns: []catalog.Column{
			{ID: "col_text", Type: "TEXT", MaxCharacters: 1200, SchemaVersion: 3},
			{ID: "col_integer", Type: "INTEGER", SchemaVersion: 3},
			{ID: "col_boolean", Type: "BOOLEAN", SchemaVersion: 3},
			{ID: "col_timestamp", Type: "TIMESTAMP", SchemaVersion: 3},
			{ID: "col_relation", Type: "RELATION_ID", SchemaVersion: 3},
			{ID: "col_nullable", Type: "TEXT", MaxCharacters: 1200, Nullable: true, SchemaVersion: 3},
		},
	}
}

func projectionRow() row.Row {
	return row.Row{
		ID: "row_1", DatabaseID: "db_work", TableID: "tbl_notes", SchemaVersion: 3,
		Revision: 7, State: row.StateLive,
		Values: map[string]any{
			"col_text": "Semantic memory", "col_integer": int64(42), "col_boolean": true,
			"col_timestamp": time.Date(2026, 8, 3, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60)),
			"col_relation":  "row_target-1", "col_nullable": nil,
		},
	}
}
