package answerrelease_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerrelease"
)

func TestLoadEvidenceStrictlyPairsScorecardsAndEvaluationsByHash(t *testing.T) {
	evidence := completeReleaseMatrix(t, 0.95)
	scorecards, evaluations := writeReleaseEvidence(t, evidence)
	evaluations[0], evaluations[2] = evaluations[2], evaluations[0]
	loaded, err := answerrelease.LoadEvidence(scorecards, evaluations)
	if err != nil {
		t.Fatal(err)
	}
	want, err := answerrelease.Build(evidence)
	if err != nil {
		t.Fatal(err)
	}
	got, err := answerrelease.Build(loaded)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded release evidence = %#v, %v", got, err)
	}
}

func TestLoadEvidenceRejectsUnsafeMalformedAndMisboundArtifacts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, []string, []string) ([]string, []string)
	}{
		{name: "relative", mutate: func(_ *testing.T, scorecards, evaluations []string) ([]string, []string) {
			scorecards[0] = filepath.Base(scorecards[0])
			return scorecards, evaluations
		}},
		{name: "unknown field", mutate: func(t *testing.T, scorecards, evaluations []string) ([]string, []string) {
			encoded, err := os.ReadFile(scorecards[0])
			if err != nil {
				t.Fatal(err)
			}
			encoded = []byte(strings.TrimSpace(string(encoded)))
			encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
			if err := os.WriteFile(scorecards[0], encoded, 0o600); err != nil {
				t.Fatal(err)
			}
			return scorecards, evaluations
		}},
		{name: "trailing JSON", mutate: func(t *testing.T, scorecards, evaluations []string) ([]string, []string) {
			file, err := os.OpenFile(evaluations[0], os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString(`{}`); err != nil {
				t.Fatal(err)
			}
			_ = file.Close()
			return scorecards, evaluations
		}},
		{name: "symlink", mutate: func(t *testing.T, scorecards, evaluations []string) ([]string, []string) {
			link := filepath.Join(t.TempDir(), "scorecard-link.json")
			if err := os.Symlink(scorecards[0], link); err != nil {
				t.Fatal(err)
			}
			scorecards[0] = link
			return scorecards, evaluations
		}},
		{name: "missing evaluation", mutate: func(_ *testing.T, scorecards, evaluations []string) ([]string, []string) {
			return scorecards, evaluations[:2]
		}},
		{name: "misbound evaluation", mutate: func(t *testing.T, scorecards, evaluations []string) ([]string, []string) {
			return scorecards, append([]string(nil), evaluations[0], evaluations[0], evaluations[2])
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scorecards, evaluations := writeReleaseEvidence(t, completeReleaseMatrix(t, 0.95))
			scorecards, evaluations = test.mutate(t, scorecards, evaluations)
			if _, err := answerrelease.LoadEvidence(scorecards, evaluations); !errors.Is(err, answerrelease.ErrInvalidArtifacts) {
				t.Fatalf("LoadEvidence() error = %v", err)
			}
		})
	}
}

func TestPublishAndLoadReleaseReportAtomicallyWithoutOverwrite(t *testing.T) {
	report, err := answerrelease.Build(completeReleaseMatrix(t, 0.95))
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(t.TempDir(), "query-release.json")
	if err := answerrelease.PublishReport(output, report); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("published mode = %v, %v", info.Mode(), err)
	}
	loaded, err := answerrelease.LoadReport(output)
	if err != nil || !reflect.DeepEqual(loaded, report) {
		t.Fatalf("LoadReport() = %#v, %v", loaded, err)
	}
	if err := answerrelease.PublishReport(output, report); !errors.Is(err, answerrelease.ErrOutputExists) {
		t.Fatalf("second PublishReport() = %v", err)
	}
	if err := answerrelease.PublishReport("relative.json", report); !errors.Is(err, answerrelease.ErrInvalidOutput) {
		t.Fatalf("relative PublishReport() = %v", err)
	}

	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	tampered := filepath.Join(t.TempDir(), "tampered.json")
	unknown := append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	if err := os.WriteFile(tampered, unknown, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := answerrelease.LoadReport(tampered); !errors.Is(err, answerrelease.ErrInvalidArtifacts) {
		t.Fatalf("LoadReport(tampered) = %v", err)
	}
}

func writeReleaseEvidence(t *testing.T, evidence []answerrelease.Evidence) ([]string, []string) {
	t.Helper()
	directory := t.TempDir()
	scorecards, evaluations := make([]string, len(evidence)), make([]string, len(evidence))
	for index, item := range evidence {
		scorecards[index] = filepath.Join(directory, item.Scorecard.ArmID+"-scorecard.json")
		evaluations[index] = filepath.Join(directory, item.Scorecard.ArmID+"-evaluation.json")
		writeReleaseJSON(t, scorecards[index], item.Scorecard)
		writeReleaseJSON(t, evaluations[index], item.Evaluation)
	}
	return scorecards, evaluations
}

func writeReleaseJSON(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
