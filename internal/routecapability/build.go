package routecapability

import (
	"sort"
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
	"github.com/HW-Yue/Memora/internal/routebenchmarkrunner"
)

func Build(
	corpus routebenchmark.Corpus,
	contract hostcontract.Contract,
	contractDigest string,
	sources []routebenchmarkrunner.Report,
	priceCard *PriceCard,
) (Report, error) {
	if len(sources) == 0 {
		return Report{}, capabilityError(result.CodeValidation, "at least one source report is required")
	}
	if priceCard != nil {
		if err := priceCard.Validate(); err != nil {
			return Report{}, err
		}
	}
	allRuns := []routebenchmarkrunner.RunReport{}
	sourceRefs := make([]SourceReport, 0, len(sources))
	seenRuns, seenReports := map[string]bool{}, map[string]bool{}
	skillDigest, protocolDigest := sources[0].SkillDigest, sources[0].ProtocolDigest
	for index, source := range sources {
		if err := source.ValidateAgainst(corpus, contract, contractDigest); err != nil {
			return Report{}, capabilityError(result.CodeValidation, "source report %d: %v", index, err)
		}
		if source.SkillDigest != skillDigest || source.ProtocolDigest != protocolDigest {
			return Report{}, capabilityError(result.CodeValidation, "source report identity drifted")
		}
		if seenReports[source.Hash] {
			return Report{}, capabilityError(result.CodeValidation, "duplicate source report %s", source.Hash)
		}
		seenReports[source.Hash] = true
		for _, run := range source.Runs {
			if seenRuns[run.RunID] {
				return Report{}, capabilityError(result.CodeValidation, "duplicate source run %s", run.RunID)
			}
			seenRuns[run.RunID] = true
			allRuns = append(allRuns, run)
		}
		sourceRefs = append(sourceRefs, SourceReport{Hash: source.Hash, Runs: uint64(len(source.Runs))})
	}
	sort.Slice(sourceRefs, func(left, right int) bool { return sourceRefs[left].Hash < sourceRefs[right].Hash })
	report := Report{
		Version: ReportVersion, PolicyVersion: PolicyVersion,
		CorpusID: corpus.ID, CorpusDigest: corpus.Hash, ContractDigest: contractDigest,
		SkillDigest: skillDigest, ProtocolDigest: protocolDigest,
		SourceReports: sourceRefs, Buckets: []Bucket{},
	}
	if priceCard != nil {
		report.PriceCardDigest = priceCard.Hash
	}
	report.Buckets = buildBuckets(allRuns, priceCard)
	report.Decision = chooseDefault(allRuns, report.Buckets)
	if err := report.Seal(); err != nil {
		return Report{}, err
	}
	return report, nil
}

type sliceDefinition struct {
	name  string
	value func(routebenchmarkrunner.RunReport) string
}

func buildBuckets(runs []routebenchmarkrunner.RunReport, card *PriceCard) []Bucket {
	slices := []sliceDefinition{
		{"aggregate", func(routebenchmarkrunner.RunReport) string { return "all" }},
		{"fanout", func(run routebenchmarkrunner.RunReport) string { return strconv.Itoa(run.Fanout) }},
		{"depth", func(run routebenchmarkrunner.RunReport) string { return strconv.Itoa(run.Depth) }},
		{"difficulty", func(run routebenchmarkrunner.RunReport) string { return run.Difficulty }},
		{"language", func(run routebenchmarkrunner.RunReport) string { return run.Language }},
		{"host_model", func(run routebenchmarkrunner.RunReport) string { return hostModel(run.Host) }},
	}
	buckets := []Bucket{}
	for _, arm := range routebenchmarkrunner.Arms() {
		for _, definition := range slices {
			values := map[string]bool{}
			for _, run := range runs {
				if run.Arm == arm {
					values[definition.value(run)] = true
				}
			}
			ordered := make([]string, 0, len(values))
			for value := range values {
				ordered = append(ordered, value)
			}
			sort.Strings(ordered)
			for _, value := range ordered {
				selected := []routebenchmarkrunner.RunReport{}
				for _, run := range runs {
					if run.Arm == arm && definition.value(run) == value {
						selected = append(selected, run)
					}
				}
				buckets = append(buckets, summarize(BucketKey{definition.name, value, arm}, selected, card))
			}
		}
	}
	baselines := map[string]Bucket{}
	for _, bucket := range buckets {
		if bucket.Key.Arm == routebenchmarkrunner.ArmRouter {
			baselines[bucket.Key.Slice+"\x00"+bucket.Key.Value] = bucket
		}
	}
	for index := range buckets {
		baseline := baselines[buckets[index].Key.Slice+"\x00"+buckets[index].Key.Value]
		buckets[index].Delta = Delta{
			ModelCallsSavedPerRun: signedMeanDelta(baseline.Counts.ModelCalls, baseline.Counts.Runs,
				buckets[index].Counts.ModelCalls, buckets[index].Counts.Runs),
			TokensSavedPerRun: signedMeanDelta(baseline.Counts.InputTokens+baseline.Counts.OutputTokens,
				baseline.Counts.Runs, buckets[index].Counts.InputTokens+buckets[index].Counts.OutputTokens,
				buckets[index].Counts.Runs),
		}
	}
	return buckets
}

func summarize(key BucketKey, runs []routebenchmarkrunner.RunReport, card *PriceCard) Bucket {
	counts := routebenchmarkrunner.Counts{}
	embedding, cpu, msql, model, end := []uint64{}, []uint64{}, []uint64{}, []uint64{}, []uint64{}
	bucket := Bucket{Key: key}
	allPriced := card != nil
	for _, run := range runs {
		counts.Runs++
		switch run.Observation.Receipt.Status {
		case "completed":
			counts.Completed++
		case "failed":
			counts.Failed++
		case "cancelled":
			counts.Cancelled++
		case "budget_exhausted":
			counts.BudgetExhausted++
		}
		counts.LevelsExpected += run.Score.LevelsExpected
		counts.LevelTop1Correct += run.Score.LevelTop1Correct
		counts.ExactPathSuccess += boolean(run.Score.ExactPathSuccess)
		counts.RowIDSuccess += boolean(run.Score.RowIDSuccess)
		counts.PredictorCases += boolean(run.Score.PredictorEligible)
		counts.PredictorHits += boolean(run.Score.PredictorHit)
		counts.PrefetchCases += boolean(run.Score.PrefetchEligible)
		counts.PrefetchHits += boolean(run.Score.PrefetchHit)
		counts.FallbackCalls += run.Observation.FallbackCalls
		counts.ModelCalls += run.Observation.ModelCalls
		counts.ToolCalls += uint64(len(run.Observation.Receipt.ToolCalls))
		counts.InputTokens += run.Observation.Receipt.Usage.InputTokens
		counts.OutputTokens += run.Observation.Receipt.Usage.OutputTokens
		counts.MispredictTokens += run.Observation.MispredictTokens
		counts.ContextCharacters += run.Observation.Receipt.Usage.ContextCharacters
		counts.IrrelevantLocators += run.Score.IrrelevantLocators
		counts.UnauthorizedLocators += run.Score.UnauthorizedLocators
		counts.WrongFactReads += run.Score.WrongFactReads
		counts.PermissionDenials += run.Observation.PermissionDenials
		counts.Truncations += run.Observation.Truncations
		if failedRun(run) {
			bucket.FailureCells++
		}
		embedding = append(embedding, run.Observation.Timing.QueryEmbeddingMillis)
		cpu = append(cpu, run.Observation.Timing.CPUScanMillis)
		msql = append(msql, run.Observation.Timing.MSQLMillis)
		model = append(model, run.Observation.Timing.ModelMillis)
		end = append(end, run.Observation.Timing.EndToEndMillis)
		bucket.Vector.GenerationBytes = max(bucket.Vector.GenerationBytes, run.Observation.Vector.GenerationBytes)
		bucket.Vector.RebuildMillis = max(bucket.Vector.RebuildMillis, run.Observation.Vector.RebuildMillis)
		bucket.Vector.PeakMemoryBytes = max(bucket.Vector.PeakMemoryBytes, run.Observation.Vector.PeakMemoryBytes)
		if card != nil {
			price, ok := card.lookup(run.Host.Provider, run.Host.Model)
			if !ok {
				allPriced = false
			} else {
				input, okInput := priced(run.Observation.Receipt.Usage.InputTokens, price.InputMicroUSDPerMillion)
				output, okOutput := priced(run.Observation.Receipt.Usage.OutputTokens, price.OutputMicroUSDPerMillion)
				if !okInput || !okOutput || ^uint64(0)-bucket.Cost.InputMicroUSD < input ||
					^uint64(0)-bucket.Cost.OutputMicroUSD < output {
					allPriced = false
				} else {
					bucket.Cost.InputMicroUSD += input
					bucket.Cost.OutputMicroUSD += output
				}
			}
		}
	}
	bucket.Counts, bucket.Metrics = counts, bucketMetrics(counts)
	bucket.Latency = Latency{distribution(embedding), distribution(cpu), distribution(msql), distribution(model), distribution(end)}
	if allPriced && ^uint64(0)-bucket.Cost.InputMicroUSD >= bucket.Cost.OutputMicroUSD {
		bucket.Cost.Available = true
		bucket.Cost.TotalMicroUSD = bucket.Cost.InputMicroUSD + bucket.Cost.OutputMicroUSD
	} else {
		bucket.Cost = Cost{}
	}
	return bucket
}

func chooseDefault(runs []routebenchmarkrunner.RunReport, buckets []Bucket) Decision {
	order := []routebenchmarkrunner.Arm{routebenchmarkrunner.ArmHybridPrefetch,
		routebenchmarkrunner.ArmHybrid, routebenchmarkrunner.ArmVector, routebenchmarkrunner.ArmLexical}
	decision := Decision{PolicyVersion: PolicyVersion, DefaultArm: routebenchmarkrunner.ArmRouter, Arms: []ArmDecision{}}
	byArm := map[routebenchmarkrunner.Arm]map[string]routebenchmarkrunner.RunReport{}
	for _, run := range runs {
		if byArm[run.Arm] == nil {
			byArm[run.Arm] = map[string]routebenchmarkrunner.RunReport{}
		}
		byArm[run.Arm][cellKey(run)] = run
	}
	aggregates := map[routebenchmarkrunner.Arm]Bucket{}
	for _, bucket := range buckets {
		if bucket.Key.Slice == "aggregate" {
			aggregates[bucket.Key.Arm] = bucket
		}
	}
	base := byArm[routebenchmarkrunner.ArmRouter]
	for _, arm := range order {
		reasons := map[string]bool{}
		candidate := byArm[arm]
		if len(candidate) != len(base) {
			reasons["incomplete_matrix"] = true
		}
		for key, baseline := range base {
			run, ok := candidate[key]
			if !ok {
				reasons["incomplete_matrix"] = true
				continue
			}
			if baseline.Score.RowIDSuccess && !run.Score.RowIDSuccess {
				reasons["matched_rowid_regression"] = true
			}
			if !run.Score.Completed {
				reasons["incomplete_cells"] = true
			}
			if run.Score.UnauthorizedLocators > 0 || run.Score.WrongFactReads > 0 ||
				run.Observation.PermissionDenials > 0 || run.Observation.Truncations > 0 {
				reasons["safety_failure"] = true
			}
		}
		baseAggregate, armAggregate := aggregates[routebenchmarkrunner.ArmRouter], aggregates[arm]
		if armAggregate.Counts.ExactPathSuccess < baseAggregate.Counts.ExactPathSuccess {
			reasons["exact_path_regression"] = true
		}
		if baseAggregate.Counts.ModelCalls == 0 || armAggregate.Counts.ModelCalls >= baseAggregate.Counts.ModelCalls ||
			float64(baseAggregate.Counts.ModelCalls-armAggregate.Counts.ModelCalls)/float64(baseAggregate.Counts.ModelCalls) < 0.05 {
			reasons["model_call_savings_below_5_percent"] = true
		}
		if armAggregate.Latency.EndToEnd.P95 > baseAggregate.Latency.EndToEnd.P95 {
			reasons["end_to_end_p95_regression"] = true
		}
		orderedReasons := make([]string, 0, len(reasons))
		for reason := range reasons {
			orderedReasons = append(orderedReasons, reason)
		}
		sort.Strings(orderedReasons)
		eligible := len(orderedReasons) == 0
		decision.Arms = append(decision.Arms, ArmDecision{Arm: arm, Eligible: eligible, Reasons: orderedReasons})
		if decision.DefaultArm == routebenchmarkrunner.ArmRouter && eligible {
			decision.DefaultArm = arm
		}
	}
	return decision
}

func failedRun(run routebenchmarkrunner.RunReport) bool {
	return !run.Score.Completed || !run.Score.ExactPathSuccess || !run.Score.RowIDSuccess ||
		run.Score.UnauthorizedLocators > 0 || run.Score.WrongFactReads > 0 ||
		run.Observation.PermissionDenials > 0 || run.Observation.Truncations > 0
}

func boolean(value bool) uint64 {
	if value {
		return 1
	}
	return 0
}
func signedMeanDelta(baseTotal, baseRuns, currentTotal, currentRuns uint64) float64 {
	if baseRuns == 0 || currentRuns == 0 {
		return 0
	}
	return float64(baseTotal)/float64(baseRuns) - float64(currentTotal)/float64(currentRuns)
}
func hostModel(host hostcontract.HostProfile) string {
	return strings.Join([]string{host.Surface, host.Provider, host.Protocol, host.Model}, "/")
}
func cellKey(run routebenchmarkrunner.RunReport) string {
	return run.ScenarioID + "\x00" + hostModel(run.Host)
}

func (card PriceCard) lookup(provider, model string) (Price, bool) {
	for _, price := range card.Entries {
		if price.Provider == provider && price.Model == model {
			return price, true
		}
	}
	return Price{}, false
}

func priced(tokens, rate uint64) (uint64, bool) {
	if rate != 0 && tokens > ^uint64(0)/rate {
		return 0, false
	}
	return tokens * rate / 1_000_000, true
}

func bucketIdentity(key BucketKey) string {
	return key.Slice + "\x00" + key.Value + "\x00" + string(key.Arm)
}
