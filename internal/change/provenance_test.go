package change

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func provenanceEntry() Entry {
	return Entry{
		ObjectKind: ObjectRow, DatabaseID: "db_work", TableID: "tbl_notes", ObjectID: "row_1",
		Operation: OperationUpdate, BeforeRevision: 1, AfterRevision: 2, SchemaVersion: 1,
		HistoryLocator: "row_1@00000000000000000002",
	}
}

// TestEnvelopeCarriesTheAnchorProvenance pins that the Change Log records the
// document- and repository-anchor fields. They used to live only on the
// per-Row History record; the envelope is becoming the single home for
// attribution, so it has to carry everything attribution means.
func TestEnvelopeCarriesTheAnchorProvenance(t *testing.T) {
	value, err := NewEnvelope(7, time.Now(), Metadata{
		Actor: " agent:test ", Source: " msql ", Reason: " anchored write ",
		SourceReceiptID: " receipt_1 ", SourceKind: " document_anchor ",
		SourceLocator: " docs/design.md#L20 ", SourceContentHash: " sha256:abc ",
	}, []Entry{provenanceEntry()})
	if err != nil {
		t.Fatal(err)
	}
	if value.SourceKind != "document_anchor" ||
		value.SourceLocator != "docs/design.md#L20" ||
		value.SourceContentHash != "sha256:abc" {
		t.Fatalf("anchor provenance = %+v", value.Metadata)
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAnchorProvenanceIsCoveredByTheChecksum pins that the new fields are part
// of what the envelope's checksum protects. Attribution that could be edited
// without invalidating the envelope would not be attribution.
func TestAnchorProvenanceIsCoveredByTheChecksum(t *testing.T) {
	base := Metadata{Actor: "agent:test", Source: "msql", Reason: "write"}
	plain, err := NewEnvelope(7, time.Now(), base, []Entry{provenanceEntry()})
	if err != nil {
		t.Fatal(err)
	}
	anchored := base
	anchored.SourceLocator = "docs/design.md#L20"
	withAnchor, err := NewEnvelope(7, plain.CommittedAt, anchored, []Entry{provenanceEntry()})
	if err != nil {
		t.Fatal(err)
	}
	if plain.Checksum == withAnchor.Checksum {
		t.Fatal("changing anchor provenance must change the checksum")
	}

	tampered := withAnchor
	tampered.SourceLocator = "docs/other.md"
	if err := tampered.Validate(); err == nil {
		t.Fatal("editing anchor provenance must invalidate the envelope")
	}
}

// TestEnvelopeWithoutAnchorProvenanceIsUnchangedOnTheWire pins backward
// compatibility: an envelope written before these fields existed carries none
// of them, and must serialize — and therefore checksum — exactly as it did.
func TestEnvelopeWithoutAnchorProvenanceIsUnchangedOnTheWire(t *testing.T) {
	value, err := NewEnvelope(7, time.Now(), Metadata{
		Actor: "agent:test", Source: "msql", Reason: "write",
	}, []Entry{provenanceEntry()})
	if err != nil {
		t.Fatal(err)
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"source_kind", "source_locator", "source_content_hash"} {
		if strings.Contains(string(encoded), absent) {
			t.Fatalf("an envelope with no anchor provenance must not emit %q: %s", absent, encoded)
		}
	}
	if err := value.Validate(); err != nil {
		t.Fatal(err)
	}
}

// TestAnchorProvenanceIsBounded pins that the new fields are length-checked like
// every other text field, so a malformed write cannot produce an envelope the
// reader will later refuse.
func TestAnchorProvenanceIsBounded(t *testing.T) {
	oversized := Metadata{
		Actor: "agent:test", Source: "msql", Reason: "write",
		SourceLocator: strings.Repeat("x", (4<<10)+1),
	}
	if _, err := NewEnvelope(7, time.Now(), oversized, []Entry{provenanceEntry()}); err == nil {
		t.Fatal("an oversized source locator must be refused")
	}
}
