package agent_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestOCREvidenceGateReturnsDeterministicEligibleReport(t *testing.T) {
	t.Parallel()
	corpus := "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	samples := []agent.OCREvidenceSample{
		{Page: 1, TextLayer: false, BaselineCorrect: false, OCRCorrect: true, BaselineLatencyMillis: 20, OCRLatencyMillis: 100},
		{Page: 2, TextLayer: false, BaselineCorrect: false, OCRCorrect: true, BaselineLatencyMillis: 20, OCRLatencyMillis: 100},
		{Page: 3, TextLayer: false, BaselineCorrect: false, OCRCorrect: true, BaselineLatencyMillis: 20, OCRLatencyMillis: 100},
		{Page: 4, TextLayer: true, BaselineCorrect: true, OCRCorrect: true, BaselineLatencyMillis: 20, OCRLatencyMillis: 100},
	}
	config := agent.DefaultOCREvidenceConfig()
	config.MinScannedPages = 3
	config.MinSamples = 4
	config.MinRecallGainBasisPoints = 5_000
	config.MaxAverageLatencyIncreaseMillis = 100
	first, err := agent.EvaluateOCREvidence(corpus, samples, config)
	if err != nil || first.Decision != agent.OCREvidenceEligible || first.RecallGainBasisPoints != 7_500 || first.AverageLatencyIncreaseMillis != 80 || first.Validate() != nil {
		t.Fatalf("first=%#v err=%v validate=%v", first, err, first.Validate())
	}
	second, err := agent.EvaluateOCREvidence(corpus, samples, config)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatalf("second=%#v/%v differs from first=%#v", second, err, first)
	}
}

func TestOCREvidenceGateDefersInsufficientEvidenceAndRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	corpus := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	config := agent.DefaultOCREvidenceConfig()
	config.MinScannedPages = 1
	config.MinSamples = 2
	deferred, err := agent.EvaluateOCREvidence(corpus, []agent.OCREvidenceSample{{Page: 1, TextLayer: false, OCRCorrect: true}}, config)
	if err != nil || deferred.Decision != agent.OCREvidenceDeferred || deferred.Reason != agent.OCREvidenceReasonInsufficientSamples || deferred.Validate() != nil {
		t.Fatalf("deferred=%#v err=%v validate=%v", deferred, err, deferred.Validate())
	}
	tests := []struct {
		name    string
		corpus  string
		samples []agent.OCREvidenceSample
		want    error
	}{
		{name: "bad corpus", corpus: "not-a-digest", samples: nil, want: agent.ErrOCREvidenceInvalid},
		{name: "duplicate page", corpus: corpus, samples: []agent.OCREvidenceSample{{Page: 1}, {Page: 1}}, want: agent.ErrOCREvidenceInvalid},
		{name: "zero page", corpus: corpus, samples: []agent.OCREvidenceSample{{Page: 0}}, want: agent.ErrOCREvidenceInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := agent.EvaluateOCREvidence(test.corpus, test.samples, config); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
	config.MaxSamples = 1
	config.MinSamples = 1
	if _, err := agent.EvaluateOCREvidence(corpus, []agent.OCREvidenceSample{{Page: 1}, {Page: 2}}, config); !errors.Is(err, agent.ErrOCREvidenceBudget) {
		t.Fatalf("budget error = %v", err)
	}
}
