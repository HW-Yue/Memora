package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/answerrelease"
)

type commandDependencies struct {
	load    func([]string, []string) ([]answerrelease.Evidence, error)
	build   func([]answerrelease.Evidence) (answerrelease.Report, error)
	publish func(string, answerrelease.Report) error
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, productionDependencies())) }

func run(arguments []string, stdout, stderr io.Writer, dependencies commandDependencies) int {
	flags := flag.NewFlagSet("build-query-release-gate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var scorecards, evaluations stringList
	flags.Var(&scorecards, "scorecard", "absolute F183 public scorecard path; repeat exactly three times")
	flags.Var(&evaluations, "evaluation", "absolute F184 public evaluation path; repeat exactly three times")
	output := flags.String("output", "", "absolute new query release report path")
	if err := flags.Parse(arguments); err != nil {
		return 2
	}
	if flags.NArg() != 0 || len(scorecards) != 3 || len(evaluations) != 3 ||
		!absoluteNormalized(*output) || !allAbsoluteNormalized(scorecards) || !allAbsoluteNormalized(evaluations) {
		fmt.Fprintln(stderr, "build-query-release-gate: exactly three absolute normalized scorecards and evaluations plus one absolute output are required")
		return 2
	}
	if dependencies.load == nil || dependencies.build == nil || dependencies.publish == nil {
		return commandFailure(stderr, "command dependencies are unavailable")
	}
	if _, err := os.Lstat(*output); err == nil {
		return commandFailure(stderr, answerrelease.ErrOutputExists.Error())
	} else if !errors.Is(err, os.ErrNotExist) {
		return commandFailure(stderr, "cannot inspect output")
	}
	evidence, err := dependencies.load(scorecards, evaluations)
	if err != nil {
		return commandFailure(stderr, "load evidence failed")
	}
	report, err := dependencies.build(evidence)
	if err != nil {
		return commandFailure(stderr, "build release report failed")
	}
	if err := dependencies.publish(*output, report); err != nil {
		return commandFailure(stderr, err.Error())
	}
	defaultArm := report.DefaultArm
	if defaultArm == "" {
		defaultArm = "-"
	}
	fmt.Fprintf(stdout, "Query release gate: status=%s default_arm=%s output=%s report=%s\n",
		report.Status, defaultArm, *output, report.Hash)
	if report.Status != answerrelease.StatusPassed {
		return 1
	}
	return 0
}

func productionDependencies() commandDependencies {
	return commandDependencies{
		load: answerrelease.LoadEvidence, build: answerrelease.Build, publish: answerrelease.PublishReport,
	}
}

func commandFailure(stderr io.Writer, message string) int {
	fmt.Fprintln(stderr, "build-query-release-gate:", message)
	return 1
}

type stringList []string

func (values *stringList) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func (values *stringList) String() string { return strings.Join(*values, ",") }

func allAbsoluteNormalized(paths []string) bool {
	for _, path := range paths {
		if !absoluteNormalized(path) {
			return false
		}
	}
	return true
}

func absoluteNormalized(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}
