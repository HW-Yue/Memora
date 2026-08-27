package pagestoremigration

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestReplacementMarkerAcceptsStrictEpochGenerationIdentity(t *testing.T) {
	manifest := generationManifest{
		PlanDigest: strings.Repeat("a", 64), SourceFingerprint: strings.Repeat("b", 64),
	}
	marker, err := newAuthorityMarker(manifest)
	if err != nil {
		t.Fatal(err)
	}
	marker.Epoch = 1
	marker.Generation = "page-index-v1.g00000000000000000001." + manifest.PlanDigest
	marker.Digest, err = authorityMarkerDigest(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := marker.validate(manifest); err != nil {
		t.Fatalf("replacement marker validation error = %v", err)
	}
}

func TestReplacementMarkerEpochGoldenAndF107Compatibility(t *testing.T) {
	manifest := generationManifest{
		PlanDigest: strings.Repeat("a", 64), SourceFingerprint: strings.Repeat("b", 64),
	}
	legacy, err := newAuthorityMarker(manifest)
	if err != nil || legacy.Epoch != 0 || legacy.Generation != GenerationDirectory ||
		legacy.Digest != "999b0627afcc3548975f316c2c6a15c22f2d4874f13d5828cde741e550ef1cd4" {
		t.Fatalf("F107 marker changed = %+v, %v", legacy, err)
	}
	marker, err := newReplacementAuthorityMarker(manifest, 1)
	if err != nil {
		t.Fatal(err)
	}
	wantName := "page-index-v1.g00000000000000000001." + manifest.PlanDigest
	if marker.Generation != wantName || marker.Digest != "0917ffd92ef79bcce895833c078ab88ea8fb7f22bd3f5f86b659776d1d4b5e52" {
		t.Fatalf("replacement marker = %+v", marker)
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"epoch":1`) {
		t.Fatalf("replacement marker JSON = %s", encoded)
	}
	if epoch, digest, err := parseReplacementGenerationName(marker.Generation); err != nil ||
		epoch != 1 || digest != manifest.PlanDigest {
		t.Fatalf("parse replacement name = %d, %q, %v", epoch, digest, err)
	}
	for _, invalid := range []string{
		"../page-index-v1", "page-index-v1.g1." + manifest.PlanDigest,
		"page-index-v1.g00000000000000000000." + manifest.PlanDigest,
		"page-index-v1.g00000000000000000001.not-a-digest",
	} {
		if _, _, err := parseReplacementGenerationName(invalid); err == nil {
			t.Fatalf("invalid replacement generation %q was accepted", invalid)
		}
	}
}

func TestReplacementMarkerRejectsReboundAndTraversalIdentity(t *testing.T) {
	manifest := generationManifest{
		PlanDigest: strings.Repeat("a", 64), SourceFingerprint: strings.Repeat("b", 64),
	}
	valid, err := newReplacementAuthorityMarker(manifest, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*authorityMarker){
		func(marker *authorityMarker) { marker.Generation = "../" + marker.Generation },
		func(marker *authorityMarker) { marker.Epoch = 2 },
		func(marker *authorityMarker) { marker.PlanDigest = strings.Repeat("c", 64) },
	} {
		marker := valid
		mutate(&marker)
		marker.Digest, err = authorityMarkerDigest(marker)
		if err != nil {
			t.Fatal(err)
		}
		if err := marker.validateSelf(); err == nil {
			t.Fatalf("rebound replacement marker was accepted: %+v", marker)
		}
	}
}

func TestReplacementBuildsValidGenerationSwapsAndReopens(t *testing.T) {
	ctx := context.Background()
	directory, file, authority := newAuthorityFixture(t)
	_, rows, table, inserted := authorityValues(t, ctx, file, authority)
	oldGeneration := authority.marker.Generation
	oldPointer := authority.Generation()

	receipt, err := authority.ReplaceGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.PreviousGeneration != oldGeneration || receipt.Generation == oldGeneration ||
		receipt.Epoch != 1 || receipt.Reused || receipt.PlanDigest == "" {
		t.Fatalf("replacement receipt = %+v", receipt)
	}
	if authority.Generation() == oldPointer || authority.marker.Generation != receipt.Generation {
		t.Fatal("Authority did not swap its live generation")
	}
	if _, err := os.Stat(filepath.Join(directory, oldGeneration)); err != nil {
		t.Fatalf("old generation was not preserved: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, receipt.Generation)); err != nil {
		t.Fatalf("replacement generation is missing: %v", err)
	}
	marker, err := decodeAuthorityMarker(directory)
	if err != nil || marker.Epoch != 1 || marker.Generation != receipt.Generation {
		t.Fatalf("replacement marker = %+v, %v", marker, err)
	}
	got, err := rows.Get(ctx, "work", "notes", inserted.ID)
	if err != nil || got.Values["title"] != "indexed" {
		t.Fatalf("Get() after swap = %+v, %v", got, err)
	}
	after, err := rows.Insert(ctx, "work", "notes", map[string]any{"title": "after swap"}, row.WriteOptions{
		ExpectedSchemaVersion: table.SchemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedFile, err := nativestore.Open(filepath.Join(directory, "database.memora"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopenedFile.Close()
	reopened, err := OpenAuthority(ctx, reopenedFile, directory)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	dictionary := nativecatalog.NewService(
		nativecatalog.New(reopenedFile), nativecatalog.ServiceOptions{Authority: reopened},
	)
	reopenedRows := nativerow.NewService(
		nativerow.New(reopenedFile), dictionary, nativerow.ServiceOptions{Authority: reopened},
	)
	got, err = reopenedRows.Get(ctx, "work", "notes", after.ID)
	if err != nil || got.Values["title"] != "after swap" || reopened.marker.Epoch != 1 {
		t.Fatalf("reopened replacement Get() = %+v, marker=%+v, %v", got, reopened.marker, err)
	}
}

func TestReplacementMatchesCompleteReferencePlan(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, table, _ := authorityValuesWithoutRow(t, ctx, file, authority)
	wantBodies := make(map[string]row.Row)
	for index := 0; index < 32; index++ {
		inserted, err := rows.Insert(ctx, "work", "notes", map[string]any{
			"title": fmt.Sprintf("note-%02d", index),
		}, row.WriteOptions{ExpectedSchemaVersion: table.SchemaVersion})
		if err != nil {
			t.Fatal(err)
		}
		wantBodies[fmt.Sprintf("%s/%d", inserted.ID, inserted.Revision)] = inserted
		if index%2 == 0 {
			updated, err := rows.Update(ctx, "work", "notes", inserted.ID, map[string]any{
				"title": fmt.Sprintf("revised-%02d", index),
			}, row.WriteOptions{
				ExpectedSchemaVersion: table.SchemaVersion, ExpectedRevision: inserted.Revision,
			})
			if err != nil {
				t.Fatal(err)
			}
			wantBodies[fmt.Sprintf("%s/%d", updated.ID, updated.Revision)] = updated
		}
	}
	plan, err := authority.reader.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.CurrentRows) != 32 || len(plan.RowVersions) != len(wantBodies) {
		t.Fatalf("reference Plan current=%d versions=%d", len(plan.CurrentRows), len(plan.RowVersions))
	}
	if _, err := authority.ReplaceGeneration(ctx); err != nil {
		t.Fatal(err)
	}
	generation := authority.Generation()
	if err := verifyGenerationPlan(ctx, generation, plan); err != nil {
		t.Fatalf("replacement/reference verification = %v", err)
	}
	for _, locator := range plan.CurrentRows {
		if got, err := generation.CurrentRowsFor(locator.TableID).Lookup(locator.RowID); err != nil || got != locator {
			t.Fatalf("current locator %s = %+v, %v", locator.RowID, got, err)
		}
	}
	for _, locator := range plan.RowVersions {
		if got, err := generation.RowVersions().ByRevision(locator.RowID, locator.Revision); err != nil || got != locator {
			t.Fatalf("version locator %s/%d = %+v, %v", locator.RowID, locator.Revision, got, err)
		}
		body, err := rows.AsOfRevision(ctx, "work", "notes", locator.RowID, locator.Revision)
		want := wantBodies[fmt.Sprintf("%s/%d", locator.RowID, locator.Revision)]
		if err != nil || body.ID != want.ID || body.Revision != want.Revision ||
			body.Values["title"] != want.Values["title"] {
			t.Fatalf("body %s/%d = %+v, want %+v, %v", locator.RowID, locator.Revision, body, want, err)
		}
	}
}

func TestReplacementConcurrentCallsSerializeEpochsWhileReadersContinue(t *testing.T) {
	ctx := context.Background()
	_, file, authority := newAuthorityFixture(t)
	_, rows, _, inserted := authorityValues(t, ctx, file, authority)

	stopReaders := make(chan struct{})
	readerDone := make(chan struct{})
	readerStarted := make(chan struct{})
	readerErrors := make(chan error, 1)
	var reads atomic.Uint64
	go func() {
		defer close(readerDone)
		for {
			select {
			case <-stopReaders:
				return
			default:
			}
			got, err := rows.Get(ctx, "work", "notes", inserted.ID)
			if err != nil || got.ID != inserted.ID {
				select {
				case readerErrors <- fmt.Errorf("reader during replacements = %+v, %w", got, err):
				default:
				}
				return
			}
			if reads.Add(1) == 1 {
				close(readerStarted)
			}
		}
	}()
	<-readerStarted

	start := make(chan struct{})
	results := make([]ReplacementReceipt, 2)
	errorsByCall := make([]error, 2)
	var wait sync.WaitGroup
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errorsByCall[index] = authority.ReplaceGeneration(ctx)
		}(index)
	}
	close(start)
	wait.Wait()
	close(stopReaders)
	<-readerDone
	select {
	case err := <-readerErrors:
		t.Fatal(err)
	default:
	}
	for index, err := range errorsByCall {
		if err != nil {
			t.Fatalf("replacement %d error = %v", index, err)
		}
	}
	epochs := map[uint64]bool{results[0].Epoch: true, results[1].Epoch: true}
	if !epochs[1] || !epochs[2] || authority.marker.Epoch != 2 || reads.Load() < 2 {
		t.Fatalf("concurrent replacement results=%+v marker=%+v reads=%d", results, authority.marker, reads.Load())
	}
}
