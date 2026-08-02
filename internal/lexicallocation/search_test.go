package lexicallocation

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
)

type recordedSource struct {
	postings map[string][]fulltext.Posting
	calls    []sourceCall
}

type sourceCall struct {
	term      string
	databases []string
}

func (source *recordedSource) PostingsInDatabases(term string, databases []string) ([]fulltext.Posting, error) {
	source.calls = append(source.calls, sourceCall{term: term, databases: append([]string(nil), databases...)})
	return append([]fulltext.Posting(nil), source.postings[term]...), nil
}

func TestSearchAggregatesTermsAndFieldsWithDeterministicLocationOrder(t *testing.T) {
	source := &recordedSource{postings: map[string][]fulltext.Posting{
		"alpha": {
			posting("alpha", fulltext.KindTable, "db_a", "tbl_a", "tbl_a", 2, "purpose", 1),
			posting("alpha", fulltext.KindRow, "db_a", "tbl_a", "row_a", 4, "col_title", 3),
			posting("alpha", fulltext.KindRoute, "db_a", "tbl_a", "route_a", 5, "synopsis", 1),
		},
		"beta": {
			posting("beta", fulltext.KindRoute, "db_a", "tbl_a", "route_a", 5, "name", 2),
			posting("beta", fulltext.KindRow, "db_a", "tbl_a", "row_a", 4, "col_body", 1),
		},
	}}
	page, err := Search(source, Request{Query: "Alpha beta alpha", DatabaseIDs: []string{"db_a"}, Limit: 64, ByteLimit: 65536})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Locations) != 3 || page.Locations[0].ObjectID != "row_a" ||
		page.Locations[1].ObjectID != "route_a" || page.Locations[2].ObjectID != "tbl_a" {
		t.Fatalf("locations = %#v", page.Locations)
	}
	row := page.Locations[0]
	if row.MatchedTermCount != 2 || row.MatchedFieldCount != 2 || row.Frequency != 4 ||
		!reflect.DeepEqual(row.MatchedFieldIDs, []string{"col_body", "col_title"}) {
		t.Fatalf("Row aggregate = %#v", row)
	}
	wantCalls := []sourceCall{{term: "alpha", databases: []string{"db_a"}}, {term: "beta", databases: []string{"db_a"}}}
	if !reflect.DeepEqual(source.calls, wantCalls) {
		t.Fatalf("posting calls = %#v, want %#v", source.calls, wantCalls)
	}
}

func TestSearchCursorBindsQueryScopeAndCandidateSnapshot(t *testing.T) {
	source := &recordedSource{postings: map[string][]fulltext.Posting{"alpha": {
		posting("alpha", fulltext.KindRow, "db_a", "tbl", "row_a", 1, "body", 1),
		posting("alpha", fulltext.KindRow, "db_a", "tbl", "row_b", 1, "body", 1),
	}}}
	first, err := Search(source, Request{Query: "alpha", DatabaseIDs: []string{"db_a"}, Limit: 1, ByteLimit: 4096})
	if err != nil || !first.Truncated || first.NextCursor == "" || len(first.Locations) != 1 {
		t.Fatalf("first page = %#v, %v", first, err)
	}
	second, err := Search(source, Request{Query: "alpha", DatabaseIDs: []string{"db_a"}, Cursor: first.NextCursor, Limit: 1, ByteLimit: 4096})
	if err != nil || second.Truncated || len(second.Locations) != 1 || second.Snapshot != first.Snapshot {
		t.Fatalf("second page = %#v, %v", second, err)
	}
	if _, err := Search(source, Request{Query: "beta", DatabaseIDs: []string{"db_a"}, Cursor: first.NextCursor, Limit: 1, ByteLimit: 4096}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-query cursor error = %v", err)
	}
	if _, err := Search(source, Request{Query: "alpha", DatabaseIDs: []string{"db_b"}, Cursor: first.NextCursor, Limit: 1, ByteLimit: 4096}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cross-scope cursor error = %v", err)
	}
	source.postings["alpha"] = append(source.postings["alpha"],
		posting("alpha", fulltext.KindRow, "db_a", "tbl", "row_c", 1, "body", 1))
	if _, err := Search(source, Request{Query: "alpha", DatabaseIDs: []string{"db_a"}, Cursor: first.NextCursor, Limit: 1, ByteLimit: 4096}); !errors.Is(err, ErrConflict) {
		t.Fatalf("snapshot drift error = %v", err)
	}
	tampered := first.NextCursor[:len(first.NextCursor)-1] + "A"
	if strings.HasSuffix(first.NextCursor, "A") {
		tampered = first.NextCursor[:len(first.NextCursor)-1] + "B"
	}
	if _, err := Search(source, Request{Query: "alpha", DatabaseIDs: []string{"db_a"}, Cursor: tampered, Limit: 1, ByteLimit: 4096}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered cursor error = %v", err)
	}
}

func TestSearchEnforcesInputOutputAndPhysicalScope(t *testing.T) {
	for _, request := range []Request{
		{Query: "", DatabaseIDs: []string{"db"}, Limit: 1, ByteLimit: 256},
		{Query: strings.Repeat("x", 257), DatabaseIDs: []string{"db"}, Limit: 1, ByteLimit: 256},
		{Query: strings.Repeat("term ", 33), DatabaseIDs: []string{"db"}, Limit: 1, ByteLimit: 256},
		{Query: "term", DatabaseIDs: []string{"db"}, Limit: 0, ByteLimit: 256},
		{Query: "term", DatabaseIDs: []string{"db"}, Limit: 65, ByteLimit: 256},
		{Query: "term", DatabaseIDs: []string{"db"}, Limit: 1, ByteLimit: 255},
	} {
		if _, err := Search(&recordedSource{}, request); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Search(%#v) error = %v", request, err)
		}
	}
	emptySource := &recordedSource{}
	empty, err := Search(emptySource, Request{Query: "term", Limit: 1, ByteLimit: 256})
	if err != nil || len(empty.Locations) != 0 || len(emptySource.calls) != 0 {
		t.Fatalf("empty scope = %#v calls=%#v error=%v", empty, emptySource.calls, err)
	}
	leaking := &recordedSource{postings: map[string][]fulltext.Posting{"term": {
		posting("term", fulltext.KindRow, "db_secret", "tbl", "row", 1, "body", 1),
	}}}
	if _, err := Search(leaking, Request{Query: "term", DatabaseIDs: []string{"db_allowed"}, Limit: 1, ByteLimit: 256}); !errors.Is(err, ErrSourceScope) {
		t.Fatalf("leaking source error = %v", err)
	}
}

func posting(term string, kind fulltext.ObjectKind, databaseID, tableID, objectID string, revision uint64, fieldID string, frequency uint64) fulltext.Posting {
	return fulltext.Posting{Term: term, Kind: kind, DatabaseID: databaseID, TableID: tableID,
		ObjectID: objectID, Revision: revision, FieldID: fieldID, Frequency: frequency}
}
