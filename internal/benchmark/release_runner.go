package benchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/result"
)

type observedAdapter struct {
	name     string
	outcomes map[string]Outcome
	evidence Evidence
}

func (adapter *observedAdapter) Name() string { return adapter.name }
func (adapter *observedAdapter) Evidence() Evidence {
	return adapter.evidence
}
func (adapter *observedAdapter) Run(_ context.Context, scenario Scenario) (Outcome, error) {
	outcome, exists := adapter.outcomes[scenario.ID]
	if !exists {
		return Outcome{}, benchmarkError(result.CodeNotFound, "observed adapter %q has no scenario %q", adapter.name, scenario.ID)
	}
	return outcome, nil
}

type rankedRecord struct {
	ID    string
	Score float64
}

type releaseExecution struct {
	outcomes map[string]Outcome
	digest   string
}

func executeReleaseAdapter(name string, suite Suite, corpus ReleaseCorpus, snapshot ModelSnapshot) (releaseExecution, error) {
	scenarios := map[string]bool{}
	for _, scenario := range suite.Scenarios {
		scenarios[scenario.ID] = true
	}
	for _, record := range corpus.Records {
		if !scenarios[record.ScenarioID] {
			return releaseExecution{}, benchmarkError(result.CodeValidation, "release record %q references unknown scenario", record.ID)
		}
	}
	for _, query := range corpus.Queries {
		if !scenarios[query.ScenarioID] {
			return releaseExecution{}, benchmarkError(result.CodeValidation, "release query %q references unknown scenario", query.ID)
		}
	}
	modeledRecords := map[string]ModeledRecord{}
	for _, record := range snapshot.Records {
		modeledRecords[record.ID] = record
	}
	modeledQueries := map[string]ModeledQuery{}
	for _, query := range snapshot.Queries {
		modeledQueries[query.ID] = query
	}
	selected := map[string]bool{}
	for _, record := range corpus.Records {
		switch name {
		case "no-memory":
		case "markdown-search", "vector":
			selected[record.ID] = true
		case "memora":
			selected[record.ID] = modeledRecords[record.ID].Durable
		default:
			return releaseExecution{}, benchmarkError(result.CodeValidation, "unknown release adapter %q", name)
		}
	}
	superseded := map[string]bool{}
	for _, record := range corpus.Records {
		if record.Supersedes != "" && selected[record.ID] {
			superseded[record.Supersedes] = true
		}
	}
	counts := map[string]*Counts{}
	evidenceRows := make([]string, 0, len(corpus.Records)+len(corpus.Queries))
	for _, scenario := range suite.Scenarios {
		counts[scenario.ID] = &Counts{}
	}
	for _, record := range corpus.Records {
		current := counts[record.ScenarioID]
		if record.Durable {
			current.WritesExpected++
		}
		if selected[record.ID] {
			current.WritesAttempted++
			if record.Durable {
				current.WritesCorrect++
			}
		}
		evidenceRows = append(evidenceRows, name+"|record|"+record.ID+"|"+boolString(selected[record.ID]))
	}
	for _, query := range corpus.Queries {
		current := counts[query.ScenarioID]
		current.RetrievalQueries++
		current.AnsweredTurns++
		if query.Takeover {
			current.Takeovers++
		}
		ranked := rankReleaseRecords(name, corpus, query, selected, superseded, modeledRecords, modeledQueries)
		if len(ranked) > 5 {
			ranked = ranked[:5]
		}
		relevant := map[string]bool{}
		for _, id := range query.RelevantIDs {
			relevant[id] = true
		}
		firstRelevant := 0
		relevantReturned := 0
		for index, rankedRecord := range ranked {
			record := releaseRecordByID(corpus, rankedRecord.ID)
			current.ContextCharacters += uint64(utf8.RuneCountInString(record.Text))
			current.SelectedRows++
			if relevant[record.ID] {
				relevantReturned++
				if firstRelevant == 0 {
					firstRelevant = index + 1
				}
			} else {
				current.IrrelevantRows++
			}
		}
		if firstRelevant > 0 {
			current.RetrievalHitsAt5++
			current.ReciprocalRankMilli += uint64(math.Round(1000 / float64(firstRelevant)))
			if query.Takeover {
				current.TakeoversSucceeded++
			}
		}
		current.NDCGMilli += ndcgMilli(ranked, relevant)
		current.ToolCalls++
		evidenceRows = append(evidenceRows, name+"|query|"+query.ID+"|"+rankedIDs(ranked)+"|"+itoa(relevantReturned))
	}
	outcomes := make(map[string]Outcome, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		outcomes[scenario.ID] = Outcome{HostResultsEquivalent: true, Counts: *counts[scenario.ID]}
	}
	sort.Strings(evidenceRows)
	sum := sha256.Sum256([]byte(strings.Join(evidenceRows, "\n")))
	return releaseExecution{outcomes: outcomes, digest: hex.EncodeToString(sum[:])}, nil
}

func rankReleaseRecords(
	name string,
	corpus ReleaseCorpus,
	query ReleaseQuery,
	selected, superseded map[string]bool,
	modeledRecords map[string]ModeledRecord,
	modeledQueries map[string]ModeledQuery,
) []rankedRecord {
	results := []rankedRecord{}
	for _, record := range corpus.Records {
		if !selected[record.ID] {
			continue
		}
		if name == "memora" && record.Project != query.Project {
			continue
		}
		if name == "memora" && superseded[record.ID] && !historyQuery(query.Text) {
			continue
		}
		score := 0.0
		switch name {
		case "no-memory":
			continue
		case "markdown-search":
			score = overlapScore(lexicalVector(query.Text, 2), lexicalVector(record.Text, 2))
		case "vector":
			score = cosine(charNGramVector(query.Text, 3), charNGramVector(record.Text, 3))
		case "memora":
			queryText := query.Text + " " + strings.Join(modeledQueries[query.ID].Terms, " ")
			recordText := record.Text + " " + strings.Join(modeledRecords[record.ID].Terms, " ")
			score = 0.75*cosine(charNGramVector(queryText, 2), charNGramVector(recordText, 2)) +
				0.25*overlapScore(lexicalVector(queryText, 1), lexicalVector(recordText, 1))
		}
		if score > 0 {
			results = append(results, rankedRecord{ID: record.ID, Score: score})
		}
	}
	sort.Slice(results, func(left, right int) bool {
		if results[left].Score != results[right].Score {
			return results[left].Score > results[right].Score
		}
		return results[left].ID < results[right].ID
	})
	return results
}

func lexicalVector(text string, hanNGram int) map[string]float64 {
	vector := map[string]float64{}
	var word []rune
	flush := func() {
		if len(word) > 0 {
			vector[string(word)]++
			word = word[:0]
		}
	}
	var han []rune
	flushHan := func() {
		if len(han) == 0 {
			return
		}
		size := hanNGram
		if size < 1 {
			size = 1
		}
		if len(han) < size {
			vector[string(han)]++
		} else {
			for index := 0; index+size <= len(han); index++ {
				vector[string(han[index:index+size])]++
			}
		}
		han = han[:0]
	}
	for _, value := range []rune(strings.ToLower(text)) {
		switch {
		case unicode.Is(unicode.Han, value):
			flush()
			han = append(han, value)
		case unicode.IsLetter(value) || unicode.IsDigit(value):
			flushHan()
			word = append(word, value)
		default:
			flush()
			flushHan()
		}
	}
	flush()
	flushHan()
	return vector
}

func charNGramVector(text string, size int) map[string]float64 {
	normalized := []rune(strings.ToLower(strings.Map(func(value rune) rune {
		if unicode.IsLetter(value) || unicode.IsDigit(value) {
			return value
		}
		return -1
	}, text)))
	vector := map[string]float64{}
	if len(normalized) < size {
		if len(normalized) > 0 {
			vector[string(normalized)] = 1
		}
		return vector
	}
	for index := 0; index+size <= len(normalized); index++ {
		vector[string(normalized[index:index+size])]++
	}
	return vector
}

func overlapScore(left, right map[string]float64) float64 {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	matches := 0.0
	for term := range left {
		if right[term] > 0 {
			matches++
		}
	}
	return matches / math.Sqrt(float64(len(left)*len(right)))
}

func cosine(left, right map[string]float64) float64 {
	dot, leftNorm, rightNorm := 0.0, 0.0, 0.0
	for term, weight := range left {
		dot += weight * right[term]
		leftNorm += weight * weight
	}
	for _, weight := range right {
		rightNorm += weight * weight
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / math.Sqrt(leftNorm*rightNorm)
}

func ndcgMilli(ranked []rankedRecord, relevant map[string]bool) uint64 {
	dcg := 0.0
	for index, record := range ranked {
		if relevant[record.ID] {
			dcg += 1 / math.Log2(float64(index+2))
		}
	}
	ideal := 0.0
	for index := 0; index < len(relevant) && index < 5; index++ {
		ideal += 1 / math.Log2(float64(index+2))
	}
	if ideal == 0 {
		return 0
	}
	return uint64(math.Round(1000 * dcg / ideal))
}

func historyQuery(text string) bool {
	for _, marker := range []string{"历史", "原来", "被取代", "previous", "history", "superseded"} {
		if strings.Contains(strings.ToLower(text), marker) {
			return true
		}
	}
	return false
}

func releaseRecordByID(corpus ReleaseCorpus, id string) ReleaseRecord {
	for _, record := range corpus.Records {
		if record.ID == id {
			return record
		}
	}
	return ReleaseRecord{}
}

func rankedIDs(records []rankedRecord) string {
	ids := make([]string, 0, len(records))
	for _, record := range records {
		ids = append(ids, record.ID)
	}
	return strings.Join(ids, ",")
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	const digits = "0123456789"
	buffer := [20]byte{}
	index := len(buffer)
	for value > 0 {
		index--
		buffer[index] = digits[value%10]
		value /= 10
	}
	return string(buffer[index:])
}
