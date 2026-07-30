package releasepublish

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	stableTagPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	objectIDPattern  = regexp.MustCompile(`^[0-9a-f]{40}(?:[0-9a-f]{24})?$`)
)

type TriggerInput struct {
	Event          string
	RefType        string
	RefName        string
	CheckoutCommit string
	TargetInMain   bool
	ReleaseExists  bool
	ReferenceJSON  []byte
	TagJSON        []byte
}

type TriggerPlan struct {
	Tag     string `json:"tag"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
}

type TriggerError struct {
	Code    string
	Message string
}

func (err *TriggerError) Error() string {
	return err.Code + ": " + err.Message
}

func AsTriggerError(err error, target **TriggerError) bool {
	return errors.As(err, target)
}

func ValidateTrigger(input TriggerInput) (TriggerPlan, error) {
	if input.Event != "push" {
		return TriggerPlan{}, triggerFailure("invalid_event", "release requires a tag push event")
	}
	if input.RefType != "tag" {
		return TriggerPlan{}, triggerFailure("invalid_ref", "release requires a tag ref")
	}
	matches := stableTagPattern.FindStringSubmatch(input.RefName)
	if matches == nil {
		return TriggerPlan{}, triggerFailure("invalid_tag", "tag must match stable vMAJOR.MINOR.PATCH")
	}
	var reference struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	if err := json.Unmarshal(input.ReferenceJSON, &reference); err != nil {
		return TriggerPlan{}, triggerFailure("invalid_payload", "Git reference response is invalid")
	}
	if reference.Ref != "refs/tags/"+input.RefName {
		return TriggerPlan{}, triggerFailure("tag_mismatch", "Git reference does not match the event tag")
	}
	if reference.Object.Type != "tag" || !validObjectID(reference.Object.SHA) {
		return TriggerPlan{}, triggerFailure("invalid_tag_object", "release tag must be an annotated tag object")
	}
	var tag struct {
		Tag    string `json:"tag"`
		SHA    string `json:"sha"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
		Verification struct {
			Verified bool `json:"verified"`
		} `json:"verification"`
	}
	if err := json.Unmarshal(input.TagJSON, &tag); err != nil {
		return TriggerPlan{}, triggerFailure("invalid_payload", "Git tag response is invalid")
	}
	if tag.Tag != input.RefName || tag.SHA != reference.Object.SHA {
		return TriggerPlan{}, triggerFailure("tag_mismatch", "annotated tag object does not match the event tag")
	}
	if !tag.Verification.Verified {
		return TriggerPlan{}, triggerFailure("unverified_tag", "GitHub did not verify the annotated tag signature")
	}
	if tag.Object.Type != "commit" || !validObjectID(tag.Object.SHA) {
		return TriggerPlan{}, triggerFailure("invalid_tag_target", "annotated tag must point directly to a commit")
	}
	checkout := strings.ToLower(strings.TrimSpace(input.CheckoutCommit))
	if checkout != tag.Object.SHA {
		return TriggerPlan{}, triggerFailure("target_mismatch", "checked out commit does not match the signed tag target")
	}
	if !input.TargetInMain {
		return TriggerPlan{}, triggerFailure("target_not_main", "signed tag target is not in origin/main history")
	}
	if input.ReleaseExists {
		return TriggerPlan{}, triggerFailure("release_exists", "a GitHub Release already exists for this tag")
	}
	return TriggerPlan{
		Tag: input.RefName, Version: strings.TrimPrefix(input.RefName, "v"), Commit: tag.Object.SHA,
	}, nil
}

func triggerFailure(code, message string) error {
	return &TriggerError{Code: code, Message: message}
}

func validObjectID(value string) bool {
	return objectIDPattern.MatchString(value)
}

func ValidateDraft(value []byte, tag, version string) error {
	if tag != "v"+version || stableTagPattern.FindStringSubmatch(tag) == nil {
		return errors.New("release draft tag or version is invalid")
	}
	var draft struct {
		TagName string `json:"tagName"`
		IsDraft bool   `json:"isDraft"`
		Assets  []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.Unmarshal(value, &draft); err != nil {
		return errors.New("release draft response is invalid")
	}
	if draft.TagName != tag || !draft.IsDraft {
		return errors.New("release draft identity or state is invalid")
	}
	expected := AssetNames(version)
	if len(draft.Assets) != len(expected) {
		return errors.New("release draft has an unexpected asset set")
	}
	seen := make(map[string]bool, len(draft.Assets))
	for _, asset := range draft.Assets {
		if asset.Size <= 0 || seen[asset.Name] {
			return fmt.Errorf("release draft asset %q is empty or duplicated", asset.Name)
		}
		seen[asset.Name] = true
	}
	for _, name := range expected {
		if !seen[name] {
			return fmt.Errorf("release draft is missing asset %q", name)
		}
	}
	return nil
}

func AssetNames(version string) []string {
	names := []string{
		"checksums.txt",
		fmt.Sprintf("memora_%s_darwin_amd64.tar.gz", version),
		fmt.Sprintf("memora_%s_darwin_arm64.tar.gz", version),
		fmt.Sprintf("memora_%s_skill_bundle.tar.gz", version),
		fmt.Sprintf("memora_%s_skill_bundle.tar.gz.sha256", version),
		"release.json",
	}
	return names
}
