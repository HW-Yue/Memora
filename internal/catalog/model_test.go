package catalog_test

import (
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
)

func archived() *time.Time {
	stamp := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	return &stamp
}

func stableCode(t *testing.T, err error) string {
	t.Helper()
	var coded interface{ StableCode() string }
	if !errors.As(err, &coded) {
		t.Fatalf("error %v carries no stable code", err)
	}
	return coded.StableCode()
}

func TestArchivedAnswersForEachLevel(t *testing.T) {
	if (catalog.Database{}).Archived() || (catalog.Table{}).Archived() || (catalog.Column{}).Archived() {
		t.Fatal("a missing archived stamp must read as live at every level")
	}
	if !(catalog.Database{ArchivedAt: archived()}).Archived() {
		t.Fatal("Database.Archived must report its own stamp")
	}
	if !(catalog.Table{ArchivedAt: archived()}).Archived() {
		t.Fatal("Table.Archived must report its own stamp")
	}
	if !(catalog.Column{ArchivedAt: archived()}).Archived() {
		t.Fatal("Column.Archived must report its own stamp")
	}
}

// A Table's visibility is the conjunction of its own state and its Database's,
// so the two answers have to stay independent: archiving a Database must not
// stamp its descendants.
func TestArchivingADatabaseLeavesItsTablesAlone(t *testing.T) {
	database := catalog.Database{
		Name: "work", ArchivedAt: archived(),
		Tables: []catalog.Table{{Name: "notes", Columns: []catalog.Column{{Name: "title"}}}},
	}
	if !database.Archived() {
		t.Fatal("the database must read as archived")
	}
	if database.Tables[0].Archived() || database.Tables[0].Columns[0].Archived() {
		t.Fatal("archiving a Database must not stamp what it contains")
	}
}

func TestLiveColumnsDropsArchivedAndKeepsOrder(t *testing.T) {
	columns := []catalog.Column{
		{ID: "col_1", Name: "title"},
		{ID: "col_2", Name: "old_title", ArchivedAt: archived()},
		{ID: "col_3", Name: "body"},
	}
	live := catalog.LiveColumns(columns)
	if len(live) != 2 || live[0].ID != "col_1" || live[1].ID != "col_3" {
		t.Fatalf("live columns = %+v", live)
	}
	if len(columns) != 3 {
		t.Fatal("LiveColumns must not shrink its input")
	}
}

// Column.Validate is the only place a value meets a logical type before it is
// stored, so the mapping onto stable codes is part of the write contract.
func TestColumnValidateMapsFailuresToStableCodes(t *testing.T) {
	title := catalog.Column{Name: "title", Type: "TEXT", MaxCharacters: 5}
	value, err := title.Validate("abcde")
	if err != nil || value != "abcde" {
		t.Fatalf("a value at the ceiling must be accepted: %v, %v", value, err)
	}
	if _, err := title.Validate("abcdef"); stableCode(t, err) != string(result.CodeValueTooLong) {
		t.Fatalf("over the ceiling: %v", err)
	}
	if _, err := title.Validate(nil); stableCode(t, err) != string(result.CodeConstraint) {
		t.Fatalf("a NULL in a NOT NULL column: %v", err)
	}
	if _, err := title.Validate(7); stableCode(t, err) != string(result.CodeConstraint) {
		t.Fatalf("a non-TEXT value: %v", err)
	}

	nullable := catalog.Column{Name: "note", Type: "TEXT", MaxCharacters: 10, Nullable: true}
	if value, err := nullable.Validate(nil); err != nil || value != nil {
		t.Fatalf("a NULL in a nullable column must stay NULL: %v, %v", value, err)
	}

	unknown := catalog.Column{Name: "broken", Type: "BLOB"}
	if _, err := unknown.Validate("anything"); stableCode(t, err) != string(result.CodeValidation) {
		t.Fatalf("an unknown logical type: %v", err)
	}
}
