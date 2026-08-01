package nativerow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/routevector"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRouteVectorGenerationUsesRealNativeRoutesAndStaleNeverBreaksRouter(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"work", "notes", "title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs: &testIDs{values: []string{"root", "recovery"}}, Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	executeMSQL(t, ctx, engine, "CREATE DATABASE work PURPOSE 'Work' SCOPE 'Engineering'", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(80) NOT NULL PURPOSE 'Title')", executor.Parameters{}, executor.MutationOptions{})
	executeMSQL(t, ctx, engine, "CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})
	executeMSQL(t, ctx, engine, "CREATE ROUTE UNDER 'route_root' NAME 'recovery' KIND 'leaf' PURPOSE 'Recovery procedures'", executor.Parameters{}, executor.MutationOptions{MaxAffectedRows: 1})

	nodes, err := rows.ListRouterNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	request := nativeVectorRequest(t, nodes)
	derivedRoot := t.TempDir()
	vectors, err := routevector.New(derivedRoot, rows, routevector.Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := vectors.Publish(ctx, request)
	if err != nil || len(manifest.Routes) != 2 {
		t.Fatalf("Publish() = %#v, %v", manifest, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	reopenedDictionary := nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{
		IDs: &testIDs{}, Clock: testClock{value: now},
	})
	reopenedRows := NewService(New(reopened), reopenedDictionary, ServiceOptions{
		IDs: &testIDs{}, Clock: testClock{value: now},
	})
	reopenedVectors, _ := routevector.New(derivedRoot, reopenedRows, routevector.Options{})
	if generation, _, err := reopenedVectors.OpenActive(ctx, "db_work"); err != nil ||
		generation.Manifest().GenerationID != "gen_native" {
		t.Fatalf("reopened vector generation = %#v, %v", generation, err)
	}
	reopenedEngine := executor.New(reopenedDictionary, reopenedRows)
	executeMSQL(t, ctx, reopenedEngine,
		"ALTER ROUTE 'route_recovery' SET SYNOPSIS 'changed boundary'",
		executor.Parameters{}, executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1})
	if _, _, err := reopenedVectors.OpenActive(ctx, "db_work"); !errors.Is(err, routevector.ErrStale) {
		t.Fatalf("OpenActive() after Route revision = %v", err)
	}
	routes := executeMSQL(t, ctx, reopenedEngine,
		"SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 8", executor.Parameters{}, executor.MutationOptions{})
	if len(routes.Rows) != 1 || routes.Rows[0]["route_id"] != "route_recovery" {
		t.Fatalf("ordinary Router after stale vector = %#v", routes)
	}
}

func nativeVectorRequest(t *testing.T, nodes []router.Node) routevector.PublishRequest {
	t.Helper()
	vectors := make([]routevector.EncodedVector, 0, 2)
	for _, node := range nodes {
		if node.DatabaseID != "db_work" {
			continue
		}
		surface, err := routevector.EncodeSurface(node)
		if err != nil {
			t.Fatal(err)
		}
		values := []float32{1, 0}
		if node.ID == "route_recovery" {
			values = []float32{0, 1}
		}
		vectors = append(vectors, routevector.EncodedVector{
			RouteID: node.ID, RouteRevision: node.Revision,
			SurfaceSHA256: surface.SHA256, Values: values,
		})
	}
	model := sha256.Sum256([]byte("native-test-model"))
	return routevector.PublishRequest{
		DatabaseID: "db_work", GenerationID: "gen_native",
		Space: routevector.Space{
			ModelID: "native-test", ModelRevision: "v1",
			ModelSHA256: "sha256:" + hex.EncodeToString(model[:]), Dimensions: 2,
			Scalar: routevector.ScalarFloat32, Normalization: routevector.NormalizationL2,
			Similarity: routevector.SimilarityDotProduct,
		},
		Vectors: vectors,
	}
}
