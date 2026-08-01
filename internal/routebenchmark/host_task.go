package routebenchmark

import "github.com/HW-Yue/Memora/internal/hostcontract"

func (corpus Corpus) HostTask(scenarioID string) (hostcontract.Task, error) {
	if err := corpus.Validate(); err != nil {
		return hostcontract.Task{}, err
	}
	for _, scenario := range corpus.Scenarios {
		if scenario.ID != scenarioID {
			continue
		}
		return hostcontract.Task{
			Version: hostcontract.TaskVersion, ID: scenario.ID, Prompt: scenario.Question,
			AuthorizedDatabases: []string{scenario.Database.DatabaseID}, CorpusDigest: corpus.Hash,
			Budget: hostcontract.Budget{
				MaxToolCalls:         uint64(scenario.Limits.MaxToolCalls),
				MaxContextCharacters: uint64(scenario.Limits.MaxContextCharacters),
				MaxElapsedMillis:     uint64(scenario.Limits.MaxElapsedMillis),
			},
		}, nil
	}
	return hostcontract.Task{}, corpusError("scenario %q was not found", scenarioID)
}
