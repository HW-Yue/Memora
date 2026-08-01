package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/routevector"
	"github.com/HW-Yue/Memora/internal/security"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestVectorRouteCandidatesUseAuthorizedNativeGenerationsAndDegradeWhenStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"work", "notes", "title", "private", "secrets", "secret_title"}}, Clock: testClock{value: now},
	})
	rows := NewService(New(file), dictionary, ServiceOptions{
		IDs: &testIDs{values: []string{"work_root", "work_match", "private_root", "private_match"}}, Clock: testClock{value: now},
	})
	engine := executor.New(dictionary, rows)
	for _, statement := range []string{
		"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Engineering'",
		"CREATE TABLE work.notes PURPOSE 'Notes' ROW SEMANTICS 'One note' (title TEXT(80) NOT NULL PURPOSE 'Title')",
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Work Router'",
		"CREATE ROUTE UNDER 'route_work_root' NAME 'recovery' KIND 'leaf' PURPOSE 'Recovery procedures'",
		"CREATE DATABASE private PURPOSE 'Private' SCOPE 'Secrets'",
		"CREATE TABLE private.secrets PURPOSE 'Secrets' ROW SEMANTICS 'One secret' (title TEXT(80) NOT NULL PURPOSE 'Title')",
		"CREATE ROUTE ROOT FOR TABLE private.secrets PURPOSE 'Private Router'",
		"CREATE ROUTE UNDER 'route_private_root' NAME 'private-recovery' KIND 'leaf' PURPOSE 'Private recovery procedures'",
	} {
		options := executor.MutationOptions{}
		if statement[0:6] == "CREATE" && (len(statement) > 12 && statement[7:12] == "ROUTE") {
			options.MaxAffectedRows = 1
		}
		executeMSQL(t, ctx, engine, statement, executor.Parameters{}, options)
	}

	derivedRoot := t.TempDir()
	vectors, err := routevector.New(derivedRoot, rows, routevector.Options{})
	if err != nil {
		t.Fatal(err)
	}
	nodes, err := rows.ListRouterNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	workManifest, _, err := vectors.Publish(ctx, vectorRequestForDatabase(t, nodes, "db_work", "gen_work"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := vectors.Publish(ctx, vectorRequestForDatabase(t, nodes, "db_private", "gen_private")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	dictionary = nativecatalog.NewService(nativecatalog.New(reopened), nativecatalog.ServiceOptions{
		IDs: &testIDs{}, Clock: testClock{value: now},
	})
	rows = NewService(New(reopened), dictionary, ServiceOptions{IDs: &testIDs{}, Clock: testClock{value: now}})
	engine = executor.New(dictionary, rows)
	vectors, err = routevector.New(derivedRoot, rows, routevector.Options{})
	if err != nil {
		t.Fatal(err)
	}

	authorized := security.WithAuthorization(ctx, security.Authorization{
		Version: security.AuthorizationVersion, Actor: "agent:test", AuthorizedDatabases: []string{"work"},
	})
	recording := &recordingRouteVectors{RouteVectorReader: vectors}
	vectorEngine := executor.NewWithRouteVectors(dictionary, rows, recording)
	parameters := executor.Parameters{Named: map[string]any{
		"query": []any{1.0, 0.0}, "space": workManifest.SpaceDigest,
		"limit": int64(1), "bytes": int64(4096),
	}}
	output, err := runMSQL(authorized, vectorEngine,
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query SPACE :space LIMIT :limit BYTES :bytes",
		parameters, executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Rows) != 0 || output.Discovery == nil || len(output.Discovery.Candidates) != 1 ||
		output.Discovery.Candidates[0].DatabaseID != "db_work" ||
		output.Discovery.Candidates[0].RouteID != "route_work_match" ||
		output.Discovery.Candidates[0].Predictor != "vector-route-exact/v1" ||
		output.Discovery.Candidates[0].ScoreKind != discovery.ScoreDotProduct {
		t.Fatalf("authorized vector discovery = %#v", output)
	}
	if len(recording.opened) != 1 || recording.opened[0] != "db_work" {
		t.Fatalf("generation opens before authorization filter = %v", recording.opened)
	}

	wrongSpace := parameters
	wrongSpace.Named = map[string]any{
		"query": []any{1.0, 0.0},
		"space": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"limit": int64(1), "bytes": int64(4096),
	}
	unavailable, err := runMSQL(authorized, vectorEngine,
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query SPACE :space LIMIT :limit BYTES :bytes",
		wrongSpace, executor.MutationOptions{})
	if err != nil || unavailable.Discovery == nil || len(unavailable.Discovery.Candidates) != 0 ||
		unavailable.Discovery.Predictors[0].Status != discovery.PredictorUnavailable {
		t.Fatalf("incompatible generation fallback = %#v, %v", unavailable, err)
	}
	for _, invalid := range []executor.Parameters{
		{Named: map[string]any{"query": []any{1.0}, "space": workManifest.SpaceDigest, "limit": int64(1), "bytes": int64(4096)}},
		{Named: map[string]any{"query": []any{2.0, 0.0}, "space": workManifest.SpaceDigest, "limit": int64(1), "bytes": int64(4096)}},
		{Named: map[string]any{"query": []any{"bad", 0.0}, "space": workManifest.SpaceDigest, "limit": int64(1), "bytes": int64(4096)}},
		{Named: map[string]any{"query": []any{1.0, 0.0}, "space": workManifest.SpaceDigest, "limit": int64(0), "bytes": int64(4096)}},
	} {
		_, err := runMSQL(authorized, vectorEngine,
			"SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query SPACE :space LIMIT :limit BYTES :bytes",
			invalid, executor.MutationOptions{})
		if !hasCode(err, string(result.CodeValidation)) {
			t.Fatalf("invalid vector parameters error = %v", err)
		}
	}

	executeMSQL(t, ctx, engine, "ALTER ROUTE 'route_work_match' SET SYNOPSIS 'changed'",
		executor.Parameters{}, executor.MutationOptions{ExpectedRevision: 1, MaxAffectedRows: 1})
	stale, err := runMSQL(authorized, vectorEngine,
		"SHOW ROUTE CANDIDATES FROM ALL TABLES USING VECTOR :query SPACE :space LIMIT :limit BYTES :bytes",
		parameters, executor.MutationOptions{})
	if err != nil || stale.Discovery == nil || stale.Discovery.Predictors[0].Status != discovery.PredictorUnavailable {
		t.Fatalf("stale generation fallback = %#v, %v", stale, err)
	}
}

type recordingRouteVectors struct {
	executor.RouteVectorReader
	opened []string
}

func (reader *recordingRouteVectors) OpenActive(
	ctx context.Context, databaseID string,
) (*routevector.Generation, routevector.Marker, error) {
	reader.opened = append(reader.opened, databaseID)
	return reader.RouteVectorReader.OpenActive(ctx, databaseID)
}

func vectorRequestForDatabase(t *testing.T, nodes []router.Node, databaseID, generationID string) routevector.PublishRequest {
	t.Helper()
	values := make([]routevector.EncodedVector, 0)
	for _, node := range nodes {
		if node.DatabaseID != databaseID {
			continue
		}
		surface, err := routevector.EncodeSurface(node)
		if err != nil {
			t.Fatal(err)
		}
		vector := []float32{0, 1}
		if node.Kind == router.KindLeaf {
			vector = []float32{1, 0}
		}
		values = append(values, routevector.EncodedVector{
			RouteID: node.ID, RouteRevision: node.Revision, SurfaceSHA256: surface.SHA256, Values: vector,
		})
	}
	return routevector.PublishRequest{
		DatabaseID: databaseID, GenerationID: generationID,
		Space: routevector.Space{
			ModelID: "native-vector-query-test", ModelRevision: "v1",
			ModelSHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			Dimensions:  2, Scalar: routevector.ScalarFloat32,
			Normalization: routevector.NormalizationL2, Similarity: routevector.SimilarityDotProduct,
		},
		Vectors: values,
	}
}
