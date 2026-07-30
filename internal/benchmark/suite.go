package benchmark

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

func Load(path string) (Suite, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Suite{}, benchmarkError(result.CodeInternal, "read suite: %v", err)
	}
	var suite Suite
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return Suite{}, benchmarkError(result.CodeValidation, "decode suite: %v", err)
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (suite Suite) Validate() error {
	if suite.Version != SuiteVersion || strings.TrimSpace(suite.ID) == "" || len(suite.Scenarios) == 0 {
		return benchmarkError(result.CodeValidation, "suite version, ID, and scenarios are required")
	}
	seenScenarios := map[string]bool{}
	for index, scenario := range suite.Scenarios {
		if strings.TrimSpace(scenario.ID) == "" || seenScenarios[scenario.ID] || len(scenario.Hosts) == 0 || len(scenario.Turns) == 0 {
			return benchmarkError(result.CodeValidation, "scenario %d requires unique identity, hosts, and turns", index)
		}
		seenScenarios[scenario.ID] = true
		switch scenario.Kind {
		case KindMultiProject, KindRevision, KindAssimilation, KindColdStart, KindHostSwitch:
		default:
			return benchmarkError(result.CodeValidation, "scenario %q has unsupported kind %q", scenario.ID, scenario.Kind)
		}
		seenTurns := map[string]bool{}
		for turnIndex, turn := range scenario.Turns {
			if strings.TrimSpace(turn.ID) == "" || seenTurns[turn.ID] || strings.TrimSpace(turn.Intent) == "" || strings.TrimSpace(turn.Expected) == "" {
				return benchmarkError(result.CodeValidation, "scenario %q turn %d is incomplete or duplicated", scenario.ID, turnIndex)
			}
			seenTurns[turn.ID] = true
		}
	}
	return nil
}
