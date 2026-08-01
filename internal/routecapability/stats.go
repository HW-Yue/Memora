package routecapability

import (
	"math"
	"sort"

	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
)

func proportion(numerator, denominator uint64) Proportion {
	result := Proportion{Numerator: numerator, Denominator: denominator}
	if denominator == 0 {
		return result
	}
	n, total, z := float64(numerator), float64(denominator), 1.959963984540054
	value := n / total
	denominatorWilson := 1 + z*z/total
	center := (value + z*z/(2*total)) / denominatorWilson
	margin := z * math.Sqrt(value*(1-value)/total+z*z/(4*total*total)) / denominatorWilson
	result.Value = value
	result.Lower95 = math.Max(0, center-margin)
	result.Upper95 = math.Min(1, center+margin)
	return result
}

func distribution(values []uint64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	values = append([]uint64(nil), values...)
	sort.Slice(values, func(left, right int) bool { return values[left] < values[right] })
	return Distribution{P50: percentile(values, 0.50), P95: percentile(values, 0.95)}
}

func percentile(sorted []uint64, quantile float64) uint64 {
	position := int(math.Ceil(quantile*float64(len(sorted)))) - 1
	if position < 0 {
		position = 0
	}
	return sorted[position]
}

func bucketMetrics(counts routebenchmarkrunner.Counts) Metrics {
	return Metrics{
		LevelTop1:       proportion(counts.LevelTop1Correct, counts.LevelsExpected),
		ExactPath:       proportion(counts.ExactPathSuccess, counts.Runs),
		RowIDSuccess:    proportion(counts.RowIDSuccess, counts.Runs),
		PredictorRecall: proportion(counts.PredictorHits, counts.PredictorCases),
		PrefetchHit:     proportion(counts.PrefetchHits, counts.PrefetchCases),
	}
}
