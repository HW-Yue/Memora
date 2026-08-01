package storygate

import (
	"fmt"
	"sort"
	"strings"
)

const ReportVersion = "memora.real-story-gate/v2"

type Step struct {
	ID           string `json:"id"`
	Surface      string `json:"surface"`
	OutputSHA256 string `json:"output_sha256"`
}

type StoryResult struct {
	Story                string   `json:"story"`
	Status               string   `json:"status"`
	Steps                []string `json:"steps"`
	RediscoveredFromRoot bool     `json:"rediscovered_from_root,omitempty"`
}

type Report struct {
	Version            string        `json:"version"`
	Status             string        `json:"status"`
	Host               string        `json:"host"`
	BinarySHA256       string        `json:"binary_sha256"`
	CanonicalDigest    string        `json:"canonical_digest"`
	ProtocolDigest     string        `json:"protocol_digest"`
	TaskContractDigest string        `json:"task_contract_digest"`
	Steps              []Step        `json:"steps"`
	Stories            []StoryResult `json:"stories"`
}

func RequiredStories() []string {
	return []string{
		"US-HUMAN", "US-COLD", "US-READ", "US-INSERT", "US-UPDATE", "US-DELETE",
		"US-CORRECT", "US-SCHEMA", "US-DBA", "US-OPTIMIZE", "US-SPLIT", "US-CONFLICT",
		"US-ASSIMILATE", "US-RECOVER", "US-ENGINE", "US-DEVELOPER",
	}
}

func Validate(report Report) error {
	if report.Version != ReportVersion || report.Status != "passed" {
		return fmt.Errorf("runtime story report did not pass")
	}
	if report.Host != "codex" && report.Host != "claude" {
		return fmt.Errorf("runtime story report host %q is unsupported", report.Host)
	}
	if !isSHA256(report.BinarySHA256) || !isSHA256(report.CanonicalDigest) ||
		!isSHA256(report.ProtocolDigest) || !isSHA256(report.TaskContractDigest) {
		return fmt.Errorf("runtime story report digests are invalid")
	}
	stepIDs := map[string]bool{}
	for _, step := range report.Steps {
		if step.ID == "" || step.Surface != "public-cli" || !isSHA256(step.OutputSHA256) || stepIDs[step.ID] {
			return fmt.Errorf("runtime story report step is invalid or duplicated")
		}
		stepIDs[step.ID] = true
	}
	required := map[string]bool{}
	for _, story := range RequiredStories() {
		required[story] = false
	}
	mutations := map[string]bool{
		"US-INSERT": true, "US-UPDATE": true, "US-DELETE": true, "US-CORRECT": true,
		"US-OPTIMIZE": true, "US-SPLIT": true, "US-ASSIMILATE": true, "US-RECOVER": true,
		"US-ENGINE": true,
	}
	for _, story := range report.Stories {
		if _, ok := required[story.Story]; !ok {
			return fmt.Errorf("unknown runtime story %q", story.Story)
		}
		if required[story.Story] {
			return fmt.Errorf("duplicate runtime story %q", story.Story)
		}
		if story.Status != "passed" || len(story.Steps) == 0 {
			return fmt.Errorf("runtime story %q did not pass with steps", story.Story)
		}
		for _, step := range story.Steps {
			if !stepIDs[step] {
				return fmt.Errorf("runtime story %q references missing step %q", story.Story, step)
			}
		}
		if mutations[story.Story] && !story.RediscoveredFromRoot {
			return fmt.Errorf("runtime story %q bypassed top-level Route rediscovery", story.Story)
		}
		required[story.Story] = true
	}
	missing := []string{}
	for story, found := range required {
		if !found {
			missing = append(missing, story)
		}
	}
	sort.Strings(missing)
	if len(missing) > 0 {
		return fmt.Errorf("missing runtime stories: %s", strings.Join(missing, ", "))
	}
	return nil
}

func ValidatePair(codex, claude Report) error {
	if err := Validate(codex); err != nil {
		return fmt.Errorf("Codex report: %w", err)
	}
	if err := Validate(claude); err != nil {
		return fmt.Errorf("Claude report: %w", err)
	}
	if codex.Host != "codex" || claude.Host != "claude" {
		return fmt.Errorf("runtime reports are not a Codex/Claude pair")
	}
	if codex.BinarySHA256 != claude.BinarySHA256 ||
		codex.CanonicalDigest != claude.CanonicalDigest ||
		codex.ProtocolDigest != claude.ProtocolDigest ||
		codex.TaskContractDigest != claude.TaskContractDigest {
		return fmt.Errorf("runtime hosts did not execute one binary, canonical protocol, and Task contract")
	}
	return nil
}

func isSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}
