package change

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestEnvelopeCanonicalizesEntriesScopesAndRelatedIDs(t *testing.T) {
	now := time.Date(2026, 8, 1, 13, 0, 0, 0, time.FixedZone("offset", 8*60*60))
	value, err := NewEnvelope(9, now, Metadata{
		Actor: " agent:test ", Source: " msql ", Reason: " split ",
	}, []Entry{
		{
			ObjectKind: ObjectRow, DatabaseID: "db_b", TableID: "tbl_notes", ObjectID: "row_2",
			Operation: OperationSplit, BeforeRevision: 1, AfterRevision: 2, SchemaVersion: 1,
			HistoryLocator: "row_2@00000000000000000002", RelatedObjectIDs: []string{"row_4", "row_3"},
		},
		{
			ObjectKind: ObjectRouteNode, DatabaseID: "db_a", TableID: "tbl_route", ObjectID: "route_1",
			Operation: OperationUpdate, BeforeRevision: 2, AfterRevision: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if value.Actor != "agent:test" || value.CommittedAt.Location() != time.UTC ||
		len(value.DatabaseIDs) != 2 || value.DatabaseIDs[0] != "db_a" || value.DatabaseIDs[1] != "db_b" ||
		value.Entries[0].ObjectKind != ObjectRouteNode || value.Entries[1].RelatedObjectIDs[0] != "row_3" {
		t.Fatalf("canonical envelope = %+v", value)
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeRejectsTamperingDuplicatesAndOversizedEntrySet(t *testing.T) {
	entry := Entry{
		ObjectKind: ObjectConfiguration, ObjectID: "query_budgets", Operation: OperationUpdate,
		BeforeRevision: 1, AfterRevision: 2,
	}
	value, err := NewEnvelope(1, time.Now(), Metadata{
		Actor: "agent:test", Source: "msql", Reason: "tune",
	}, []Entry{entry})
	if err != nil {
		t.Fatal(err)
	}
	value.Reason = "tampered"
	if err := value.Validate(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("tampered Validate() error = %v", err)
	}
	if _, err := NewEnvelope(1, time.Now(), Metadata{
		Actor: "agent:test", Source: "msql", Reason: "duplicate",
	}, []Entry{entry, entry}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate NewEnvelope() error = %v", err)
	}
	tooMany := make([]Entry, MaxEntries+1)
	for index := range tooMany {
		tooMany[index] = entry
		tooMany[index].ObjectID = strings.Repeat("x", 1) + string(rune(index+1))
	}
	if _, err := NewEnvelope(1, time.Now(), Metadata{
		Actor: "agent:test", Source: "msql", Reason: "oversized",
	}, tooMany); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized NewEnvelope() error = %v", err)
	}
}
