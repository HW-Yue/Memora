package storygate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/claudeadapter"
	"github.com/HW-Yue/Memora/internal/codexadapter"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

type RunOptions struct {
	Binary       string
	DataDir      string
	CanonicalDir string
	AdapterDir   string
	Host         string
}

type runtimeJourney struct {
	ctx     context.Context
	options RunOptions
	report  Report
	steps   map[string]bool
}

func Run(ctx context.Context, options RunOptions) (Report, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !filepath.IsAbs(options.Binary) || !filepath.IsAbs(options.DataDir) ||
		!filepath.IsAbs(options.CanonicalDir) || !filepath.IsAbs(options.AdapterDir) {
		return Report{}, errors.New("story gate paths must be absolute")
	}
	binaryDigest, err := fileDigest(options.Binary)
	if err != nil {
		return Report{}, err
	}
	canonicalDigest, protocolDigest, taskContractDigest, err := installAdapter(options)
	if err != nil {
		return Report{}, err
	}
	journey := &runtimeJourney{
		ctx: ctx, options: options, steps: map[string]bool{},
		report: Report{
			Version: ReportVersion, Status: "failed", Host: options.Host,
			BinarySHA256: binaryDigest, CanonicalDigest: canonicalDigest, ProtocolDigest: protocolDigest,
			TaskContractDigest: taskContractDigest,
			Steps:              []Step{}, Stories: []StoryResult{},
		},
	}
	if _, err := journey.command("init", "init", "--data-dir", options.DataDir); err != nil {
		return journey.report, err
	}
	if _, err := journey.command("daemon-start", "daemon", "start", "--data-dir", options.DataDir); err != nil {
		return journey.report, err
	}
	defer func() { _, _ = journey.command("daemon-stop", "daemon", "stop", "--data-dir", options.DataDir) }()
	if err := journey.run(); err != nil {
		return journey.report, err
	}
	journey.report.Stories = journey.storyResults()
	journey.report.Status = "passed"
	if err := Validate(journey.report); err != nil {
		journey.report.Status = "failed"
		return journey.report, err
	}
	return journey.report, nil
}

func installAdapter(options RunOptions) (string, string, string, error) {
	switch options.Host {
	case "codex":
		bundle, err := codexadapter.Build(options.CanonicalDir)
		if err != nil {
			return "", "", "", err
		}
		if err := bundle.Install(options.AdapterDir); err != nil {
			return "", "", "", err
		}
		return bundle.Manifest.CanonicalDigest, bundle.Manifest.ProtocolDigest,
			bundle.Manifest.TaskContractDigest, nil
	case "claude":
		bundle, err := claudeadapter.Build(options.CanonicalDir)
		if err != nil {
			return "", "", "", err
		}
		if err := bundle.Install(options.AdapterDir); err != nil {
			return "", "", "", err
		}
		return bundle.Manifest.CanonicalDigest, bundle.Manifest.ProtocolDigest,
			bundle.Manifest.TaskContractDigest, nil
	default:
		return "", "", "", fmt.Errorf("story gate host %q is unsupported", options.Host)
	}
}

func (journey *runtimeJourney) run() error {
	if _, err := journey.command("version", "version", "--json"); err != nil {
		return err
	}
	ddl := "CREATE DATABASE work PURPOSE 'Release story knowledge' SCOPE 'Isolated runtime gate'; " +
		"CREATE TABLE work.notes PURPOSE 'Durable notes' ROW SEMANTICS 'One semantic module' " +
		"(title TEXT(2000) NOT NULL PURPOSE 'Module title')"
	if _, err := journey.envelope("schema-create", "exec", nil, ddl); err != nil {
		return err
	}
	root, err := journey.envelope(
		"route-root-create", "exec", mutationInput(nil, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'",
	)
	if err != nil {
		return err
	}
	rootID := stringRow(root, 0, "route_id")
	branch, err := journey.envelope(
		"route-branch-create", "exec",
		mutationInput(map[string]any{"parent": rootID, "name": "architecture", "kind": "branch", "purpose": "Architecture"}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
	)
	if err != nil {
		return err
	}
	branchID := stringRow(branch, 0, "route_id")
	leaf, err := journey.envelope(
		"route-leaf-create", "exec",
		mutationInput(map[string]any{"parent": branchID, "name": "storage", "kind": "leaf", "purpose": "Storage"}, map[string]any{"max_affected_rows": 1}),
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
	)
	if err != nil {
		return err
	}
	leafID := stringRow(leaf, 0, "route_id")
	inserted, err := journey.envelope(
		"row-insert", "exec", mutationInput(nil, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 1, "route_leaf_ids": []string{leafID},
			"actor": "agent:" + journey.options.Host, "source": "story:insert", "reason": "runtime insertion",
		}), "INSERT INTO work.notes (title) VALUES ('native manifest')",
	)
	if err != nil {
		return err
	}
	rowID := stringRow(inserted, 0, "row_id")
	if err := journey.discover("after-insert", branchID, leafID, map[string]uint64{rowID: 1}); err != nil {
		return err
	}
	updated, err := journey.envelope(
		"row-update", "exec", mutationInput(map[string]any{"row": rowID, "title": "atomic native manifest"}, map[string]any{
			"expected_schema_version": 1, "expected_revision": 1, "max_affected_rows": 1,
			"route_leaf_ids": []string{leafID}, "actor": "agent:" + journey.options.Host,
			"source": "story:update", "reason": "runtime correction",
		}), "UPDATE work.notes SET title = :title WHERE row_id = :row",
	)
	if err != nil || revision(updated) != 2 {
		return fmt.Errorf("runtime UPDATE failed: %w", err)
	}
	if err := journey.discover("after-update", branchID, leafID, map[string]uint64{rowID: 2}); err != nil {
		return err
	}
	if _, err := journey.envelope(
		"history-conflict", "query", mutationInput(map[string]any{"row": rowID}, nil),
		"SHOW HISTORY FROM work.notes FOR ROW :row LIMIT 10",
	); err != nil {
		return err
	}
	if _, err := journey.envelope(
		"row-delete", "exec", mutationInput(map[string]any{"row": rowID}, map[string]any{
			"expected_schema_version": 1, "expected_revision": 2, "max_affected_rows": 1,
			"actor": "agent:" + journey.options.Host, "source": "story:delete", "reason": "runtime delete",
		}), "DELETE FROM work.notes WHERE row_id = :row",
	); err != nil {
		return err
	}
	if err := journey.discover("after-delete", branchID, leafID, map[string]uint64{}); err != nil {
		return err
	}
	restored, err := journey.envelope(
		"row-restore", "exec", mutationInput(map[string]any{"row": rowID, "revision": 2}, map[string]any{
			"expected_schema_version": 1, "expected_revision": 3, "max_affected_rows": 1,
			"route_leaf_ids": []string{leafID}, "actor": "agent:" + journey.options.Host,
			"source": "story:restore", "reason": "runtime recovery",
		}), "RESTORE work.notes ROW :row TO REVISION :revision",
	)
	if err != nil || revision(restored) != 4 {
		return fmt.Errorf("runtime RESTORE failed: %w", err)
	}
	if err := journey.discover("after-restore", branchID, leafID, map[string]uint64{rowID: 4}); err != nil {
		return err
	}
	feedbackEvent := feedback.Event{
		Version: feedback.EventVersion, EventID: journey.options.Host + "-feedback",
		Kind: feedback.KindWrong, Actor: "user:story-gate", Reason: "exercise correction receipt",
		Target: feedback.Target{Database: "work", Table: "notes", RowID: rowID, Revision: 4},
	}
	if _, err := journey.jsonCommand("feedback-record", "feedback", "--data-dir", journey.options.DataDir, "--event", mustJSON(feedbackEvent)); err != nil {
		return err
	}
	split, err := journey.envelope(
		"row-split", "exec", mutationInput(map[string]any{"row": rowID, "first": "manifest atomicity", "second": "manifest publication"}, map[string]any{
			"expected_schema_version": 1, "expected_revision": 4, "max_affected_rows": 3,
			"target_route_leaf_ids": [][]string{{leafID}, {leafID}}, "actor": "agent:" + journey.options.Host,
			"source": "story:split", "reason": "runtime semantic split",
		}), "SPLIT work.notes ROW :row INTO (title) VALUES (:first), (:second)",
	)
	if err != nil || len(split.Results[0].Rows) != 2 {
		return fmt.Errorf("runtime SPLIT failed: %w", err)
	}
	firstID, secondID := stringRow(split, 0, "row_id"), stringRow(split, 1, "row_id")
	if err := journey.discover("after-split", branchID, leafID, map[string]uint64{firstID: 1, secondID: 1}); err != nil {
		return err
	}
	merged, err := journey.envelope(
		"row-merge", "exec", mutationInput(map[string]any{"first": firstID, "second": secondID, "merged": "atomic manifest publication"}, map[string]any{
			"expected_schema_version": 1, "max_affected_rows": 3,
			"source_revisions":      map[string]uint64{firstID: 1, secondID: 1},
			"target_route_leaf_ids": [][]string{{leafID}}, "actor": "agent:" + journey.options.Host,
			"source": "story:merge", "reason": "runtime semantic merge",
		}), "MERGE work.notes ROWS (:first, :second) INTO (title) VALUES (:merged)",
	)
	if err != nil || len(merged.Results[0].Rows) != 1 {
		return fmt.Errorf("runtime MERGE failed: %w", err)
	}
	mergedID := stringRow(merged, 0, "row_id")
	if err := journey.discover("after-merge", branchID, leafID, map[string]uint64{mergedID: 1}); err != nil {
		return err
	}
	if err := journey.assimilate(leafID); err != nil {
		return err
	}
	if err := journey.discoverAtLeast("after-assimilation", branchID, leafID, 2); err != nil {
		return err
	}
	if _, err := journey.jsonCommand("semantic-health", "maintain", "--data-dir", journey.options.DataDir, "--report"); err != nil {
		return err
	}
	if _, err := journey.envelope("configuration-show", "query", nil, "SHOW CONFIGURATION"); err != nil {
		return err
	}
	if _, err := journey.envelope(
		"configuration-alter", "exec", mutationInput(nil, map[string]any{
			"expected_revision": 1, "actor": "agent:" + journey.options.Host, "reason": "runtime budget check",
		}), "ALTER CONFIGURATION QUERY_BUDGETS SET ROUTE_CHILDREN 6, OPEN_LOCATORS 12, SELECT_SCAN 100, SELECT_ROWS 5, ROUTE_FRAME_NODES 6",
	); err != nil {
		return err
	}
	if _, err := journey.envelope(
		"configuration-restore", "exec", mutationInput(nil, map[string]any{
			"expected_revision": 2, "actor": "agent:" + journey.options.Host, "reason": "restore defaults",
		}), "RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION 1",
	); err != nil {
		return err
	}
	if _, err := journey.command("daemon-restart-stop", "daemon", "stop", "--data-dir", journey.options.DataDir); err != nil {
		return err
	}
	if _, err := journey.command("daemon-restart-start", "daemon", "start", "--data-dir", journey.options.DataDir); err != nil {
		return err
	}
	if err := journey.discoverAtLeast("after-restart", branchID, leafID, 2); err != nil {
		return err
	}
	_, err = journey.jsonCommand("final-doctor", "doctor", "--data-dir", journey.options.DataDir)
	return err
}

func (journey *runtimeJourney) assimilate(leafID string) error {
	task := journey.options.Host + "-source"
	sourceHash := "sha256:" + strings.Repeat("a", 64)
	inventory := assimilation.Event{
		Version: assimilation.EventVersion, EventID: task + "-inventory", TaskID: task,
		Workspace: "story-gate", Kind: assimilation.KindInventory,
		Inventory: &assimilation.Inventory{
			Source: assimilation.Source{ID: task, Title: "Fixture", Locator: "fixture/story.md", ContentHash: sourceHash, Kind: "document_anchor"},
			Units: []assimilation.Unit{
				{ID: "source", Kind: assimilation.UnitSource, Label: "Fixture"},
				{ID: "section", ParentID: "source", Kind: assimilation.UnitChapter, Label: "Section", Extent: 1},
			},
		},
	}
	var receipt assimilation.Receipt
	if err := journey.decodeCommand(&receipt, "assimilation-inventory", "assimilate", "--data-dir", journey.options.DataDir, "--event", mustJSON(inventory)); err != nil {
		return err
	}
	window := assimilation.Event{
		Version: assimilation.EventVersion, EventID: task + "-window", TaskID: task,
		Workspace: "story-gate", Kind: assimilation.KindWindow, ExpectedRevision: receipt.Revision,
		Window: &assimilation.Window{UnitID: "section", Start: 0, End: 1, Fingerprint: "sha256:" + strings.Repeat("b", 64)},
	}
	if err := journey.decodeCommand(&receipt, "assimilation-window", "assimilate", "--data-dir", journey.options.DataDir, "--event", mustJSON(window)); err != nil {
		return err
	}
	finish := assimilation.Event{
		Version: assimilation.EventVersion, EventID: task + "-finish", TaskID: task,
		Workspace: "story-gate", Kind: assimilation.KindFinish, ExpectedRevision: receipt.Revision,
	}
	if err := journey.decodeCommand(&receipt, "assimilation-finish", "assimilate", "--data-dir", journey.options.DataDir, "--event", mustJSON(finish)); err != nil {
		return err
	}
	zero, one := 0, 1
	submissionID := task + "-submission"
	title := "reviewed runtime source"
	plan := skillwrite.Plan{
		Version: skillwrite.PlanVersion, ID: task + "-plan", Decision: skillwrite.DecisionInsert,
		Database: "work", Table: "notes", Actor: "agent:" + journey.options.Host,
		SourceEventID: submissionID, Reason: "absorb reviewed runtime fixture",
		AuthorizedDatabases: []string{"work"},
		Preflight: []skillwrite.Check{{
			ID: "before", MSQL: "SELECT row_id FROM work.notes WHERE title = :title LIMIT 1",
			Input: executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"title": title}}}, ExpectRows: &zero,
		}},
		Steps: []skillwrite.Step{{
			ID: "insert", Kind: "INSERT", Target: "module",
			MSQL: "INSERT INTO work.notes (title) VALUES (:title)",
			Input: executor.StatementInput{
				Parameters: executor.Parameters{Named: map[string]any{"title": title}},
				Mutation: executor.MutationOptions{
					ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{leafID},
					Actor: "agent:" + journey.options.Host, Source: submissionID, Reason: "absorb reviewed runtime fixture",
				},
			},
		}},
		Verify: []skillwrite.Check{{
			ID: "after", MSQL: "SELECT title, row_id, revision FROM work.notes WHERE title = :title LIMIT 1",
			Input:      executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"title": title}}},
			ExpectRows: &one, RowContains: map[string]any{"title": title},
		}},
	}
	review := assimilation.Review{
		Version: assimilation.ReviewVersion, Reviewer: "agent:review", ContextID: "isolated-review",
		DraftDigest: "sha256:" + strings.Repeat("c", 64), CoverageRevision: receipt.Revision,
		Verdict: assimilation.ReviewAccepted, CheckedModuleIDs: []string{"module"},
		AnchorsVerified: true, KeyFactsVerified: true, ConflictsChecked: true, RawContentAbsent: true,
		Challenge: receipt.ReviewChallenge, FindingsDigest: "sha256:" + strings.Repeat("d", 64),
	}
	review.ArtifactDigest = assimilation.ReviewArtifactDigest(review)
	submission := assimilation.Submission{
		Version: assimilation.SubmissionVersion, SubmissionID: submissionID, TaskID: task,
		Workspace: "story-gate", CoverageRevision: receipt.Revision, Author: "agent:" + journey.options.Host,
		DraftContextID: "draft-context", DraftDigest: review.DraftDigest,
		Modules: []assimilation.SemanticModule{{
			ID: "module", Anchors: []assimilation.SourceAnchor{{UnitID: "section", Start: 0, End: 1}}, Plan: plan,
		}},
		Review: review,
	}
	var sourceReceipt assimilation.SourceReceipt
	if err := journey.decodeCommand(&sourceReceipt, "assimilation-submit", "assimilate", "--data-dir", journey.options.DataDir, "--submission", mustJSON(submission)); err != nil {
		return err
	}
	if sourceReceipt.Status != assimilation.SubmissionCommitted {
		return errors.New("runtime assimilation did not commit")
	}
	return nil
}

func (journey *runtimeJourney) discover(tag, branchID, leafID string, expected map[string]uint64) error {
	open, err := journey.route(tag, branchID, leafID)
	if err != nil {
		return err
	}
	if len(open.Results[0].Rows) != len(expected) {
		return fmt.Errorf("%s locator count = %d, want %d", tag, len(open.Results[0].Rows), len(expected))
	}
	for _, locator := range open.Results[0].Rows {
		rowID, _ := locator["row_id"].(string)
		revision, _ := number(locator["revision"])
		if expected[rowID] != revision {
			return fmt.Errorf("%s locator %q revision = %d", tag, rowID, revision)
		}
		if _, err := journey.envelope(
			tag+"-select-"+rowID, "query", mutationInput(map[string]any{"row": rowID}, nil),
			"SELECT title, row_id, revision FROM work.notes WHERE row_id = :row LIMIT 1",
		); err != nil {
			return err
		}
	}
	return nil
}

func (journey *runtimeJourney) discoverAtLeast(tag, branchID, leafID string, minimum int) error {
	open, err := journey.route(tag, branchID, leafID)
	if err != nil {
		return err
	}
	if len(open.Results[0].Rows) < minimum {
		return fmt.Errorf("%s locator count = %d, want at least %d", tag, len(open.Results[0].Rows), minimum)
	}
	return nil
}

func (journey *runtimeJourney) route(tag, branchID, leafID string) (result.Envelope, error) {
	if _, err := journey.envelope(tag+"-config-databases", "query", nil, "SHOW CONFIGURATION; SHOW DATABASES"); err != nil {
		return result.Envelope{}, err
	}
	if _, err := journey.envelope(tag+"-tables", "query", nil, "SHOW TABLES FROM work COMPACT"); err != nil {
		return result.Envelope{}, err
	}
	if _, err := journey.envelope(tag+"-schema", "query", nil, "DESCRIBE TABLE work.notes COMPACT"); err != nil {
		return result.Envelope{}, err
	}
	root, err := journey.envelope(tag+"-root", "query", nil, "SHOW ROUTES FROM TABLE work.notes AT ROOT LIMIT 12")
	if err != nil || stringRow(root, 0, "route_id") != branchID {
		return result.Envelope{}, fmt.Errorf("%s root rediscovery failed: %w", tag, err)
	}
	children, err := journey.envelope(tag+"-children", "query", mutationInput(map[string]any{"route": branchID}, nil), "SHOW ROUTES UNDER :route LIMIT 12")
	if err != nil || stringRow(children, 0, "route_id") != leafID {
		return result.Envelope{}, fmt.Errorf("%s child rediscovery failed: %w", tag, err)
	}
	return journey.envelope(tag+"-open", "query", mutationInput(map[string]any{"leaf": leafID}, nil), "OPEN ROUTE :leaf LIMIT 24")
}

func (journey *runtimeJourney) envelope(id, command string, input *string, msql string) (result.Envelope, error) {
	args := []string{command, "--data-dir", journey.options.DataDir}
	if input != nil {
		args = append(args, "--input", *input)
	}
	args = append(args, msql)
	output, err := journey.command(id, args...)
	if err != nil {
		return result.Envelope{}, err
	}
	var envelope result.Envelope
	if err := json.Unmarshal(output, &envelope); err != nil || !envelope.OK {
		return result.Envelope{}, fmt.Errorf("%s returned invalid or failed envelope: %w", id, err)
	}
	return envelope, nil
}

func (journey *runtimeJourney) jsonCommand(id string, args ...string) ([]byte, error) {
	output, err := journey.command(id, args...)
	if err != nil {
		return nil, err
	}
	var value any
	if json.Unmarshal(output, &value) != nil {
		return nil, fmt.Errorf("%s did not return JSON", id)
	}
	return output, nil
}

func (journey *runtimeJourney) decodeCommand(target any, id string, args ...string) error {
	output, err := journey.jsonCommand(id, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(output, target)
}

func (journey *runtimeJourney) command(id string, args ...string) ([]byte, error) {
	if journey.steps[id] {
		return nil, fmt.Errorf("runtime story step %q is duplicated", id)
	}
	command := exec.CommandContext(journey.ctx, journey.options.Binary, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		return output, fmt.Errorf("%s: %w: %s", id, err, output)
	}
	sum := sha256.Sum256(output)
	journey.report.Steps = append(journey.report.Steps, Step{
		ID: id, Surface: "public-cli", OutputSHA256: hex.EncodeToString(sum[:]),
	})
	journey.steps[id] = true
	return output, nil
}

func (journey *runtimeJourney) storyResults() []StoryResult {
	mapping := map[string][]string{
		"US-HUMAN": {"version"}, "US-COLD": {"after-insert-config-databases", "after-insert-schema", "after-insert-root"},
		"US-READ":       {"after-insert-root", "after-insert-children", "after-insert-open", "after-insert-select-" + firstStepSuffix(journey.steps, "after-insert-select-")},
		"US-INSERT":     {"row-insert", "after-insert-root", "after-insert-open"},
		"US-UPDATE":     {"row-update", "after-update-root", "after-update-open"},
		"US-DELETE":     {"row-delete", "after-delete-root", "after-delete-open"},
		"US-CORRECT":    {"feedback-record", "row-update", "after-update-root"},
		"US-SCHEMA":     {"schema-create", "after-insert-schema"},
		"US-DBA":        {"semantic-health", "final-doctor"},
		"US-OPTIMIZE":   {"row-split", "row-merge", "after-merge-root"},
		"US-SPLIT":      {"row-split", "after-split-root", "after-split-open"},
		"US-CONFLICT":   {"history-conflict"},
		"US-ASSIMILATE": {"assimilation-submit", "after-assimilation-root", "after-assimilation-open"},
		"US-RECOVER":    {"row-restore", "after-restore-root", "after-restart-root"},
		"US-ENGINE":     {"configuration-alter", "configuration-restore", "after-restart-open"},
		"US-DEVELOPER":  {"version", "final-doctor"},
	}
	mutation := map[string]bool{
		"US-INSERT": true, "US-UPDATE": true, "US-DELETE": true, "US-CORRECT": true,
		"US-OPTIMIZE": true, "US-SPLIT": true, "US-ASSIMILATE": true, "US-RECOVER": true,
		"US-ENGINE": true,
	}
	results := make([]StoryResult, 0, len(RequiredStories()))
	for _, story := range RequiredStories() {
		results = append(results, StoryResult{
			Story: story, Status: "passed", Steps: mapping[story],
			RediscoveredFromRoot: mutation[story],
		})
	}
	return results
}

func firstStepSuffix(steps map[string]bool, prefix string) string {
	for step := range steps {
		if strings.HasPrefix(step, prefix) {
			return strings.TrimPrefix(step, prefix)
		}
	}
	return ""
}

func mutationInput(parameters, mutation map[string]any) *string {
	value := map[string]any{}
	if parameters != nil {
		value["parameters"] = map[string]any{"named": parameters}
	}
	if mutation != nil {
		value["mutation"] = mutation
	}
	encoded, _ := json.Marshal(value)
	text := string(encoded)
	return &text
}

func revision(envelope result.Envelope) uint64 {
	if len(envelope.Results) == 0 || envelope.Results[0].Revision == nil {
		return 0
	}
	return *envelope.Results[0].Revision
}

func stringRow(envelope result.Envelope, index int, field string) string {
	if len(envelope.Results) == 0 || index < 0 || index >= len(envelope.Results[0].Rows) {
		return ""
	}
	value, _ := envelope.Results[0].Rows[index][field].(string)
	return value
}

func number(value any) (uint64, bool) {
	switch typed := value.(type) {
	case float64:
		return uint64(typed), typed >= 0 && typed == float64(uint64(typed))
	case uint64:
		return typed, true
	default:
		return 0, false
	}
}

func mustJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func fileDigest(path string) (string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:]), nil
}
