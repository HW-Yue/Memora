package pagestoremigration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/row"
)

// TestCrossTreePublicationIsAtomicUnderFault is E0 stage 2's gate, and the
// central piece of evidence for the shared redo log.
//
// One Row Update writes the two authoritative Trees — versions and current.
// Each Tree used to own a WAL, so that was independent commits, and a fault
// landing between them left the Trees describing different Rows.
//
// The Fulltext Tree used to be in this set and no longer is: it is a derived
// index that catches up from the committed change log, outside the write's
// transaction. Asserting it here would be asserting the coupling E2 removed.
//
// The generation is opened directly rather than through OpenAuthority, which
// is how this read stays a read of what the fault actually left on disk.
//
// E8 stage 1 sharpened what "atomic" means here, and moved where the fault has
// to be injected. The record log used to commit first, so a fault after it was
// an uncertain outcome: the write had happened in one store and not the other,
// and the answer was to fence the Database and repair the Trees from the record
// log on the next open. The Trees commit first now, so the only fault that can
// produce a half-written pair is one landing before the group commit — the
// phases after it fire once the commit has already returned, and a write that
// has committed is not this test's subject (see
// TestARowPublicationFaultAfterTheTreeCommitStillCommitted).
//
// "Both or neither" is still the assertion; what changed is that "neither" is
// now the only outcome a fault can produce.
//
// See docs/storage/shared-circular-redo-v1.md §2.1.
func TestCrossTreePublicationIsAtomicUnderFault(t *testing.T) {
	for _, phase := range []authorityPhase{phaseRowBodyCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			_, rows, table, inserted := authorityValues(t, ctx, file, authority)

			injected := errors.New("injected publication fault")
			authority.checkpoint = func(current authorityPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			if _, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
				"title": "revised",
			}, row.WriteOptions{
				ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
			}); !errors.Is(err, injected) {
				t.Fatalf("Update(%s fault) error = %v, want the injected failure", phase, err)
			} else if errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Update(%s fault) reported an unknown outcome: %v", phase, err)
			}
			authority.checkpoint = nil
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			generation, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
			if err != nil {
				t.Fatal(err)
			}
			defer generation.Close()

			_, versionsErr := generation.RowVersions().ByRevision(inserted.ID, 2)
			versionsHas := versionsErr == nil
			locator, currentErr := generation.CurrentRowsFor(table.ID).Lookup(inserted.ID)
			currentHas := currentErr == nil && locator.Revision == 2

			// Both or neither — and now, specifically, neither.
			if versionsHas != currentHas {
				t.Fatalf(
					"Trees disagree after a fault at %s: versions=%v current=%v",
					phase, versionsHas, currentHas,
				)
			}
			if versionsHas {
				t.Fatalf("a failed publication at %s left the revision committed", phase)
			}
		})
	}
}

// TestCatalogPublicationSurvivesAFaultWithoutTearing.
//
// Creating a Table used to write the Catalog Tree and the Fulltext Tree, in two
// commits with a tearing window between them. E0 stage 2 made them one
// transaction; E2 then took the Fulltext Tree out of the write path entirely,
// so the publication now writes one Tree and there is no cross-Tree window left
// to tear in. What remains worth pinning is that a fault leaves the Catalog
// Tree in one state or the other, never half a Table.
//
// E8 stage 1 moved which phase can still produce that fault: the ones after the
// group commit fire once it has returned, so they leave a committed transition
// rather than a lost one (TestACatalogPublicationFaultAfterTheTreeCommitStillCommitted).
// The phase before it is the one left that a write can fail at.
func TestCatalogPublicationSurvivesAFaultWithoutTearing(t *testing.T) {
	for _, phase := range []authorityPhase{phaseCatalogBodyCommitted} {
		t.Run(string(phase), func(t *testing.T) {
			ctx := context.Background()
			directory, file, authority := newAuthorityFixture(t)
			dictionary, _, _, _ := authorityValuesWithoutRow(t, ctx, file, authority)

			injected := errors.New("injected catalog publication fault")
			authority.checkpoint = func(current authorityPhase) error {
				if current == phase {
					return injected
				}
				return nil
			}
			created, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
				Name: "journal", Purpose: "Journal", RowSemantics: "One entry",
				Columns: []catalog.ColumnDefinition{{Name: "body", Type: "TEXT(40)", Purpose: "Body"}},
			})
			if err == nil {
				t.Fatalf("CreateTable(%s fault) unexpectedly succeeded", phase)
			}
			authority.checkpoint = nil
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}

			generation, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
			if err != nil {
				t.Fatal(err)
			}
			defer generation.Close()

			if created.ID == "" {
				return
			}
			table, err := generation.Catalog().TableByID(created.ID)
			if err != nil {
				// Losing the publication to a fault is allowed.
				return
			}
			// Having it means having all of it.
			if table.ID != created.ID || table.DatabaseID == "" {
				t.Fatalf("Catalog Tree holds a torn Table: %#v", table)
			}
		})
	}
}
