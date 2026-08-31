package nativerow

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

func TestIndexedExecutorCapturesOneFreshSnapshotPerAutocommitStatement(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	calls := 0
	session := executor.NewBatchSessionWithPointReads(
		ctx, &poisonIndexedCatalog{calls: &calls}, &poisonIndexedRows{calls: &calls}, points,
	)
	t.Cleanup(func() { _ = session.Close() })
	query := func(requestID string) string {
		envelope := session.Execute(ctx, executor.BatchRequest{
			RequestID: requestID,
			Source:    "SELECT title FROM work.notes WHERE row_id = 'row_live' LIMIT 1",
		})
		if !envelope.OK || len(envelope.Results) != 1 || len(envelope.Results[0].Rows) != 1 {
			t.Fatalf("%s envelope = %#v", requestID, envelope)
		}
		return envelope.Results[0].Rows[0]["title"].(string)
	}
	if got := query("before-commit"); got != "current" {
		t.Fatalf("before commit title = %q", got)
	}
	publishLaterRows(t, fixture)
	if got := query("after-commit"); got != "future" {
		t.Fatalf("after commit title = %q", got)
	}
	if calls != 0 {
		t.Fatalf("snapshot Executor used legacy fallback %d time(s)", calls)
	}
}

func TestReadViewCapturedBetweenCurrentAndVersionPublishSeesOldCommit(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	_, rows := indexedPointData()
	updated := rows["row_live"][1]
	updated.Revision = 3
	updated.CommitSequence = 30
	updated.Values = map[string]any{"col_title": "future"}
	if err := New(fixture.file).WriteRevision(updated); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.current.Apply(3, []currentrowindex.Update{{
		ExpectedRevision: 2, Locator: currentLocator(updated),
	}}); err != nil {
		t.Fatal(err)
	}

	midPublish, err := points.BeginReadView(ctx)
	if err != nil || midPublish.Sequence() != 25 {
		t.Fatalf("mid-publish snapshot = %d, %v", midPublish.Sequence(), err)
	}
	old, err := midPublish.Get(ctx, table, updated.ID)
	if err != nil || old.Revision != 2 || old.Values["title"] != "current" {
		t.Fatalf("mid-publish Get() = %#v, %v", old, err)
	}
	if _, err := fixture.versions.Append(2, []rowversionindex.Locator{versionLocator(updated)}); err != nil {
		t.Fatal(err)
	}
	stillOld, err := midPublish.Get(ctx, table, updated.ID)
	if err != nil || stillOld.Revision != 2 {
		t.Fatalf("old view after high-water advance = %#v, %v", stillOld, err)
	}
	newView, err := points.BeginReadView(ctx)
	if err != nil || newView.Sequence() != 30 {
		t.Fatalf("post-publish snapshot = %d, %v", newView.Sequence(), err)
	}
	latest, err := newView.Get(ctx, table, updated.ID)
	if err != nil || latest.Revision != 3 || latest.Values["title"] != "future" {
		t.Fatalf("post-publish Get() = %#v, %v", latest, err)
	}
}

func TestReadViewKeepsPointAndCursorVisibilityAcrossLaterCommit(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, err := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	if err != nil {
		t.Fatal(err)
	}
	points, err := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	if err != nil {
		t.Fatal(err)
	}
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	view, err := points.BeginReadView(ctx)
	if err != nil || view.Sequence() != 25 {
		t.Fatalf("BeginReadView() sequence = %d, %v", view.Sequence(), err)
	}
	before, err := view.Get(ctx, table, "row_live")
	if err != nil || before.Revision != 2 || before.Values["title"] != "current" {
		t.Fatalf("initial snapshot Get() = %#v, %v", before, err)
	}
	firstPage, err := view.PageLocators(ctx, table, "", 1)
	if err != nil || len(firstPage.Locators) != 1 || firstPage.Locators[0].RowID != "row_live" ||
		!firstPage.HasMore || firstPage.NextAfterRowID != "row_live" {
		t.Fatalf("first snapshot Page() = %#v, %v", firstPage, err)
	}

	publishLaterRows(t, fixture)
	after, err := view.Get(ctx, table, "row_live")
	if err != nil || after.Revision != 2 || after.Values["title"] != "current" {
		t.Fatalf("stable snapshot Get() = %#v, %v", after, err)
	}
	if _, err := points.AsOfRevision(ctx, table, "row_live", 3, view.Sequence()); !hasCode(err, "not_found") {
		t.Fatalf("future AS OF REVISION error = %v", err)
	}
	capped, err := points.AsOfCommit(ctx, table, "row_live", 30, view.Sequence())
	if err != nil || capped.Revision != 2 || capped.CommitSequence != 20 {
		t.Fatalf("snapshot-capped AS OF COMMIT = %#v, %v", capped, err)
	}
	secondPage, err := view.PageLocators(ctx, table, firstPage.NextAfterRowID, 1)
	if err != nil || len(secondPage.Locators) != 1 || secondPage.Locators[0].RowID != "row_deleted" ||
		secondPage.Locators[0].Revision != 2 || !secondPage.HasMore {
		t.Fatalf("second snapshot Page() = %#v, %v", secondPage, err)
	}
	lastPage, err := view.PageLocators(ctx, table, secondPage.NextAfterRowID, 1)
	if err != nil || len(lastPage.Locators) != 0 || lastPage.HasMore || lastPage.NextAfterRowID != "" {
		t.Fatalf("filtered future Page() = %#v, %v", lastPage, err)
	}

	newView, err := points.BeginReadView(ctx)
	if err != nil || newView.Sequence() != 31 {
		t.Fatalf("new BeginReadView() sequence = %d, %v", newView.Sequence(), err)
	}
	latest, err := newView.Get(ctx, table, "row_live")
	if err != nil || latest.Revision != 3 || latest.Values["title"] != "future" {
		t.Fatalf("new snapshot Get() = %#v, %v", latest, err)
	}
	inserted, err := newView.Get(ctx, table, "row_new_after")
	if err != nil || inserted.Revision != 1 || inserted.Values["title"] != "new" {
		t.Fatalf("new snapshot inserted Get() = %#v, %v", inserted, err)
	}
}

func TestReadViewOwnWritesMergeIntoPointAndTablePageThenDiscard(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	view, err := points.BeginReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	staged := row.Row{
		ID: "row_live", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 3, State: row.StateLive,
		Values: map[string]any{"col_title": "own write"},
	}
	if err := view.Stage(staged); err != nil {
		t.Fatal(err)
	}
	got, err := view.Get(ctx, table, staged.ID)
	if err != nil || got.Revision != 3 || got.Values["title"] != "own write" {
		t.Fatalf("own update Get() = %#v, %v", got, err)
	}
	staged.State = row.StateDeleted
	if err := view.Stage(staged); err != nil {
		t.Fatal(err)
	}
	if _, err := view.Get(ctx, table, staged.ID); !hasCode(err, "not_found") {
		t.Fatalf("own delete Get() error = %v", err)
	}
	inserted := staged
	inserted.ID, inserted.Revision, inserted.State = "row_middle", 1, row.StateLive
	inserted.Values = map[string]any{"col_title": "own insert"}
	if err := view.Stage(inserted); err != nil {
		t.Fatal(err)
	}
	page, err := view.PageLocators(ctx, table, "", 2)
	if err != nil || len(page.Locators) != 2 ||
		page.Locators[0].RowID != "row_live" || page.Locators[0].State != row.StateDeleted ||
		page.Locators[1].RowID != "row_middle" || page.Locators[1].State != row.StateLive {
		t.Fatalf("own-write Page() = %#v, %v", page, err)
	}
	if err := view.Discard(); err != nil {
		t.Fatal(err)
	}
	if err := view.Stage(inserted); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Stage after Discard error = %v", err)
	}
	fresh, err := points.BeginReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Get(ctx, table, inserted.ID); !hasCode(err, "not_found") {
		t.Fatalf("discard leaked inserted Row: %v", err)
	}
}

func TestReadViewOverlayHasHardBoundAndAllowsReplacement(t *testing.T) {
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	view, err := points.BeginReadView(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for number := 0; number < maxReadViewOverlay; number++ {
		value := row.Row{
			ID: fmt.Sprintf("row_staged_%04d", number), DatabaseID: "db_work", TableID: "tbl_notes",
			SchemaVersion: 1, Revision: 1, State: row.StateLive, Values: map[string]any{},
		}
		if err := view.Stage(value); err != nil {
			t.Fatalf("Stage(%d) error = %v", number, err)
		}
	}
	replacement := row.Row{
		ID: "row_staged_0000", DatabaseID: "db_work", TableID: "tbl_notes",
		SchemaVersion: 1, Revision: 2, State: row.StateDeleted, Values: map[string]any{},
	}
	if err := view.Stage(replacement); err != nil {
		t.Fatalf("replacement Stage error = %v", err)
	}
	overflow := replacement
	overflow.ID = "row_staged_overflow"
	if err := view.Stage(overflow); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overflow Stage error = %v", err)
	}
}

func TestReadViewOverlayConcurrentStagePointAndPageIsRaceFree(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	view, err := points.BeginReadView(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := row.Row{
		ID: "row_live", DatabaseID: table.DatabaseID, TableID: table.ID,
		SchemaVersion: table.SchemaVersion, Revision: 3, State: row.StateLive,
		Values: map[string]any{"col_title": "staged"},
	}
	if err := view.Stage(base); err != nil {
		t.Fatal(err)
	}

	errorsSeen := make(chan error, 600)
	var wait sync.WaitGroup
	wait.Add(3)
	go func() {
		defer wait.Done()
		for revision := uint64(3); revision < 203; revision++ {
			value := base
			value.Revision = revision
			value.Values = map[string]any{"col_title": fmt.Sprintf("staged-%d", revision)}
			if err := view.Stage(value); err != nil {
				errorsSeen <- err
			}
		}
	}()
	go func() {
		defer wait.Done()
		for attempt := 0; attempt < 200; attempt++ {
			value, err := view.Get(ctx, table, base.ID)
			if err != nil || value.ID != base.ID {
				errorsSeen <- fmt.Errorf("concurrent Get = %#v, %w", value, err)
			}
		}
	}()
	go func() {
		defer wait.Done()
		for attempt := 0; attempt < 200; attempt++ {
			page, err := view.PageLocators(ctx, table, "", 3)
			if err != nil || len(page.Locators) == 0 {
				errorsSeen <- fmt.Errorf("concurrent Page = %#v, %w", page, err)
			}
		}
	}()
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
}

func TestReadViewPagesMatchSnapshotReferenceModelAcrossLaterMutations(t *testing.T) {
	ctx := context.Background()
	fixture := createIndexedPointFixture(t)
	t.Cleanup(func() { fixture.close(t) })
	catalogReader, _ := nativecatalog.NewIndexedReader(fixture.catalog, fixture)
	points, _ := NewIndexedReader(New(fixture.file), catalogReader, oneTableRows{index: fixture.current}, fixture.versions)
	table, err := catalogReader.DescribeTable(ctx, "work", "notes")
	if err != nil {
		t.Fatal(err)
	}
	initialCurrent := make([]currentrowindex.Update, 0, 120)
	initialVersions := make([]rowversionindex.Locator, 0, 120)
	expected := make(map[string]rowversionindex.Locator, 122)
	_, baseRows := indexedPointData()
	for _, versions := range baseRows {
		value := versions[len(versions)-1]
		expected[value.ID] = versionLocator(value)
	}
	for number := 0; number < 120; number++ {
		value := row.Row{
			ID: fmt.Sprintf("row_ref_%03d", number), DatabaseID: table.DatabaseID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: uint64(100 + number),
			State: row.StateLive,
		}
		initialCurrent = append(initialCurrent, currentrowindex.Update{Locator: currentLocator(value)})
		locator := versionLocator(value)
		initialVersions = append(initialVersions, locator)
		expected[value.ID] = locator
	}
	if _, err := fixture.current.Apply(3, initialCurrent); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.versions.Append(2, initialVersions); err != nil {
		t.Fatal(err)
	}
	view, err := points.BeginReadView(ctx)
	if err != nil || view.Sequence() != 219 {
		t.Fatalf("reference snapshot = %d, %v", view.Sequence(), err)
	}

	laterCurrent := make([]currentrowindex.Update, 0, 70)
	laterVersions := make([]rowversionindex.Locator, 0, 70)
	for number := 0; number < 120; number += 3 {
		value := row.Row{
			ID: fmt.Sprintf("row_ref_%03d", number), DatabaseID: table.DatabaseID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 2, CommitSequence: uint64(1000 + number),
			State: row.StateLive,
		}
		if number%2 == 0 {
			value.State = row.StateDeleted
		}
		laterCurrent = append(laterCurrent, currentrowindex.Update{ExpectedRevision: 1, Locator: currentLocator(value)})
		laterVersions = append(laterVersions, versionLocator(value))
	}
	for number := 0; number < 30; number++ {
		value := row.Row{
			ID: fmt.Sprintf("row_post_%03d", number), DatabaseID: table.DatabaseID, TableID: table.ID,
			SchemaVersion: table.SchemaVersion, Revision: 1, CommitSequence: uint64(2000 + number),
			State: row.StateLive,
		}
		laterCurrent = append(laterCurrent, currentrowindex.Update{Locator: currentLocator(value)})
		laterVersions = append(laterVersions, versionLocator(value))
	}
	if _, err := fixture.current.Apply(4, laterCurrent); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.versions.Append(3, laterVersions); err != nil {
		t.Fatal(err)
	}

	seen := make(map[string]rowversionindex.Locator, len(expected))
	after := ""
	for pageNumber := 0; pageNumber < 100; pageNumber++ {
		page, err := view.PageLocators(ctx, table, after, 7)
		if err != nil {
			t.Fatalf("PageLocators(%q) error = %v", after, err)
		}
		for _, locator := range page.Locators {
			want, exists := expected[locator.RowID]
			if !exists || locator != want {
				t.Fatalf("visible locator = %+v, want %+v, exists %v", locator, want, exists)
			}
			if _, duplicate := seen[locator.RowID]; duplicate {
				t.Fatalf("duplicate snapshot Row %q", locator.RowID)
			}
			seen[locator.RowID] = locator
		}
		if !page.HasMore {
			break
		}
		if page.NextAfterRowID == "" || page.NextAfterRowID == after {
			t.Fatalf("snapshot cursor did not advance: %#v", page)
		}
		after = page.NextAfterRowID
	}
	if len(seen) != len(expected) {
		t.Fatalf("snapshot visible count = %d, want %d", len(seen), len(expected))
	}
}

func publishLaterRows(t *testing.T, fixture *indexedPointFixture) {
	t.Helper()
	_, rows := indexedPointData()
	updated := rows["row_live"][1]
	updated.Revision = 3
	updated.CommitSequence = 30
	updated.Values = map[string]any{"col_title": "future"}
	inserted := updated
	inserted.ID = "row_new_after"
	inserted.Revision = 1
	inserted.CommitSequence = 31
	inserted.Values = map[string]any{"col_title": "new"}
	repository := New(fixture.file)
	if err := repository.WriteRevision(updated); err != nil {
		t.Fatal(err)
	}
	if err := repository.Write(inserted); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.current.Apply(3, []currentrowindex.Update{
		{ExpectedRevision: 2, Locator: currentLocator(updated)},
		{Locator: currentLocator(inserted)},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.versions.Append(2, []rowversionindex.Locator{
		versionLocator(updated), versionLocator(inserted),
	}); err != nil {
		t.Fatal(err)
	}
}

func hasCode(err error, code string) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == code
}
