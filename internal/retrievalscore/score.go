package retrievalscore

import (
	"errors"
	"sort"
)

type scoreAccumulator struct {
	counts    Counts
	recallSum float64
	recipSum  float64
}

type scoredRun struct {
	score  RunScore
	groups map[string]*scoreAccumulator
}

func Evaluate(suite Suite, runs []Run, baselineRun string, k int) (Report, error) {
	if err := suite.Validate(); err != nil {
		return Report{}, err
	}
	if k <= 0 || k > 10_000 || !validText(baselineRun) || len(runs) == 0 {
		return Report{}, errors.New("retrieval evaluation K, baseline, and runs are required")
	}
	positives := positiveDocuments(suite)
	scored := make([]scoredRun, 0, len(runs))
	seenRuns := make(map[string]struct{}, len(runs))
	baselineIndex := -1
	for _, run := range runs {
		if err := run.ValidateAgainst(suite); err != nil {
			return Report{}, err
		}
		if _, duplicate := seenRuns[run.RunID]; duplicate {
			return Report{}, errors.New("retrieval evaluation run identity is duplicated")
		}
		seenRuns[run.RunID] = struct{}{}
		if run.RunID == baselineRun {
			baselineIndex = len(scored)
		}
		scored = append(scored, scoreRun(suite, run, positives, k))
	}
	if baselineIndex < 0 {
		return Report{}, errors.New("retrieval evaluation baseline run is missing")
	}
	baseline := scored[baselineIndex]
	report := Report{Version: ReportVersion, SuiteID: suite.SuiteID, SuiteHash: suite.Hash, K: k, BaselineRun: baselineRun,
		Runs: make([]RunScore, 0, len(scored))}
	for index := range scored {
		applyReduction(&scored[index].score.Metrics, baseline.score.Counts, scored[index].score.Counts)
		for bucketIndex := range scored[index].score.Buckets {
			bucket := &scored[index].score.Buckets[bucketIndex]
			baselineBucket := baseline.groups[bucket.Group]
			applyReduction(&bucket.Metrics, baselineBucket.counts, bucket.Counts)
		}
		report.Runs = append(report.Runs, scored[index].score)
	}
	if err := report.seal(); err != nil {
		return Report{}, err
	}
	return report, nil
}

func positiveDocuments(suite Suite) map[string]map[string]struct{} {
	positive := make(map[string]map[string]struct{}, len(suite.Queries))
	for _, query := range suite.Queries {
		positive[query.QueryID] = make(map[string]struct{})
	}
	for _, qrel := range suite.Qrels {
		if qrel.Relevance > 0 {
			positive[qrel.QueryID][qrel.DocumentID] = struct{}{}
		}
	}
	return positive
}

func scoreRun(suite Suite, run Run, positives map[string]map[string]struct{}, k int) scoredRun {
	overall := &scoreAccumulator{}
	groups := make(map[string]*scoreAccumulator)
	for index, query := range suite.Queries {
		group := groups[query.Group]
		if group == nil {
			group = &scoreAccumulator{}
			groups[query.Group] = group
		}
		result := run.Results[index]
		for _, accumulator := range []*scoreAccumulator{overall, group} {
			accumulator.counts.Queries++
			accumulator.counts.Relevant += uint64(len(positives[query.QueryID]))
			addCost(&accumulator.counts, result)
			if result.Status == "completed" {
				accumulator.counts.Completed++
			} else {
				accumulator.counts.Failed++
			}
		}
		if result.Status != "completed" {
			continue
		}
		recalled := 0
		firstRank := 0
		for candidateIndex, documentID := range result.CandidateDocumentIDs {
			if _, relevant := positives[query.QueryID][documentID]; !relevant {
				continue
			}
			if firstRank == 0 {
				firstRank = candidateIndex + 1
			}
			if candidateIndex < k {
				recalled++
			}
		}
		recall := float64(recalled) / float64(len(positives[query.QueryID]))
		reciprocal := 0.0
		if firstRank > 0 {
			reciprocal = 1 / float64(firstRank)
		}
		for _, accumulator := range []*scoreAccumulator{overall, group} {
			accumulator.counts.RecalledAtK += uint64(recalled)
			if recalled > 0 {
				accumulator.counts.HitsAtK++
			}
			accumulator.recallSum += recall
			accumulator.recipSum += reciprocal
		}
	}
	groupsOrdered := make([]string, 0, len(groups))
	for group := range groups {
		groupsOrdered = append(groupsOrdered, group)
	}
	sort.Strings(groupsOrdered)
	result := scoredRun{score: RunScore{RunID: run.RunID, Arm: run.Arm, Counts: overall.counts, Metrics: metrics(*overall),
		Buckets: make([]Bucket, 0, len(groupsOrdered))}, groups: groups}
	for _, group := range groupsOrdered {
		accumulator := groups[group]
		result.score.Buckets = append(result.score.Buckets, Bucket{Group: group, Counts: accumulator.counts, Metrics: metrics(*accumulator)})
	}
	return result
}

func addCost(counts *Counts, result Result) {
	counts.InputTokens += result.InputTokens
	counts.OutputTokens += result.OutputTokens
	counts.ToolCalls += result.ToolCalls
	counts.ModelCalls += result.ModelCalls
	counts.ContextCharacters += result.ContextCharacters
	counts.EndToEndMicroseconds += result.EndToEndMicroseconds
}

func metrics(accumulator scoreAccumulator) Metrics {
	queries := float64(accumulator.counts.Queries)
	if queries == 0 {
		return Metrics{}
	}
	return Metrics{Coverage: float64(accumulator.counts.Completed) / queries,
		RecallAtK: accumulator.recallSum / queries, HitRateAtK: float64(accumulator.counts.HitsAtK) / queries,
		MRR: accumulator.recipSum / queries, MeanInputTokens: float64(accumulator.counts.InputTokens) / queries,
		MeanToolCalls: float64(accumulator.counts.ToolCalls) / queries}
}

func applyReduction(metrics *Metrics, baseline, current Counts) {
	metrics.InputTokenReduction = reduction(baseline.InputTokens, current.InputTokens)
	metrics.ToolCallReduction = reduction(baseline.ToolCalls, current.ToolCalls)
}

func reduction(baseline, current uint64) *float64 {
	if baseline == 0 {
		return nil
	}
	value := (float64(baseline) - float64(current)) / float64(baseline)
	return &value
}
