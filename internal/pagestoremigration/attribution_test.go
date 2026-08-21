package pagestoremigration

import (
	"context"
	"testing"
)

// TestEveryRevisionResolvesAttributionThroughItsEnvelope is the guard against a
// silent regression that already happened once: the fallback to the per-Row
// History record works, so an attribution join that never fires looks green from
// the outside. Every observable field matches either way.
//
// This asserts the path itself. A revision that falls back here means the change
// sequence is missing, wrong, or unresolvable — the join is not carrying the
// weight it is supposed to carry.
func TestEveryRevisionResolvesAttributionThroughItsEnvelope(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, value := authorityValues(t, ctx, file, authority)

	versions, err := authority.rows.HistoryVersions(ctx, table, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) == 0 {
		t.Fatal("fixture produced no Row versions")
	}
	for _, version := range versions {
		if version.ChangeSequence == 0 {
			t.Fatalf("revision %d carries no change sequence, so it can never join to its transaction", version.Revision)
		}
		record, ok := authority.attribute(version)
		if !ok {
			t.Fatalf("revision %d fell back instead of resolving through its envelope", version.Revision)
		}
		if record.Actor == "" || record.Reason == "" {
			t.Fatalf("revision %d resolved to empty attribution: %#v", version.Revision, record)
		}
		if record.RowID != version.ID || record.Revision != version.Revision {
			t.Fatalf("revision %d resolved to another revision: %#v", version.Revision, record)
		}
	}
}

// TestRevisionWithoutAChangeSequenceFallsBack pins the other half of the
// contract: a revision written before the link existed still reports its
// attribution, from the per-Row History record those Databases hold.
func TestRevisionWithoutAChangeSequenceFallsBack(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, value := authorityValues(t, ctx, file, authority)

	versions, err := authority.rows.HistoryVersions(ctx, table, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy := versions[0]
	legacy.ChangeSequence = 0
	if _, ok := authority.attribute(legacy); ok {
		t.Fatal("a revision with no change sequence must not claim to resolve an envelope")
	}
	record, ok := authority.rows.LegacyHistoryRecord(legacy)
	if !ok || record.Actor == "" {
		t.Fatalf("the legacy fallback must still supply attribution, got %#v ok=%v", record, ok)
	}
}

// TestHistoryReportsTheSameAttributionThroughEitherPath pins that the two paths
// agree. If they ever diverge, the fallback would quietly change what a caller
// sees depending on how old the revision is.
func TestHistoryReportsTheSameAttributionThroughEitherPath(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, _, table, value := authorityValues(t, ctx, file, authority)

	versions, err := authority.rows.HistoryVersions(ctx, table, value.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, version := range versions {
		viaEnvelope, ok := authority.attribute(version)
		if !ok {
			t.Fatalf("revision %d did not resolve through its envelope", version.Revision)
		}
		viaLegacy, ok := authority.rows.LegacyHistoryRecord(version)
		if !ok {
			continue
		}
		if viaEnvelope.Actor != viaLegacy.Actor ||
			viaEnvelope.Source != viaLegacy.Source ||
			viaEnvelope.Reason != viaLegacy.Reason ||
			viaEnvelope.Operation != viaLegacy.Operation {
			t.Fatalf("revision %d: envelope %#v disagrees with legacy record %#v",
				version.Revision, viaEnvelope, viaLegacy)
		}
	}
}
