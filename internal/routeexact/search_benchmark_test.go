package routeexact_test

import (
	"fmt"
	"math/rand"
	"sort"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/routeexact"
	"github.com/HW-Yue/Memora/internal/router"
)

var benchmarkResult routeexact.Result

func BenchmarkRouteExactResourceGate(b *testing.B) {
	for _, routeCount := range []int{4_368, 17_472} {
		b.Run(fmt.Sprintf("routes=%d/dimensions=384", routeCount), func(b *testing.B) {
			random := rand.New(rand.NewSource(162))
			nodes := make([]router.Node, routeCount)
			vectors := make(map[string][]float32, routeCount)
			for index := range nodes {
				id := fmt.Sprintf("route_%06d", index)
				nodes[index] = node(id, "table_allowed", uint64(index+1))
				vectors[id] = normalizedRandom(random, 384)
			}
			active, space := generation(b, nodes, vectors)
			query := normalizedRandom(random, 384)
			scopes := []routeexact.Scope{{
				DatabaseID: "db_work", Generation: active,
				AllowedTableIDs: map[string]struct{}{"table_allowed": {}},
			}}
			durations := make([]time.Duration, 0, b.N)
			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				started := time.Now()
				result, err := routeexact.Search(routeexact.Query{
					SpaceDigest: space, Vector: query, Limit: 16,
				}, scopes)
				durations = append(durations, time.Since(started))
				if err != nil || result.Scanned != uint64(routeCount) || len(result.Matches) != 16 {
					b.Fatalf("Search() scanned=%d matches=%d err=%v", result.Scanned, len(result.Matches), err)
				}
				benchmarkResult = result
			}
			b.StopTimer()
			sort.Slice(durations, func(left, right int) bool { return durations[left] < durations[right] })
			p95 := durations[(len(durations)-1)*95/100]
			b.ReportMetric(float64(p95.Nanoseconds()), "p95-ns/query")
		})
	}
}
