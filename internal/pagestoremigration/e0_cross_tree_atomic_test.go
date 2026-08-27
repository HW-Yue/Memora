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
// The generation is opened directly rather than through OpenAuthority on
// purpose: OpenAuthority runs a reconcile pass that repairs the divergence from
// the native store file. That repair is real and it works — it is why users do
// not see this today — but it is a compensation for missing atomicity, not
// atomicity. Reading through it would hide exactly what is under test.
//
// See docs/storage/shared-circular-redo-v1.md §2.1.
func TestCrossTreePublicationIsAtomicUnderFault(t *testing.T) {
	for _, phase := range []authorityPhase{
		phaseRowVersionPublished, phaseRowFulltextPublished, phaseRowCurrentPublished,
	} {
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
			}); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Update(%s fault) error = %v, want ErrOutcomeUnknown", phase, err)
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

			// Both or neither. Which one is not the point — a publication is
			// allowed to be lost by a fault, but it is not allowed to be half
			// applied.
			if versionsHas != currentHas {
				t.Fatalf(
					"Trees disagree after a fault at %s: versions=%v current=%v",
					phase, versionsHas, currentHas,
				)
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
func TestCatalogPublicationSurvivesAFaultWithoutTearing(t *testing.T) {
	for _, phase := range []authorityPhase{phaseCatalogPublished, phaseCatalogFulltextPublished} {
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
