package routeexact_test

import (
	"context"
	"math"
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"github.com/HW-Yue/Memora/internal/routeexact"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/routevector"
)

func TestSearchMatchesReferenceAndFiltersTableScopeBeforeDotProducts(t *testing.T) {
	t.Parallel()
	nodes := []router.Node{
		node("route_c", "table_denied", 1),
		node("route_b", "table_allowed", 1),
		node("route_a", "table_allowed", 1),
	}
	active, space := generation(t, nodes, map[string][]float32{
		"route_a": {1, 0, 0}, "route_b": {0.8, 0.6, 0}, "route_c": {1, 0, 0},
	})
	query := []float32{1, 0, 0}
	original := append([]float32(nil), query...)
	result, err := routeexact.Search(routeexact.Query{
		SpaceDigest: space, Vector: query, Limit: 2,
	}, []routeexact.Scope{{
		DatabaseID: "db_work", Generation: active,
		AllowedTableIDs: map[string]struct{}{"table_allowed": {}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Scanned != 2 || len(result.Matches) != 2 ||
		result.Matches[0].RouteID != "route_a" || result.Matches[1].RouteID != "route_b" ||
		math.Abs(result.Matches[0].Score-1) > 1e-7 || math.Abs(result.Matches[1].Score-0.8) > 1e-6 {
		t.Fatalf("exact result = %#v", result)
	}
	if !reflect.DeepEqual(query, original) {
		t.Fatalf("query mutated: %v", query)
	}

	// Equal scores are ordered by stable Route ID, independent of manifest order.
	tied, _ := generation(t, []router.Node{node("route_z", "table_allowed", 1), node("route_y", "table_allowed", 1)}, map[string][]float32{
		"route_z": {1, 0, 0}, "route_y": {1, 0, 0},
	})
	ties, err := routeexact.Search(routeexact.Query{SpaceDigest: space, Vector: query, Limit: 2}, []routeexact.Scope{{
		DatabaseID: "db_work", Generation: tied, AllowedTableIDs: map[string]struct{}{"table_allowed": {}},
	}})
	if err != nil || ties.Matches[0].RouteID != "route_y" || ties.Matches[1].RouteID != "route_z" {
		t.Fatalf("ties = %#v, %v", ties, err)
	}
}

func TestSearchMatchesIndependentRandomReference(t *testing.T) {
	t.Parallel()
	random := rand.New(rand.NewSource(124))
	nodes := make([]router.Node, 80)
	vectors := make(map[string][]float32, len(nodes))
	for index := range nodes {
		id := routeID(index)
		table := "table_allowed"
		if index%5 == 0 {
			table = "table_denied"
		}
		nodes[index] = node(id, table, uint64(index+1))
		vectors[id] = normalizedRandom(random, 8)
	}
	active, space := generation(t, nodes, vectors)
	query := normalizedRandom(random, 8)
	result, err := routeexact.Search(routeexact.Query{SpaceDigest: space, Vector: query, Limit: 13}, []routeexact.Scope{{
		DatabaseID: "db_work", Generation: active,
		AllowedTableIDs: map[string]struct{}{"table_allowed": {}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	type expected struct {
		id    string
		score float64
	}
	want := make([]expected, 0)
	for _, value := range nodes {
		if value.TableID != "table_allowed" {
			continue
		}
		var score float64
		for component := range query {
			score += float64(query[component]) * float64(vectors[value.ID][component])
		}
		want = append(want, expected{id: value.ID, score: score})
	}
	sort.Slice(want, func(i, j int) bool {
		if want[i].score != want[j].score {
			return want[i].score > want[j].score
		}
		return want[i].id < want[j].id
	})
	want = want[:13]
	if result.Scanned != 64 || len(result.Matches) != len(want) {
		t.Fatalf("result size = %#v", result)
	}
	for index := range want {
		if result.Matches[index].RouteID != want[index].id || math.Abs(result.Matches[index].Score-want[index].score) > 1e-12 {
			t.Fatalf("match %d = %#v, want %#v", index, result.Matches[index], want[index])
		}
	}
}

func TestSearchRejectsInvalidQuerySpaceAndScope(t *testing.T) {
	t.Parallel()
	active, space := generation(t, []router.Node{node("route_a", "table_a", 1)}, map[string][]float32{"route_a": {1, 0, 0}})
	scope := []routeexact.Scope{{DatabaseID: "db_work", Generation: active, AllowedTableIDs: map[string]struct{}{"table_a": {}}}}
	invalid := []routeexact.Query{
		{SpaceDigest: space, Vector: []float32{1, 0}, Limit: 1},
		{SpaceDigest: space, Vector: []float32{2, 0, 0}, Limit: 1},
		{SpaceDigest: space, Vector: []float32{float32(math.NaN()), 0, 0}, Limit: 1},
		{SpaceDigest: space, Vector: []float32{float32(math.Inf(1)), 0, 0}, Limit: 1},
		{SpaceDigest: "wrong", Vector: []float32{1, 0, 0}, Limit: 1},
		{SpaceDigest: space, Vector: []float32{1, 0, 0}, Limit: 0},
	}
	for _, query := range invalid {
		if _, err := routeexact.Search(query, scope); err == nil {
			t.Fatalf("Search(%#v) succeeded", query)
		}
	}
	if _, err := routeexact.Search(routeexact.Query{SpaceDigest: space, Vector: []float32{1, 0, 0}, Limit: 1}, []routeexact.Scope{{
		DatabaseID: "db_other", Generation: active, AllowedTableIDs: map[string]struct{}{"table_a": {}},
	}}); err == nil {
		t.Fatal("mismatched Database scope succeeded")
	}
}

func TestSnapshotBindsSortedGenerationReceiptsWithoutMutatingInput(t *testing.T) {
	t.Parallel()
	base := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	space := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	receipts := []routeexact.GenerationReceipt{
		{DatabaseID: "db_z", GenerationID: "gen_z", MarkerRevision: 2, ManifestSHA256: base},
		{DatabaseID: "db_a", GenerationID: "gen_a", MarkerRevision: 1, ManifestSHA256: space},
	}
	original := append([]routeexact.GenerationReceipt(nil), receipts...)
	first, err := routeexact.Snapshot(base, space, receipts)
	if err != nil {
		t.Fatal(err)
	}
	second, err := routeexact.Snapshot(base, space, []routeexact.GenerationReceipt{receipts[1], receipts[0]})
	if err != nil || second != first {
		t.Fatalf("snapshot order = %q, %q, %v", first, second, err)
	}
	changed := append([]routeexact.GenerationReceipt(nil), receipts...)
	changed[0].MarkerRevision++
	third, err := routeexact.Snapshot(base, space, changed)
	if err != nil || third == first {
		t.Fatalf("changed generation snapshot = %q, %v", third, err)
	}
	if !reflect.DeepEqual(receipts, original) {
		t.Fatalf("receipts mutated: %#v", receipts)
	}
}

type source []router.Node

func (value source) ListRouterNodes(context.Context) ([]router.Node, error) {
	result := make([]router.Node, len(value))
	copy(result, value)
	return result, nil
}

func generation(t testing.TB, nodes []router.Node, vectors map[string][]float32) (*routevector.Generation, string) {
	t.Helper()
	service, err := routevector.New(t.TempDir(), source(nodes), routevector.Options{})
	if err != nil {
		t.Fatal(err)
	}
	space := routevector.Space{
		ModelID: "test-route-encoder", ModelRevision: "1",
		ModelSHA256: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		Dimensions:  3, Scalar: routevector.ScalarFloat32,
		Normalization: routevector.NormalizationL2, Similarity: routevector.SimilarityDotProduct,
	}
	if len(vectors) > 0 {
		for _, value := range vectors {
			space.Dimensions = uint32(len(value))
			break
		}
	}
	encoded := make([]routevector.EncodedVector, 0, len(nodes))
	for _, value := range nodes {
		surface, err := routevector.EncodeSurface(value)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, routevector.EncodedVector{
			RouteID: value.ID, RouteRevision: value.Revision, SurfaceSHA256: surface.SHA256,
			Values: append([]float32(nil), vectors[value.ID]...),
		})
	}
	manifest, _, err := service.Publish(context.Background(), routevector.PublishRequest{
		DatabaseID: "db_work", GenerationID: "gen_test", Space: space, Vectors: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	generation, _, err := service.OpenActive(context.Background(), "db_work")
	if err != nil {
		t.Fatal(err)
	}
	return generation, manifest.SpaceDigest
}

func node(id, table string, revision uint64) router.Node {
	return router.Node{Version: router.Version, ID: id, DatabaseID: "db_work", TableID: table,
		Name: id, Path: "/" + id, Kind: router.KindLeaf, Purpose: "Route " + id, Revision: revision}
}

func normalizedRandom(random *rand.Rand, dimensions int) []float32 {
	values := make([]float32, dimensions)
	var norm float64
	for index := range values {
		values[index] = random.Float32()*2 - 1
		norm += float64(values[index]) * float64(values[index])
	}
	scale := float32(1 / math.Sqrt(norm))
	for index := range values {
		values[index] *= scale
	}
	return values
}

func routeID(index int) string {
	const digits = "0123456789"
	return "route_" + string([]byte{digits[(index/10)%10], digits[index%10]})
}
