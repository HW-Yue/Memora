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
// One Row Update writes three Trees — versions, fulltext, current. Each Tree
// used to own a WAL, so that was three independent commits, and a fault landing
// between them left the three Trees describing different Rows.
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
			locator, currentErr := generation.CurrentRows().Lookup(table.ID, inserted.ID)
			currentHas := currentErr == nil && locator.Revision == 2
			postings, err := generation.Fulltext().Postings("revised")
			if err != nil {
				t.Fatal(err)
			}
			fulltextHas := len(postings) == 1 && postings[0].Revision == 2

			// All three or none. Which one is not the point — a publication is
			// allowed to be lost by a fault, but it is not allowed to be half
			// applied.
			if versionsHas != currentHas || versionsHas != fulltextHas {
				t.Fatalf(
					"Trees disagree after a fault at %s: versions=%v fulltext=%v current=%v",
					phase, versionsHas, fulltextHas, currentHas,
				)
			}
		})
	}
}

// TestCatalogPublicationIsAtomicUnderFault is the Catalog half of stage 2.
//
// Creating a Table writes the Catalog Tree and the Fulltext Tree. Those were
// two commits as well, with the same tearing window, so they are now one
// transaction for the same reason.
func TestCatalogPublicationIsAtomicUnderFault(t *testing.T) {
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

			// Raw generation again: OpenAuthority's reconcile would repair the
			// divergence before it could be observed.
			generation, err := openLiveGeneration(filepath.Join(directory, GenerationDirectory))
			if err != nil {
				t.Fatal(err)
			}
			defer generation.Close()

			catalogHas := false
			if created.ID != "" {
				if _, err := generation.Catalog().TableByID(created.ID); err == nil {
					catalogHas = true
				}
			}
			postings, err := generation.Fulltext().Postings("journal")
			if err != nil {
				t.Fatal(err)
			}
			fulltextHas := len(postings) != 0
			if catalogHas != fulltextHas {
				t.Fatalf(
					"Trees disagree after a fault at %s: catalog=%v fulltext=%v",
					phase, catalogHas, fulltextHas,
				)
			}
		})
	}
}
