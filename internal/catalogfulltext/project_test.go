package catalogfulltext

import (
	"errors"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
)

func TestProjectCoversDatabaseTableAndColumnSemanticSurface(t *testing.T) {
	database := catalogFixture()
	documents, err := Project([]catalog.Database{database})
	if err != nil || len(documents) != 3 {
		t.Fatalf("Project() = %#v, %v", documents, err)
	}
	index, err := fulltext.Build(documents)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		term, objectID, fieldID string
		kind                    fulltext.ObjectKind
	}{
		{"work", "db_work", "name", fulltext.KindDatabase},
		{"job", "db_work", "aliases", fulltext.KindDatabase},
		{"projects", "db_work", "scope", fulltext.KindDatabase},
		{"notes", "tbl_notes", "name", fulltext.KindTable},
		{"memo", "tbl_notes", "aliases", fulltext.KindTable},
		{"record", "tbl_notes", "row_semantics", fulltext.KindTable},
		{"title", "col_title", "name", fulltext.KindColumn},
		{"heading", "col_title", "aliases", fulltext.KindColumn},
		{"text", "col_title", "type", fulltext.KindColumn},
		{"primary", "col_title", "semantic_role", fulltext.KindColumn},
	}
	for _, test := range tests {
		postings := index.Postings(test.term)
		found := false
		for _, posting := range postings {
			found = found || (posting.Kind == test.kind && posting.ObjectID == test.objectID && posting.FieldID == test.fieldID)
		}
		if !found {
			t.Fatalf("Postings(%q) = %#v", test.term, postings)
		}
	}
}

func TestProjectSkipsBlankOptionalTextAndIsOrderDeterministic(t *testing.T) {
	database := catalogFixture()
	database.AntiScope = " \t"
	database.Tables[0].Scope = ""
	first, err := Project([]catalog.Database{database})
	if err != nil {
		t.Fatal(err)
	}
	database.Tables[0].Columns = append([]catalog.Column(nil), database.Tables[0].Columns...)
	second, err := Project([]catalog.Database{database})
	if err != nil || len(first) != len(second) {
		t.Fatalf("deterministic Project() = %#v / %#v, %v", first, second, err)
	}
	for index := range first {
		left, leftErr := fulltext.Compile(first[index])
		right, rightErr := fulltext.Compile(second[index])
		if leftErr != nil || rightErr != nil || left.Digest != right.Digest {
			t.Fatalf("document %d digest = %+v/%v %+v/%v", index, left, leftErr, right, rightErr)
		}
	}
}

func TestTombstoneAdvancesCatalogObjectRevision(t *testing.T) {
	object := fulltext.Object{Kind: fulltext.KindColumn, DatabaseID: "db_work", TableID: "tbl_notes",
		ObjectID: "col_title", Revision: 3, State: fulltext.StateLive, Digest: "sha256:old"}
	document, err := Tombstone(object)
	if err != nil || document.Revision != 4 || document.SchemaRevision != 4 ||
		document.State != fulltext.StateDeleted || len(document.Fields) != 0 {
		t.Fatalf("Tombstone() = %#v, %v", document, err)
	}
	if _, err := fulltext.Compile(document); err != nil {
		t.Fatal(err)
	}
}

func TestProjectRejectsIdentityRevisionAndKindDrift(t *testing.T) {
	tests := []func(*catalog.Database){
		func(database *catalog.Database) { database.SchemaVersion = 0 },
		func(database *catalog.Database) { database.Name = " " },
		func(database *catalog.Database) { database.Tables[0].DatabaseID = "db_other" },
		func(database *catalog.Database) { database.Tables[0].RowSemantics = "" },
		func(database *catalog.Database) { database.Tables[0].Columns[0].SchemaVersion = 0 },
		func(database *catalog.Database) { database.Tables[0].Columns[0].Type = "\t" },
	}
	for index, mutate := range tests {
		database := catalogFixture()
		mutate(&database)
		if _, err := Project([]catalog.Database{database}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d Project() error = %v", index, err)
		}
	}
	if _, err := Tombstone(fulltext.Object{Kind: fulltext.KindRow, ObjectID: "row_1", Revision: 1,
		State: fulltext.StateLive}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Row Tombstone() error = %v", err)
	}
}

func catalogFixture() catalog.Database {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	column := catalog.Column{ID: "col_title", Name: "title", Aliases: []string{"heading"}, Type: "TEXT",
		MaxCharacters: 200, Purpose: "Primary title", SemanticRole: "primary", SchemaVersion: 1,
		CreatedAt: now, UpdatedAt: now}
	table := catalog.Table{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{"memo"},
		Purpose: "Knowledge notes", Scope: "private", RowSemantics: "One record", SchemaVersion: 2,
		CreatedAt: now, UpdatedAt: now, Columns: []catalog.Column{column}}
	return catalog.Database{ID: "db_work", Name: "work", Aliases: []string{"job"}, Purpose: "Work knowledge",
		Scope: "Projects", AntiScope: "Public", SchemaVersion: 3, CreatedAt: now, UpdatedAt: now,
		Tables: []catalog.Table{table}}
}
