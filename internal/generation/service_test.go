package generation_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/generation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	sqlitestore "github.com/HW-Yue/Memora/internal/store/sqlite"
)

func TestIndependentGenerationPublishPinsConsistentManifestAndDefersGC(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := generation.New(databaseStore)
	stageValidated(t, ctx, service, "db_work", generation.KindRouter, "gen_router_1", 10)
	stageValidated(t, ctx, service, "db_work", generation.KindAgent, "gen_agent_1", 10)
	stageValidated(t, ctx, service, "db_work", generation.KindMechanical, "gen_mechanical_1", 10)
	manifest, err := service.Publish(ctx, "db_work", 0, map[generation.Kind]string{
		generation.KindRouter:     "gen_router_1",
		generation.KindAgent:      "gen_agent_1",
		generation.KindMechanical: "gen_mechanical_1",
	})
	if err != nil || manifest.Revision != 1 {
		t.Fatalf("initial Publish() = %#v, %v", manifest, err)
	}

	pin, err := service.Pin(ctx, "db_work")
	if err != nil {
		t.Fatal(err)
	}
	stageValidated(t, ctx, service, "db_work", generation.KindAgent, "gen_agent_2", 20)
	published, err := service.Publish(ctx, "db_work", 1, map[generation.Kind]string{
		generation.KindAgent: "gen_agent_2",
	})
	if err != nil || published.Revision != 2 ||
		published.Active[generation.KindRouter].ID != "gen_router_1" ||
		published.Active[generation.KindAgent].ID != "gen_agent_2" ||
		published.Active[generation.KindMechanical].ID != "gen_mechanical_1" {
		t.Fatalf("independent Publish() = %#v, %v", published, err)
	}
	if pin.Manifest().Revision != 1 ||
		pin.Manifest().Active[generation.KindAgent].ID != "gen_agent_1" {
		t.Fatalf("pinned manifest changed = %#v", pin.Manifest())
	}
	collected, err := service.GC(ctx, "db_work")
	if err != nil || len(collected) != 0 {
		t.Fatalf("GC while pinned = %#v, %v", collected, err)
	}
	pin.Release()
	collected, err = service.GC(ctx, "db_work")
	if err != nil || len(collected) != 1 || collected[0].ID != "gen_agent_1" {
		t.Fatalf("GC after release = %#v, %v", collected, err)
	}
	if _, err := service.Get(ctx, "db_work", generation.KindAgent, "gen_agent_1"); err == nil {
		t.Fatal("collected generation remains readable")
	}
}

func TestManifestPublishIsCrashAtomicBeforeAndAfterCommit(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.db")
	databaseStore, err := sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service := generation.New(databaseStore)
	for kind, id := range map[generation.Kind]string{
		generation.KindRouter: "gen_router_1", generation.KindAgent: "gen_agent_1",
		generation.KindMechanical: "gen_mechanical_1",
	} {
		stageValidated(t, ctx, service, "db_work", kind, id, 10)
	}
	if _, err := service.Publish(ctx, "db_work", 0, map[generation.Kind]string{
		generation.KindRouter: "gen_router_1", generation.KindAgent: "gen_agent_1",
		generation.KindMechanical: "gen_mechanical_1",
	}); err != nil {
		t.Fatal(err)
	}
	stageValidated(t, ctx, service, "db_work", generation.KindRouter, "gen_router_2", 20)

	tx, err := databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishIn(ctx, tx, "db_work", 1, map[generation.Kind]string{
		generation.KindRouter: "gen_router_2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}
	databaseStore, err = sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	service = generation.New(databaseStore)
	pin, err := service.Pin(ctx, "db_work")
	if err != nil || pin.Manifest().Revision != 1 ||
		pin.Manifest().Active[generation.KindRouter].ID != "gen_router_1" {
		t.Fatalf("manifest after rollback = %#v, %v", pin.Manifest(), err)
	}
	pin.Release()

	tx, err = databaseStore.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.PublishIn(ctx, tx, "db_work", 1, map[generation.Kind]string{
		generation.KindRouter: "gen_router_2",
	}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := databaseStore.Close(); err != nil {
		t.Fatal(err)
	}
	databaseStore, err = sqlitestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service = generation.New(databaseStore)
	pin, err = service.Pin(ctx, "db_work")
	if err != nil || pin.Manifest().Revision != 2 ||
		pin.Manifest().Active[generation.KindRouter].ID != "gen_router_2" ||
		pin.Manifest().Active[generation.KindAgent].ID != "gen_agent_1" {
		t.Fatalf("manifest after commit = %#v, %v", pin.Manifest(), err)
	}
	pin.Release()
}

func TestGenerationValidationRejectsBadChecksumIncompleteAndStalePublish(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	databaseStore, err := sqlitestore.Open(filepath.Join(t.TempDir(), "database.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer databaseStore.Close()
	service := generation.New(databaseStore)
	if _, err := service.Stage(ctx, generation.Definition{
		DatabaseID: "db_work", Kind: generation.KindRouter, ID: "gen_router_1",
		CoveredCommitSequence: 10, Checksum: "sha256:router",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Validate(
		ctx, "db_work", generation.KindRouter, "gen_router_1", "sha256:wrong",
	)
	assertCode(t, err, result.CodeConstraint)
	if _, err := service.Validate(
		ctx, "db_work", generation.KindRouter, "gen_router_1", "sha256:router",
	); err != nil {
		t.Fatal(err)
	}
	_, err = service.Publish(ctx, "db_work", 0, map[generation.Kind]string{
		generation.KindRouter: "gen_router_1",
	})
	assertCode(t, err, result.CodeValidation)

	stageValidated(t, ctx, service, "db_work", generation.KindAgent, "gen_agent_1", 10)
	stageValidated(t, ctx, service, "db_work", generation.KindMechanical, "gen_mechanical_1", 10)
	if _, err := service.Publish(ctx, "db_work", 0, map[generation.Kind]string{
		generation.KindRouter: "gen_router_1", generation.KindAgent: "gen_agent_1",
		generation.KindMechanical: "gen_mechanical_1",
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Publish(ctx, "db_work", 0, map[generation.Kind]string{
		generation.KindRouter: "gen_router_1",
	})
	assertCode(t, err, result.CodeRevisionConflict)
}

func stageValidated(
	t *testing.T,
	ctx context.Context,
	service *generation.Service,
	databaseID string,
	kind generation.Kind,
	id string,
	covered uint64,
) {
	t.Helper()
	if _, err := service.Stage(ctx, generation.Definition{
		DatabaseID: databaseID, Kind: kind, ID: id,
		CoveredCommitSequence: covered, Checksum: "sha256:" + id,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Validate(ctx, databaseID, kind, id, "sha256:"+id); err != nil {
		t.Fatal(err)
	}
}

func assertCode(t *testing.T, err error, want result.Code) {
	t.Helper()
	var stable interface{ StableCode() string }
	if !errors.As(err, &stable) {
		t.Fatalf("error = %v has no stable code, want %q", err, want)
	}
	if got := stable.StableCode(); got != string(want) {
		t.Fatalf("error = %v, code = %q, want %q", err, got, want)
	}
}
