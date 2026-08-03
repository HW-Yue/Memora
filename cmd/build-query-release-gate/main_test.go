package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answerbenchmark"
	"github.com/HW-Yue/Memora/internal/answerrelease"
)

func TestCommandBuildsAndPublishesPassedReleaseGate(t *testing.T) {
	directory := t.TempDir()
	scorecards, evaluations := commandPaths(directory)
	output := filepath.Join(directory, "release.json")
	report := answerrelease.Report{
		Status: answerrelease.StatusPassed, DefaultArm: answerbenchmark.ArmAtlasOnly,
		Hash: "sha256:test-report",
	}
	loadCalls, publishCalls := 0, 0
	dependencies := commandDependencies{
		load: func(gotScorecards, gotEvaluations []string) ([]answerrelease.Evidence, error) {
			loadCalls++
			if strings.Join(gotScorecards, "|") != strings.Join(scorecards, "|") ||
				strings.Join(gotEvaluations, "|") != strings.Join(evaluations, "|") {
				t.Fatalf("paths = %#v / %#v", gotScorecards, gotEvaluations)
			}
			return []answerrelease.Evidence{{}}, nil
		},
		build: func([]answerrelease.Evidence) (answerrelease.Report, error) { return report, nil },
		publish: func(path string, got answerrelease.Report) error {
			publishCalls++
			if path != output || !reflect.DeepEqual(got, report) {
				t.Fatalf("publish = %q, %#v", path, got)
			}
			return nil
		},
	}
	var stdout, stderr bytes.Buffer
	exit := run(commandArguments(scorecards, evaluations, output), &stdout, &stderr, dependencies)
	if exit != 0 || loadCalls != 1 || publishCalls != 1 || stderr.Len() != 0 ||
		!strings.Contains(stdout.String(), "status=passed") || !strings.Contains(stdout.String(), answerbenchmark.ArmAtlasOnly) {
		t.Fatalf("exit=%d calls=%d/%d stdout=%q stderr=%q", exit, loadCalls, publishCalls, stdout.String(), stderr.String())
	}
}

func TestCommandPublishesIncompleteReportAndFailsGate(t *testing.T) {
	directory := t.TempDir()
	scorecards, evaluations := commandPaths(directory)
	output := filepath.Join(directory, "release.json")
	published := false
	report := answerrelease.Report{Status: answerrelease.StatusIncomplete, Hash: "sha256:incomplete"}
	dependencies := commandDependencies{
		load:    func([]string, []string) ([]answerrelease.Evidence, error) { return []answerrelease.Evidence{{}}, nil },
		build:   func([]answerrelease.Evidence) (answerrelease.Report, error) { return report, nil },
		publish: func(string, answerrelease.Report) error { published = true; return nil },
	}
	var stdout, stderr bytes.Buffer
	exit := run(commandArguments(scorecards, evaluations, output), &stdout, &stderr, dependencies)
	if exit != 1 || !published || stderr.Len() != 0 || !strings.Contains(stdout.String(), "status=incomplete") {
		t.Fatalf("exit=%d published=%t stdout=%q stderr=%q", exit, published, stdout.String(), stderr.String())
	}
}

func TestCommandRejectsInvalidUsageAndDependencyFailures(t *testing.T) {
	directory := t.TempDir()
	scorecards, evaluations := commandPaths(directory)
	output := filepath.Join(directory, "release.json")
	loadCalls := 0
	dependencies := commandDependencies{
		load: func([]string, []string) ([]answerrelease.Evidence, error) {
			loadCalls++
			return nil, errors.New("private loader detail")
		},
		build: answerrelease.Build, publish: answerrelease.PublishReport,
	}
	for _, arguments := range [][]string{
		{},
		{"--scorecard", "relative.json", "--output", output},
		append(commandArguments(scorecards[:2], evaluations, output), "unexpected"),
	} {
		var stdout, stderr bytes.Buffer
		if exit := run(arguments, &stdout, &stderr, dependencies); exit != 2 {
			t.Fatalf("run(%#v) exit=%d stderr=%q", arguments, exit, stderr.String())
		}
	}
	if loadCalls != 0 {
		t.Fatalf("invalid usage called loader %d times", loadCalls)
	}
	var stdout, stderr bytes.Buffer
	if exit := run(commandArguments(scorecards, evaluations, output), &stdout, &stderr, dependencies); exit != 1 ||
		!strings.Contains(stderr.String(), "load evidence failed") || strings.Contains(stderr.String(), "private loader detail") {
		t.Fatalf("dependency failure exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func commandPaths(directory string) ([]string, []string) {
	scorecards, evaluations := []string{}, []string{}
	for index := 0; index < 3; index++ {
		scorecards = append(scorecards, filepath.Join(directory, "scorecard-"+string(rune('a'+index))+".json"))
		evaluations = append(evaluations, filepath.Join(directory, "evaluation-"+string(rune('a'+index))+".json"))
	}
	return scorecards, evaluations
}

func commandArguments(scorecards, evaluations []string, output string) []string {
	arguments := []string{}
	for _, path := range scorecards {
		arguments = append(arguments, "--scorecard", path)
	}
	for _, path := range evaluations {
		arguments = append(arguments, "--evaluation", path)
	}
	return append(arguments, "--output", output)
}
