package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

func main() {
	var input releasepublish.TriggerInput
	var referencePath string
	var tagPath string
	var githubOutput string
	flag.StringVar(&input.Event, "event", "", "GitHub event name")
	flag.StringVar(&input.RefType, "ref-type", "", "GitHub ref type")
	flag.StringVar(&input.RefName, "ref-name", "", "GitHub ref name")
	flag.StringVar(&input.CheckoutCommit, "checkout-commit", "", "checked out commit")
	flag.BoolVar(&input.TargetInMain, "target-in-main", false, "tag target is in origin/main")
	flag.BoolVar(&input.ReleaseExists, "release-exists", false, "a release already exists")
	flag.StringVar(&referencePath, "reference-json", "", "GitHub Git reference response")
	flag.StringVar(&tagPath, "tag-json", "", "GitHub annotated tag response")
	flag.StringVar(&githubOutput, "github-output", "", "optional GitHub Actions output file")
	flag.Parse()
	if flag.NArg() != 0 || referencePath == "" || tagPath == "" {
		fatal(2, "reference and tag JSON files are required")
	}
	var err error
	input.ReferenceJSON, err = os.ReadFile(referencePath)
	if err != nil {
		fatal(1, fmt.Sprintf("read reference JSON: %v", err))
	}
	input.TagJSON, err = os.ReadFile(tagPath)
	if err != nil {
		fatal(1, fmt.Sprintf("read tag JSON: %v", err))
	}
	plan, err := releasepublish.ValidateTrigger(input)
	if err != nil {
		fatal(1, err.Error())
	}
	encoded, _ := json.Marshal(plan)
	fmt.Println(string(encoded))
	if githubOutput != "" {
		file, err := os.OpenFile(githubOutput, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			fatal(1, fmt.Sprintf("open GitHub output: %v", err))
		}
		_, writeErr := fmt.Fprintf(
			file, "tag=%s\nversion=%s\ncommit=%s\n",
			plan.Tag, plan.Version, plan.Commit,
		)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			fatal(1, "write GitHub output")
		}
	}
}

func fatal(code int, message string) {
	fmt.Fprintln(os.Stderr, "validate-release-trigger:", message)
	os.Exit(code)
}
