package releasepublish_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

const (
	tagObjectSHA = "0123456789abcdef0123456789abcdef01234567"
	targetCommit = "89abcdef0123456789abcdef0123456789abcdef"
)

func TestValidateTriggerAcceptsVerifiedAnnotatedStableTag(t *testing.T) {
	t.Parallel()

	plan, err := releasepublish.ValidateTrigger(validTrigger())
	if err != nil {
		t.Fatal(err)
	}
	if plan.Tag != "v1.2.3" || plan.Version != "1.2.3" || plan.Commit != targetCommit {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestValidateTriggerRejectsUnsafeReleaseInputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code string
		edit func(*releasepublish.TriggerInput)
	}{
		{"pull request", "invalid_event", func(value *releasepublish.TriggerInput) { value.Event = "pull_request" }},
		{"branch", "invalid_ref", func(value *releasepublish.TriggerInput) { value.RefType = "branch" }},
		{"prerelease", "invalid_tag", func(value *releasepublish.TriggerInput) { value.RefName = "v1.2.3-rc.1" }},
		{"leading zero", "invalid_tag", func(value *releasepublish.TriggerInput) { value.RefName = "v01.2.3" }},
		{"lightweight tag", "invalid_tag_object", func(value *releasepublish.TriggerInput) {
			value.ReferenceJSON = referenceJSON("commit", targetCommit)
		}},
		{"unsigned tag", "unverified_tag", func(value *releasepublish.TriggerInput) {
			value.TagJSON = tagJSON("v1.2.3", tagObjectSHA, targetCommit, false)
		}},
		{"wrong API tag", "tag_mismatch", func(value *releasepublish.TriggerInput) {
			value.TagJSON = tagJSON("v1.2.4", tagObjectSHA, targetCommit, true)
		}},
		{"wrong checkout", "target_mismatch", func(value *releasepublish.TriggerInput) {
			value.CheckoutCommit = strings.Repeat("a", 40)
		}},
		{"not main", "target_not_main", func(value *releasepublish.TriggerInput) { value.TargetInMain = false }},
		{"repeat", "release_exists", func(value *releasepublish.TriggerInput) { value.ReleaseExists = true }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := validTrigger()
			test.edit(&input)
			_, err := releasepublish.ValidateTrigger(input)
			var triggerError *releasepublish.TriggerError
			if err == nil || !releasepublish.AsTriggerError(err, &triggerError) ||
				triggerError.Code != test.code {
				t.Fatalf("error = %#v, want code %q", err, test.code)
			}
		})
	}
}

func TestValidateDraftRequiresExactNonemptyAssets(t *testing.T) {
	t.Parallel()

	version := "1.2.3"
	names := releasepublish.AssetNames(version)
	assets := make([]map[string]any, 0, len(names))
	for _, name := range names {
		assets = append(assets, map[string]any{"name": name, "size": 10})
	}
	value, _ := json.Marshal(map[string]any{
		"tagName": "v" + version,
		"isDraft": true,
		"assets":  assets,
	})
	if err := releasepublish.ValidateDraft(value, "v"+version, version); err != nil {
		t.Fatal(err)
	}
	assets[0]["size"] = 0
	value, _ = json.Marshal(map[string]any{
		"tagName": "v" + version,
		"isDraft": true,
		"assets":  assets,
	})
	if err := releasepublish.ValidateDraft(value, "v"+version, version); err == nil {
		t.Fatal("ValidateDraft accepted an empty asset")
	}
}

func validTrigger() releasepublish.TriggerInput {
	return releasepublish.TriggerInput{
		Event:          "push",
		RefType:        "tag",
		RefName:        "v1.2.3",
		CheckoutCommit: targetCommit,
		TargetInMain:   true,
		ReleaseExists:  false,
		ReferenceJSON:  referenceJSON("tag", tagObjectSHA),
		TagJSON:        tagJSON("v1.2.3", tagObjectSHA, targetCommit, true),
	}
}

func referenceJSON(objectType, sha string) []byte {
	value, _ := json.Marshal(map[string]any{
		"ref":    "refs/tags/v1.2.3",
		"object": map[string]any{"type": objectType, "sha": sha},
	})
	return value
}

func tagJSON(tag, sha, commit string, verified bool) []byte {
	value, _ := json.Marshal(map[string]any{
		"tag":          tag,
		"sha":          sha,
		"object":       map[string]any{"type": "commit", "sha": commit},
		"verification": map[string]any{"verified": verified},
	})
	return value
}
