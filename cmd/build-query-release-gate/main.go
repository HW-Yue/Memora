package main

import (
	"fmt"
	"io"
	"os"

	"github.com/HW-Yue/Memora/internal/answerrelease"
)

type commandDependencies struct {
	load    func([]string, []string) ([]answerrelease.Evidence, error)
	build   func([]answerrelease.Evidence) (answerrelease.Report, error)
	publish func(string, answerrelease.Report) error
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, productionDependencies())) }

func run([]string, io.Writer, io.Writer, commandDependencies) int {
	return 1
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
