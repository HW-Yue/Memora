package fulltext

import (
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

type referenceDocument struct {
	revision uint64
	state    State
	terms    []string
}

func TestReferenceIndexCoversEveryLiveRowAndSemanticField(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, 8, 3, 10, 11, 12, 0, time.FixedZone("CST", 8*60*60))
	documents := []Document{
		document(KindDatabase, "db_work", "", "db_work", 4,
			field("database.name", TextValue("Workspace"))),
		document(KindTable, "db_work", "tbl_notes", "tbl_notes", 3,
			field("table.name", TextValue("Notebook"))),
		document(KindColumn, "db_work", "tbl_notes", "col_title", 8,
			field("column.name", TextValue("Headline"))),
		document(KindRoute, "db_work", "tbl_notes", "route_architecture", 7,
			field("route.purpose", TextValue("Architecture decisions"))),
		document(KindRow, "db_work", "tbl_notes", "row_decision", 12,
			field("col_title", TextValue("Deterministic storage")),
			field("col_number", IntegerValue(42)),
			field("col_active", BooleanValue(false)),
			field("col_at", TimestampValue(stamp)),
			field("col_relation", RelationIDValue("row_target-1"))),
	}
	index, err := Build(documents)
	if err != nil {
		t.Fatal(err)
	}

	assertSinglePosting(t, index.Postings("workspace"), KindDatabase, "db_work", "database.name", 1)
	assertSinglePosting(t, index.Postings("notebook"), KindTable, "tbl_notes", "table.name", 1)
	assertSinglePosting(t, index.Postings("headline"), KindColumn, "col_title", "column.name", 1)
	assertSinglePosting(t, index.Postings("architecture"), KindRoute, "route_architecture", "route.purpose", 1)
	assertSinglePosting(t, index.Postings("deterministic"), KindRow, "row_decision", "col_title", 1)
	assertSinglePosting(t, index.Postings("42"), KindRow, "row_decision", "col_number", 1)
	assertSinglePosting(t, index.Postings("false"), KindRow, "row_decision", "col_active", 1)
	assertSinglePosting(t, index.Postings("2026"), KindRow, "row_decision", "col_at", 1)
	assertSinglePosting(t, index.Postings("target"), KindRow, "row_decision", "col_relation", 1)
}

func TestReferenceIndexReplacementRemovesStaleTerms(t *testing.T) {
	t.Parallel()

	index := New()
	first := document(KindRow, "db_work", "tbl_notes", "row_1", 1,
		field("col_title", TextValue("Old old title")))
	receipt, err := index.Replace(first)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replay || receipt.Added != 2 || receipt.Removed != 0 || !strings.HasPrefix(receipt.Digest, "sha256:") {
		t.Fatalf("first receipt = %#v", receipt)
	}
	assertSinglePosting(t, index.Postings("old"), KindRow, "row_1", "col_title", 2)

	second := document(KindRow, "db_work", "tbl_notes", "row_1", 2,
		field("col_title", TextValue("New title")))
	receipt, err = index.Replace(second)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Replay || receipt.Added != 1 || receipt.Removed != 1 {
		t.Fatalf("replacement receipt = %#v", receipt)
	}
	if got := index.Postings("old"); len(got) != 0 {
		t.Fatalf("stale postings = %#v", got)
	}
	assertSinglePosting(t, index.Postings("new"), KindRow, "row_1", "col_title", 1)

	receipt, err = index.Replace(second)
	if err != nil || !receipt.Replay || receipt.Added != 0 || receipt.Removed != 0 {
		t.Fatalf("idempotent replacement receipt=%#v error=%v", receipt, err)
	}
}

func TestReferenceIndexDeleteAndSupersedeRemovePostings(t *testing.T) {
	t.Parallel()

	for _, state := range []State{StateDeleted, StateSuperseded} {
		t.Run(string(state), func(t *testing.T) {
			index := New()
			if _, err := index.Replace(document(KindRow, "db_work", "tbl_notes", "row_1", 1,
				field("col_title", TextValue("Retired fact")))); err != nil {
				t.Fatal(err)
			}
			tombstone := Document{
				Version: DocumentVersion, Kind: KindRow, DatabaseID: "db_work", TableID: "tbl_notes",
				ObjectID: "row_1", Revision: 2, SchemaRevision: 1, State: state, Complete: true,
			}
			receipt, err := index.Replace(tombstone)
			if err != nil {
				t.Fatal(err)
			}
			if receipt.Removed != 2 || receipt.Added != 0 || len(index.AllPostings()) != 0 {
				t.Fatalf("tombstone receipt=%#v postings=%#v", receipt, index.AllPostings())
			}
		})
	}
}

func TestReferenceIndexTokenizesChineseAndLatinDeterministically(t *testing.T) {
	t.Parallel()

	doc := document(KindRoute, "db_work", "tbl_notes", "route_1", 1,
		field("route.name", TextValue("Go GO,数据库数据库")))
	left, err := Build([]Document{doc})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Build([]Document{doc})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left.AllPostings(), right.AllPostings()) {
		t.Fatalf("non-deterministic postings: left=%#v right=%#v", left.AllPostings(), right.AllPostings())
	}
	assertSinglePosting(t, left.Postings("go"), KindRoute, "route_1", "route.name", 2)
	assertSinglePosting(t, left.Postings("数据"), KindRoute, "route_1", "route.name", 2)
	assertSinglePosting(t, left.Postings("据库"), KindRoute, "route_1", "route.name", 2)
	assertSinglePosting(t, left.Postings("库数"), KindRoute, "route_1", "route.name", 1)
}

func TestReferenceIndexRejectsStaleOrPartialReplacement(t *testing.T) {
	t.Parallel()

	index := New()
	first := document(KindRow, "db_work", "tbl_notes", "row_1", 1,
		field("col_title", TextValue("Original")))
	if _, err := index.Replace(first); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		doc  Document
		want error
	}{
		{"same revision different content", document(KindRow, "db_work", "tbl_notes", "row_1", 1, field("col_title", TextValue("Changed"))), ErrRevisionConflict},
		{"stale revision", document(KindRow, "db_work", "tbl_notes", "row_1", 0, field("col_title", TextValue("Stale"))), ErrInvalidDocument},
		{"skipped revision", document(KindRow, "db_work", "tbl_notes", "row_1", 3, field("col_title", TextValue("Skipped"))), ErrRevisionConflict},
		{"partial replacement", func() Document {
			value := document(KindRow, "db_work", "tbl_notes", "row_1", 2, field("col_title", TextValue("Partial")))
			value.Complete = false
			return value
		}(), ErrInvalidDocument},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := index.Replace(test.doc); !errors.Is(err, test.want) {
				t.Fatalf("Replace() error = %v, want %v", err, test.want)
			}
			assertSinglePosting(t, index.Postings("original"), KindRow, "row_1", "col_title", 1)
		})
	}
}

func TestReferenceIndexBuildSeedsArbitraryCurrentRevisions(t *testing.T) {
	t.Parallel()

	seed := document(KindRow, "db_work", "tbl_notes", "row_1", 19,
		field("col_title", TextValue("Current snapshot")))
	index, err := Build([]Document{seed})
	if err != nil {
		t.Fatal(err)
	}
	assertSinglePosting(t, index.Postings("current"), KindRow, "row_1", "col_title", 1)
	if _, err := index.Replace(document(KindRow, "db_work", "tbl_notes", "row_1", 20,
		field("col_title", TextValue("Next snapshot")))); err != nil {
		t.Fatalf("replace seeded revision: %v", err)
	}

	empty := New()
	if _, err := empty.Replace(seed); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("empty incremental seed error = %v, want revision conflict", err)
	}
}

func TestReferenceIndexRejectsMalformedDocumentsWithoutPartialBuild(t *testing.T) {
	t.Parallel()

	invalidUTF8 := string([]byte{0xff})
	tests := []Document{
		{Version: "wrong", Kind: KindRow, DatabaseID: "db_work", TableID: "tbl_notes", ObjectID: "row_1", Revision: 1, SchemaRevision: 1, State: StateLive, Complete: true, Fields: []Field{field("col_title", TextValue("valid"))}},
		document(KindRow, "db_work", "tbl_notes", "bad\nrow", 1, field("col_title", TextValue("valid"))),
		document(KindRow, "db_work", "tbl_notes", "row_1", 1, field("col_title", TextValue(invalidUTF8))),
		document(KindRow, "db_work", "tbl_notes", "row_1", 1, field("col_title", TextValue("one")), field("col_title", TextValue("two"))),
	}
	for index, value := range tests {
		if _, err := Build([]Document{document(KindRoute, "db_work", "tbl_notes", "route_valid", 4,
			field("route.name", TextValue("valid"))), value}); !errors.Is(err, ErrInvalidDocument) {
			t.Fatalf("malformed document %d error = %v", index, err)
		}
	}
}

func TestReferenceIndexRandomRevisionSequenceMatchesSimpleReference(t *testing.T) {
	t.Parallel()

	random := rand.New(rand.NewSource(170))
	index := New()
	reference := map[string]referenceDocument{}
	vocabulary := []string{"alpha", "beta", "gamma", "delta", "epsilon"}
	for step := 0; step < 500; step++ {
		objectID := fmt.Sprintf("row_%d", random.Intn(12))
		current := reference[objectID]
		revision := current.revision + 1
		state := StateLive
		if revision > 1 && random.Intn(7) == 0 {
			if random.Intn(2) == 0 {
				state = StateDeleted
			} else {
				state = StateSuperseded
			}
		}
		terms := []string{}
		doc := Document{
			Version: DocumentVersion, Kind: KindRow, DatabaseID: "db_work", TableID: "tbl_notes",
			ObjectID: objectID, Revision: revision, SchemaRevision: 1, State: state, Complete: true,
		}
		if state == StateLive {
			for count := 1 + random.Intn(5); count > 0; count-- {
				terms = append(terms, vocabulary[random.Intn(len(vocabulary))])
			}
			doc.Fields = []Field{field("col_title", TextValue(strings.Join(terms, " ")))}
		}
		if _, err := index.Replace(doc); err != nil {
			t.Fatalf("step %d Replace(): %v", step, err)
		}
		reference[objectID] = referenceDocument{revision: revision, state: state, terms: terms}
		if got, want := index.AllPostings(), referencePostings(reference); !reflect.DeepEqual(got, want) {
			t.Fatalf("step %d postings mismatch:\n got=%#v\nwant=%#v", step, got, want)
		}
	}
}

func document(kind ObjectKind, databaseID, tableID, objectID string, revision uint64, fields ...Field) Document {
	return Document{
		Version: DocumentVersion, Kind: kind, DatabaseID: databaseID, TableID: tableID,
		ObjectID: objectID, Revision: revision, SchemaRevision: 1, State: StateLive,
		Complete: true, Fields: fields,
	}
}

func field(id string, values ...Value) Field { return Field{ID: id, Values: values} }

func assertSinglePosting(
	t *testing.T, values []Posting, kind ObjectKind, objectID, fieldID string, frequency uint64,
) {
	t.Helper()
	if len(values) != 1 || values[0].Kind != kind || values[0].ObjectID != objectID ||
		values[0].FieldID != fieldID || values[0].Frequency != frequency {
		t.Fatalf("postings = %#v, want %s/%s/%s frequency %d", values, kind, objectID, fieldID, frequency)
	}
}

func referencePostings(values map[string]referenceDocument) []Posting {
	result := []Posting{}
	for objectID, value := range values {
		if value.state != StateLive {
			continue
		}
		frequencies := map[string]uint64{}
		for _, term := range value.terms {
			frequencies[term]++
		}
		for term, frequency := range frequencies {
			result = append(result, Posting{
				Term: term, Kind: KindRow, DatabaseID: "db_work", TableID: "tbl_notes",
				ObjectID: objectID, Revision: value.revision, FieldID: "col_title", Frequency: frequency,
			})
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Term != result[right].Term {
			return result[left].Term < result[right].Term
		}
		return result[left].ObjectID < result[right].ObjectID
	})
	return result
}
