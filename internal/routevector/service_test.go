package routevector

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
)

func TestPublishOpenReopenBindsSpaceSurfaceRevisionAndBytes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeRouteSource{snapshots: [][]router.Node{testRoutes()}}
	root := t.TempDir()
	service, err := New(root, source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := testRequest(t, "gen_first", 0, testRoutes())
	manifest, marker, err := service.Publish(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if marker.Revision != 1 || marker.GenerationID != request.GenerationID ||
		manifest.Space.Dimensions != 2 || manifest.SpaceDigest == "" ||
		manifest.SourceSHA256 == "" || manifest.VectorsSHA256 == "" || len(manifest.Routes) != 2 {
		t.Fatalf("Publish() = %#v, %#v", manifest, marker)
	}
	generation, openedMarker, err := service.OpenActive(ctx, "db_work")
	if err != nil || openedMarker != marker || generation.Manifest().GenerationID != "gen_first" {
		t.Fatalf("OpenActive() = %#v, %#v, %v", generation, openedMarker, err)
	}
	values, ok := generation.Vector("route_alpha")
	if !ok || !reflect.DeepEqual(values, []float32{1, 0}) {
		t.Fatalf("Vector(route_alpha) = %#v, %v", values, ok)
	}
	values[0] = 0
	again, _ := generation.Vector("route_alpha")
	if again[0] != 1 {
		t.Fatal("Generation returned mutable vector storage")
	}

	reopened, err := New(root, source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	reopenedGeneration, _, err := reopened.OpenActive(ctx, "db_work")
	if err != nil || !reflect.DeepEqual(reopenedGeneration.Manifest(), manifest) {
		t.Fatalf("reopened generation = %#v, %v", reopenedGeneration, err)
	}
	encoded, err := os.ReadFile(filepath.Join(generationPath(root, "db_work", "gen_first"), vectorsFileName))
	if err != nil {
		t.Fatal(err)
	}
	wantBytes := []byte{
		0x00, 0x00, 0x80, 0x3f, 0x00, 0x00, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x80, 0x3f,
	}
	if strings.Contains(string(encoded), "Recovery procedures") || !reflect.DeepEqual(encoded, wantBytes) {
		t.Fatalf("vector bytes leaked surface or have wrong size: %d", len(encoded))
	}
}

func TestPublishRejectsInvalidSpaceVectorsCoverageAndSurfaceReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	routes := testRoutes()
	tests := map[string]func(*PublishRequest){
		"model digest":    func(request *PublishRequest) { request.Space.ModelSHA256 = "bad" },
		"dimension":       func(request *PublishRequest) { request.Space.Dimensions = 3 },
		"nan":             func(request *PublishRequest) { request.Vectors[0].Values[0] = float32(math.NaN()) },
		"infinity":        func(request *PublishRequest) { request.Vectors[0].Values[0] = float32(math.Inf(1)) },
		"not normalized":  func(request *PublishRequest) { request.Vectors[0].Values = []float32{2, 0} },
		"surface digest":  func(request *PublishRequest) { request.Vectors[0].SurfaceSHA256 = sha256Value("wrong") },
		"revision":        func(request *PublishRequest) { request.Vectors[0].RouteRevision++ },
		"missing route":   func(request *PublishRequest) { request.Vectors = request.Vectors[:1] },
		"duplicate route": func(request *PublishRequest) { request.Vectors[1] = request.Vectors[0] },
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service, err := New(t.TempDir(), &fakeRouteSource{snapshots: [][]router.Node{routes}}, Options{})
			if err != nil {
				t.Fatal(err)
			}
			request := testRequest(t, "gen_invalid", 0, routes)
			mutate(&request)
			if _, _, err := service.Publish(ctx, request); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Publish() error = %v", err)
			}
			if _, _, err := service.OpenActive(ctx, "db_work"); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("OpenActive() error = %v", err)
			}
		})
	}
}

func TestRouteChangePreventsPublishAndMakesOldGenerationStale(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := testRoutes()
	changed := testRoutes()
	changed[1].Revision++
	changed[1].Purpose = "Changed recovery boundary"

	changing := &fakeRouteSource{snapshots: [][]router.Node{base, changed}}
	service, _ := New(t.TempDir(), changing, Options{})
	if _, _, err := service.Publish(ctx, testRequest(t, "gen_changed", 0, base)); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("Publish() error = %v", err)
	}
	if _, _, err := service.OpenActive(ctx, "db_work"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("active after changed build = %v", err)
	}

	stable := &fakeRouteSource{snapshots: [][]router.Node{base}}
	root := t.TempDir()
	service, _ = New(root, stable, Options{})
	if _, _, err := service.Publish(ctx, testRequest(t, "gen_stable", 0, base)); err != nil {
		t.Fatal(err)
	}
	stable.Set(changed)
	if _, _, err := service.OpenActive(ctx, "db_work"); !errors.Is(err, ErrStale) {
		t.Fatalf("OpenActive() stale error = %v", err)
	}
	listed, err := stable.ListRouterNodes(ctx)
	if err != nil || listed[1].Revision != changed[1].Revision {
		t.Fatalf("ordinary Router source changed = %#v, %v", listed, err)
	}
}

func TestPublishFaultMatrixKeepsOldMarkerOrReportsOutcomeUnknown(t *testing.T) {
	t.Parallel()
	preMarker := []FaultPoint{
		FaultBeforeStageWrite, FaultBeforeStageSync, FaultBeforeGenerationRename,
		FaultBeforeGenerationsSync, FaultAfterGenerationRename,
		FaultBeforeMarkerWrite, FaultBeforeMarkerSync, FaultBeforeMarkerRename,
	}
	for _, point := range preMarker {
		point := point
		t.Run(string(point), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			routes := testRoutes()
			source := &fakeRouteSource{snapshots: [][]router.Node{routes}}
			root := t.TempDir()
			baseline, _ := New(root, source, Options{})
			if _, _, err := baseline.Publish(ctx, testRequest(t, "gen_old", 0, routes)); err != nil {
				t.Fatal(err)
			}
			faulted, _ := New(root, source, Options{Fault: failAt(point)})
			if _, _, err := faulted.Publish(ctx, testRequest(t, "gen_new", 1, routes)); err == nil || errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Publish() error = %v", err)
			}
			reopened, _ := New(root, source, Options{})
			generation, marker, err := reopened.OpenActive(ctx, "db_work")
			if err != nil || marker.GenerationID != "gen_old" || generation.Manifest().GenerationID != "gen_old" {
				t.Fatalf("old marker changed = %#v, %#v, %v", generation, marker, err)
			}
		})
	}
	for _, point := range []FaultPoint{FaultAfterMarkerRename, FaultBeforeParentSync} {
		point := point
		t.Run(string(point), func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			routes := testRoutes()
			source := &fakeRouteSource{snapshots: [][]router.Node{routes}}
			root := t.TempDir()
			baseline, _ := New(root, source, Options{})
			if _, _, err := baseline.Publish(ctx, testRequest(t, "gen_old", 0, routes)); err != nil {
				t.Fatal(err)
			}
			faulted, _ := New(root, source, Options{Fault: failAt(point)})
			request := testRequest(t, "gen_new", 1, routes)
			if _, _, err := faulted.Publish(ctx, request); !errors.Is(err, ErrOutcomeUnknown) {
				t.Fatalf("Publish() error = %v", err)
			}
			reopened, _ := New(root, source, Options{})
			generation, marker, err := reopened.OpenActive(ctx, "db_work")
			if err != nil || marker.GenerationID != "gen_new" || generation.Manifest().GenerationID != "gen_new" {
				t.Fatalf("marker did not decide outcome = %#v, %#v, %v", generation, marker, err)
			}
			if _, retried, err := reopened.Publish(ctx, request); err != nil || retried != marker {
				t.Fatalf("idempotent retry = %#v, %v", retried, err)
			}
		})
	}
}

func TestOpenRejectsMarkerManifestAndVectorCorruption(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	routes := testRoutes()
	tests := map[string]func(string){
		"marker traversal": func(root string) {
			path := markerPath(root, "db_work")
			marker := readMarkerForTest(t, path)
			marker.GenerationID = "../escape"
			writeJSONForTest(t, path, marker)
		},
		"manifest unknown": func(root string) {
			path := filepath.Join(generationPath(root, "db_work", "gen_corrupt"), manifestFileName)
			encoded, _ := os.ReadFile(path)
			encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"vector bit flip": func(root string) {
			path := filepath.Join(generationPath(root, "db_work", "gen_corrupt"), vectorsFileName)
			encoded, _ := os.ReadFile(path)
			encoded[0] ^= 0xff
			if err := os.WriteFile(path, encoded, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, corrupt := range tests {
		name, corrupt := name, corrupt
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			source := &fakeRouteSource{snapshots: [][]router.Node{routes}}
			service, _ := New(root, source, Options{})
			if _, _, err := service.Publish(ctx, testRequest(t, "gen_corrupt", 0, routes)); err != nil {
				t.Fatal(err)
			}
			corrupt(root)
			reopened, _ := New(root, source, Options{})
			if _, _, err := reopened.OpenActive(ctx, "db_work"); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("OpenActive() error = %v", err)
			}
		})
	}
}

func TestConcurrentPublishConflictsAndReclaimKeepsActiveAndRetained(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	routes := testRoutes()
	source := &fakeRouteSource{snapshots: [][]router.Node{routes}}
	root := t.TempDir()
	service, _ := New(root, source, Options{})
	if _, _, err := service.Publish(ctx, testRequest(t, "gen_one", 0, routes)); err != nil {
		t.Fatal(err)
	}
	requests := []PublishRequest{
		testRequest(t, "gen_two", 1, routes), testRequest(t, "gen_three", 1, routes),
	}
	errorsFound := make(chan error, 2)
	var wait sync.WaitGroup
	for _, request := range requests {
		request := request
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := service.Publish(ctx, request)
			errorsFound <- err
		}()
	}
	wait.Wait()
	close(errorsFound)
	successes, conflicts := 0, 0
	for err := range errorsFound {
		if err == nil {
			successes++
		} else if errors.Is(err, ErrRevisionConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent Publish() error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("publish outcomes = %d successes, %d conflicts", successes, conflicts)
	}
	_, marker, err := service.OpenActive(ctx, "db_work")
	if err != nil {
		t.Fatal(err)
	}
	retained := "gen_two"
	if marker.GenerationID == retained {
		retained = "gen_three"
	}
	removed, err := service.Reclaim(ctx, "db_work", []string{retained})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(removed)
	if !containsString(removed, "gen_one") || containsString(removed, marker.GenerationID) ||
		containsString(removed, retained) {
		t.Fatalf("Reclaim() removed = %#v, active = %s, retained = %s", removed, marker.GenerationID, retained)
	}
}

func TestRouteVectorContractsHaveNoFactPayload(t *testing.T) {
	t.Parallel()
	requestType := reflect.TypeOf(PublishRequest{})
	for _, forbidden := range []string{"Rows", "History", "Memberships", "Chunks", "Documents", "SurfaceText"} {
		if _, exists := requestType.FieldByName(forbidden); exists {
			t.Fatalf("PublishRequest contains fact field %q", forbidden)
		}
	}
}

type fakeRouteSource struct {
	mu        sync.Mutex
	snapshots [][]router.Node
	calls     int
}

func (source *fakeRouteSource) ListRouterNodes(context.Context) ([]router.Node, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	index := source.calls
	if index >= len(source.snapshots) {
		index = len(source.snapshots) - 1
	}
	source.calls++
	return cloneRoutes(source.snapshots[index]), nil
}

func (source *fakeRouteSource) Set(nodes []router.Node) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.snapshots = [][]router.Node{cloneRoutes(nodes)}
	source.calls = 0
}

func testRoutes() []router.Node {
	return []router.Node{
		{Version: router.Version, ID: "route_alpha", DatabaseID: "db_work", TableID: "tbl_notes",
			Name: "root", Aliases: []string{}, Path: "/", Kind: router.KindRoot,
			Purpose: "Notes semantic root", Revision: 1},
		{Version: router.Version, ID: "route_recovery", DatabaseID: "db_work", TableID: "tbl_notes",
			ParentID: "route_alpha", Name: "recovery", Aliases: []string{"crash"}, Path: "/recovery",
			Kind: router.KindLeaf, Purpose: "Recovery procedures", Synopsis: "Committed redo only", Revision: 2},
		{Version: router.Version, ID: "route_private", DatabaseID: "db_private", TableID: "tbl_secret",
			Name: "private", Aliases: []string{}, Path: "/", Kind: router.KindRoot,
			Purpose: "Private", Revision: 1},
	}
}

func testRequest(t *testing.T, generationID string, revision uint64, routes []router.Node) PublishRequest {
	t.Helper()
	values := map[string][]float32{"route_alpha": {1, 0}, "route_recovery": {0, 1}}
	encoded := make([]EncodedVector, 0, 2)
	for _, node := range routes {
		if node.DatabaseID != "db_work" || node.Deleted {
			continue
		}
		surface, err := EncodeSurface(node)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, EncodedVector{
			RouteID: node.ID, RouteRevision: node.Revision, SurfaceSHA256: surface.SHA256,
			Values: append([]float32(nil), values[node.ID]...),
		})
	}
	return PublishRequest{
		DatabaseID: "db_work", GenerationID: generationID, ExpectedRevision: revision,
		Space: Space{
			ModelID: "mini-route-encoder", ModelRevision: "v1", ModelSHA256: sha256Value("model"),
			Dimensions: 2, Scalar: ScalarFloat32, Normalization: NormalizationL2,
			Similarity: SimilarityDotProduct,
		},
		Vectors: encoded,
	}
}

func failAt(target FaultPoint) func(FaultPoint) error {
	return func(point FaultPoint) error {
		if point == target {
			return errors.New("injected fault")
		}
		return nil
	}
}

func readMarkerForTest(t *testing.T, path string) Marker {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var marker Marker
	if err := json.Unmarshal(encoded, &marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

func writeJSONForTest(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}

func cloneRoutes(values []router.Node) []router.Node {
	result := make([]router.Node, len(values))
	for index, value := range values {
		result[index] = value
		result[index].Aliases = append([]string(nil), value.Aliases...)
	}
	return result
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// TestOpenActiveReusesTheOpenedGenerationUntilItIsRepublished pins the cache
// behind OpenActive. Every query used to re-read and re-verify every Route
// vector in the Generation; a published Generation is immutable and named by
// its manifest digest, so there is nothing to invalidate — only a marker to
// compare.
func TestOpenActiveReusesTheOpenedGenerationUntilItIsRepublished(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	source := &fakeRouteSource{snapshots: [][]router.Node{testRoutes(), testRoutes(), testRoutes()}}
	service, err := New(t.TempDir(), source, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.Publish(ctx, testRequest(t, "gen_first", 0, testRoutes())); err != nil {
		t.Fatal(err)
	}
	first, _, err := service.OpenActive(ctx, "db_work")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.OpenActive(ctx, "db_work")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("OpenActive re-read the Generation instead of reusing it")
	}

	if _, _, err := service.Publish(ctx, testRequest(t, "gen_second", 1, testRoutes())); err != nil {
		t.Fatal(err)
	}
	republished, marker, err := service.OpenActive(ctx, "db_work")
	if err != nil {
		t.Fatal(err)
	}
	if republished == first {
		t.Fatal("OpenActive served the superseded Generation")
	}
	if marker.GenerationID != "gen_second" || republished.Manifest().GenerationID != "gen_second" {
		t.Fatalf("republished generation = %#v, marker = %#v", republished.Manifest(), marker)
	}
}
