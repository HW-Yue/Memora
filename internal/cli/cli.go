package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/HW-Yue/Memora/internal/assimilation"
	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/conversation"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/feedback"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/instanceupgrade"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/msql/parser"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	"github.com/HW-Yue/Memora/internal/skillschema"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const helpText = `Memora is an AI-maintained local personal database.

Usage:
  memora <command> [options]

Commands:
  assimilate  Track, review, and receipt source assimilation
  daemon     Manage the local daemon
  doctor     Verify logical database integrity
  exec       Execute MSQL through the local daemon
  export     Export a deterministic Obsidian Wiki
  feedback   Record feedback or confirm an auditable revision
  help       Show this help
  init       Initialize a local instance
  install    Install an explicitly trusted database package
  maintain   Report and retry low-risk semantic maintenance
  mutate     Execute a validated Mutation Plan
  open       Inspect a database package read-only
  pack       Export one portable database package
  parse      Parse an MSQL request through the local daemon
  query      Query MSQL through the local daemon
  reflect    Ingest an explicit conversation event
  schema     Execute a validated Schema Plan
  upgrade    Plan or apply a transactional Instance format upgrade
  version    Show build version

Run 'memora help' for usage.
`

type BuildInfo struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

type versionOutput struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"built_at"`
}

func Run(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	return RunWithDependencies(args, stdout, stderr, build, Dependencies{
		HomeDir: os.UserHomeDir,
	})
}

type Dependencies struct {
	HomeDir     func() (string, error)
	LookupEnv   func(string) (string, bool)
	Clock       instance.Clock
	IDs         instance.IDSource
	ExecuteMSQL func(
		context.Context,
		string,
		string,
		[]executor.StatementInput,
	) (result.Envelope, error)
	Reflect            func(context.Context, string, conversation.Event) (conversation.Receipt, error)
	Assimilate         func(context.Context, string, assimilation.Event) (assimilation.Receipt, error)
	SubmitAssimilation func(context.Context, string, assimilation.Submission) (assimilation.SourceReceipt, error)
	GetSourceReceipt   func(context.Context, string, string) (assimilation.SourceReceipt, error)
	SemanticHealth     func(context.Context, string) (semantichealth.Report, error)
	Maintain           func(context.Context, string, semantichealth.Request) (semantichealth.Receipt, error)
	RecordFeedback     func(context.Context, string, feedback.Event) (feedback.Receipt, error)
	ConfirmFeedback    func(context.Context, string, feedback.Confirmation) (feedback.ConfirmationReceipt, error)
	PreviewUpgrade     func(string) (instanceupgrade.Plan, error)
	ApplyUpgrade       func(context.Context, string, instanceupgrade.Options) (instanceupgrade.Receipt, error)
	RepairUpgrade      func(context.Context, string, string, instanceupgrade.Options) (instanceupgrade.Receipt, error)
}

func RunWithDependencies(args []string, stdout, stderr io.Writer, build BuildInfo, dependencies Dependencies) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, helpText)
	}

	switch args[0] {
	case "help", "-h", "--help":
		if len(args) != 1 {
			return usageError(stderr, "help does not accept arguments")
		}
		return writeText(stdout, stderr, helpText)
	case "daemon":
		return runDaemon(args[1:], stdout, stderr, dependencies)
	case "assimilate":
		return runAssimilate(args[1:], stdout, stderr, dependencies)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, dependencies)
	case "exec", "query":
		return runExecute(args[0], args[1:], stdout, stderr, dependencies)
	case "export":
		return runWikiExport(args[1:], stdout, stderr, dependencies)
	case "feedback":
		return runFeedback(args[1:], stdout, stderr, dependencies)
	case "init":
		return runInit(args[1:], stdout, stderr, dependencies)
	case "mutate":
		return runMutate(args[1:], stdout, stderr, dependencies)
	case "pack", "open", "install":
		return runDatabasePackage(args[0], args[1:], stdout, stderr, dependencies)
	case "maintain":
		return runMaintain(args[1:], stdout, stderr, dependencies)
	case "parse":
		return runParse(args[1:], stdout, stderr, dependencies)
	case "reflect":
		return runReflect(args[1:], stdout, stderr, dependencies)
	case "schema":
		return runSchema(args[1:], stdout, stderr, dependencies)
	case "upgrade":
		return runUpgrade(args[1:], stdout, stderr, dependencies)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
}

func runWikiExport(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var root, profilePath string
	var databases []string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--wiki":
			if index+1 >= len(args) {
				return usageError(stderr, "--wiki requires an absolute Vault path")
			}
			root = args[index+1]
			index++
		case "--profile":
			if index+1 >= len(args) {
				return usageError(stderr, "--profile requires a JSON file")
			}
			profilePath = args[index+1]
			index++
		case "--database":
			if index+1 >= len(args) {
				return usageError(stderr, "--database requires a Database name or ID")
			}
			databases = append(databases, args[index+1])
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown export option: %q", args[index]))
		}
	}
	if root == "" || profilePath == "" || len(databases) == 0 {
		return usageError(stderr, "export requires --wiki VAULT, --profile PROFILE.json, and at least one --database")
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return usageError(stderr, "--wiki must be an absolute normalized path")
	}
	if err := security.ValidateOutputRoot(root); err != nil {
		return usageError(stderr, err.Error())
	}
	profile, err := os.ReadFile(profilePath)
	if err != nil {
		return commandError(stderr, "read Wiki Export Profile", err)
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	envelope, err := execute(
		context.Background(), dataDir, "EXPORT WIKI TO :path PROFILE :profile",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"path": root, "profile": string(profile),
			}},
			Authorization: security.Authorization{
				Version: security.AuthorizationVersion, Actor: "user:local",
				AuthorizedDatabases: databases,
			},
		}},
	)
	if err != nil {
		return commandError(stderr, "export Wiki", err)
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return writeFailure(stderr, err)
	}
	if !envelope.OK {
		return ExitFailure
	}
	return ExitOK
}

func runDatabasePackage(command string, args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var subject, output, author string
	trusted := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--output", "-o":
			if command != "pack" || index+1 >= len(args) {
				return usageError(stderr, "--output is valid for pack and requires a path")
			}
			output = args[index+1]
			index++
		case "--by":
			if command != "pack" || index+1 >= len(args) {
				return usageError(stderr, "--by is valid for pack and requires an author declaration")
			}
			author = args[index+1]
			index++
		case "--trusted":
			if command != "install" {
				return usageError(stderr, "--trusted is only valid for install")
			}
			trusted = true
		default:
			if subject != "" {
				return usageError(stderr, command+" accepts exactly one Database name or package path")
			}
			subject = args[index]
		}
	}
	if subject == "" {
		return usageError(stderr, command+" requires a Database name or package path")
	}
	if command == "pack" && (output == "" || author == "") {
		return usageError(stderr, "pack requires --output PATH and --by AUTHOR")
	}
	if command == "pack" && (!filepath.IsAbs(output) || filepath.Clean(output) != output) {
		return usageError(stderr, "pack --output must be an absolute normalized path")
	}
	if command == "pack" {
		if err := security.ValidateOutputRoot(filepath.Dir(output)); err != nil {
			return usageError(stderr, err.Error())
		}
	}
	if command == "install" && !trusted {
		return usageError(stderr, "install requires explicit --trusted consent")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	ctx := context.Background()
	var source string
	var statements []executor.StatementInput
	if command == "pack" {
		source = "PACK DATABASE " + quoteMSQLIdentifier(subject) + " BY :author"
		statements = []executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{"author": author}},
			Authorization: security.Authorization{
				Version: security.AuthorizationVersion, Actor: "user:local",
				AuthorizedDatabases: []string{subject},
			},
		}}
	} else {
		encoded, err := os.ReadFile(subject)
		if err != nil {
			return commandError(stderr, "read database package", err)
		}
		source = "OPEN PACKAGE :package READ ONLY"
		if command == "install" {
			source = "INSTALL PACKAGE :package TRUSTED"
		}
		input := executor.StatementInput{Parameters: executor.Parameters{Named: map[string]any{"package": string(encoded)}}}
		if command == "install" {
			input.Authorization = security.Authorization{
				Version: security.AuthorizationVersion, Actor: "user:local",
				Approval: &security.Approval{
					Version: security.ApprovalVersion, Action: security.ActionInstallPackage,
					SubjectSHA256: dbpackage.Hash(encoded), Confirmed: true,
				},
			}
		}
		statements = []executor.StatementInput{input}
	}
	envelope, err := execute(ctx, dataDir, source, statements)
	if err != nil {
		return commandError(stderr, command+" database package", err)
	}
	if !envelope.OK {
		if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitFailure
	}
	if command != "pack" {
		if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	}
	if len(envelope.Results) != 1 || len(envelope.Results[0].Rows) != 1 {
		return commandError(stderr, "pack database package", errors.New("MSQL response did not contain one package"))
	}
	encoded, ok := envelope.Results[0].Rows[0]["package"].(string)
	if !ok || encoded == "" {
		return commandError(stderr, "pack database package", errors.New("MSQL response package is invalid"))
	}
	if err := writePackageFile(output, []byte(encoded)); err != nil {
		return commandError(stderr, "write database package", err)
	}
	return writeJSON(stdout, stderr, map[string]any{
		"path": output, "package_sha256": envelope.Results[0].Rows[0]["package_sha256"],
	})
}

func quoteMSQLIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func writePackageFile(path string, content []byte) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("output path must be absolute and normalized")
	}
	if _, err := os.Stat(path); err == nil {
		return errors.New("output path already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	directory := filepath.Dir(path)
	if err := security.ValidateOutputRoot(directory); err != nil {
		return err
	}
	destination, err := security.SecureJoin(directory, filepath.Base(path))
	if err != nil {
		return err
	}
	if destination != path {
		return errors.New("output path escapes its authorized directory")
	}
	temporary, err := os.CreateTemp(directory, ".memora-package-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	if err := json.NewEncoder(stdout).Encode(value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func runFeedback(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var eventJSON, confirmationJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--event":
			if index+1 >= len(args) {
				return usageError(stderr, "--event requires a JSON object")
			}
			if eventJSON != "" {
				return usageError(stderr, "--event may only be specified once")
			}
			eventJSON = args[index+1]
			index++
		case "--confirmation":
			if index+1 >= len(args) {
				return usageError(stderr, "--confirmation requires a JSON object")
			}
			if confirmationJSON != "" {
				return usageError(stderr, "--confirmation may only be specified once")
			}
			confirmationJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown feedback option: %q", args[index]))
		}
	}
	if (eventJSON == "") == (confirmationJSON == "") {
		return usageError(stderr, "feedback requires exactly one of --event or --confirmation")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if eventJSON != "" {
		var event feedback.Event
		decoder := json.NewDecoder(bytes.NewBufferString(eventJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return usageError(stderr, "--event must be one strict Feedback Event JSON object")
		}
		record := dependencies.RecordFeedback
		if record == nil {
			record = daemon.RecordFeedback
		}
		receipt, err := record(context.Background(), dataDir, event)
		if err != nil {
			return commandError(stderr, "record feedback", err)
		}
		if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	}
	var confirmation feedback.Confirmation
	decoder := json.NewDecoder(bytes.NewBufferString(confirmationJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&confirmation); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--confirmation must be one strict Feedback Confirmation JSON object")
	}
	confirm := dependencies.ConfirmFeedback
	if confirm == nil {
		confirm = daemon.ConfirmFeedback
	}
	receipt, err := confirm(context.Background(), dataDir, confirmation)
	if err != nil {
		return commandError(stderr, "confirm feedback revision", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if receipt.Status == "committed_unverified" {
		return ExitFailure
	}
	return ExitOK
}

func runAssimilate(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var eventJSON string
	var submissionJSON string
	var receiptID string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--event":
			if index+1 >= len(args) {
				return usageError(stderr, "--event requires a JSON object")
			}
			if eventJSON != "" {
				return usageError(stderr, "--event may only be specified once")
			}
			eventJSON = args[index+1]
			index++
		case "--submission":
			if index+1 >= len(args) {
				return usageError(stderr, "--submission requires a JSON object")
			}
			if submissionJSON != "" {
				return usageError(stderr, "--submission may only be specified once")
			}
			submissionJSON = args[index+1]
			index++
		case "--receipt":
			if index+1 >= len(args) {
				return usageError(stderr, "--receipt requires a submission ID")
			}
			if receiptID != "" {
				return usageError(stderr, "--receipt may only be specified once")
			}
			receiptID = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown assimilate option: %q", args[index]))
		}
	}
	selected := 0
	for _, value := range []string{eventJSON, submissionJSON, receiptID} {
		if value != "" {
			selected++
		}
	}
	if selected != 1 {
		return usageError(stderr, "assimilate requires exactly one of --event, --submission, or --receipt")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if receiptID != "" {
		load := dependencies.GetSourceReceipt
		if load == nil {
			load = daemon.SourceReceipt
		}
		receipt, err := load(context.Background(), dataDir, receiptID)
		if err != nil {
			return commandError(stderr, "read Source Receipt", err)
		}
		if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
			return writeFailure(stderr, err)
		}
		if receipt.Status != assimilation.SubmissionCommitted {
			return ExitFailure
		}
		return ExitOK
	}
	if submissionJSON != "" {
		var submission assimilation.Submission
		decoder := json.NewDecoder(bytes.NewBufferString(submissionJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&submission); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
			return usageError(stderr, "--submission must be one strict Assimilation Submission JSON object")
		}
		submit := dependencies.SubmitAssimilation
		if submit == nil {
			submit = daemon.SubmitAssimilation
		}
		receipt, err := submit(context.Background(), dataDir, submission)
		if err != nil {
			return commandError(stderr, "submit reviewed assimilation", err)
		}
		if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
			return writeFailure(stderr, err)
		}
		if receipt.Status != assimilation.SubmissionCommitted {
			return ExitFailure
		}
		return ExitOK
	}
	var event assimilation.Event
	decoder := json.NewDecoder(bytes.NewBufferString(eventJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--event must be one strict Assimilation Event JSON object")
	}
	process := dependencies.Assimilate
	if process == nil {
		process = daemon.Assimilate
	}
	receipt, err := process(context.Background(), dataDir, event)
	if err != nil {
		return commandError(stderr, "record assimilation coverage", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if receipt.Status == assimilation.StatusIncomplete {
		return ExitFailure
	}
	return ExitOK
}

func runMaintain(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var requestJSON string
	report := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--report":
			report = true
		case "--request":
			if index+1 >= len(args) {
				return usageError(stderr, "--request requires a JSON object")
			}
			requestJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown maintain option: %q", args[index]))
		}
	}
	if report == (requestJSON != "") {
		return usageError(stderr, "maintain requires exactly one of --report or --request")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if report {
		inspect := dependencies.SemanticHealth
		if inspect == nil {
			inspect = daemon.SemanticHealth
		}
		value, err := inspect(context.Background(), dataDir)
		if err != nil {
			return commandError(stderr, "inspect semantic health", err)
		}
		if err := json.NewEncoder(stdout).Encode(value); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	}
	var request semantichealth.Request
	decoder := json.NewDecoder(bytes.NewBufferString(requestJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--request must be one strict Maintenance Request JSON object")
	}
	maintain := dependencies.Maintain
	if maintain == nil {
		maintain = daemon.Maintain
	}
	receipt, err := maintain(context.Background(), dataDir, request)
	if err != nil {
		return commandError(stderr, "maintain semantic database", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func runReflect(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var eventJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--event":
			if index+1 >= len(args) {
				return usageError(stderr, "--event requires a JSON object")
			}
			if eventJSON != "" {
				return usageError(stderr, "--event may only be specified once")
			}
			eventJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown reflect option: %q", args[index]))
		}
	}
	if eventJSON == "" {
		return usageError(stderr, "reflect requires --event JSON")
	}
	var event conversation.Event
	decoder := json.NewDecoder(bytes.NewBufferString(eventJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&event); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--event must be one strict Conversation Event JSON object")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	reflectEvent := dependencies.Reflect
	if reflectEvent == nil {
		reflectEvent = daemon.Reflect
	}
	receipt, err := reflectEvent(context.Background(), dataDir, event)
	if err != nil {
		return commandError(stderr, "reflect conversation event", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if receipt.Status == conversation.StatusNeedsContext {
		return ExitFailure
	}
	return ExitOK
}

func runSchema(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var planJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--plan":
			if index+1 >= len(args) {
				return usageError(stderr, "--plan requires a JSON object")
			}
			if planJSON != "" {
				return usageError(stderr, "--plan may only be specified once")
			}
			planJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown schema option: %q", args[index]))
		}
	}
	if planJSON == "" {
		return usageError(stderr, "schema requires --plan JSON")
	}
	var plan skillschema.Plan
	decoder := json.NewDecoder(bytes.NewBufferString(planJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--plan must be one strict Schema Plan JSON object")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	tool := skillschema.ToolFunc(func(ctx context.Context, call skillschema.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements)
	})
	report, err := skillschema.New(tool).Run(context.Background(), plan)
	if err != nil {
		if report.Receipt.Version != "" {
			if encodeErr := json.NewEncoder(stdout).Encode(report.Receipt); encodeErr != nil {
				return writeFailure(stderr, encodeErr)
			}
		}
		return commandError(stderr, "execute Schema Plan", err)
	}
	if err := json.NewEncoder(stdout).Encode(report.Receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if !report.Receipt.Verified {
		return ExitFailure
	}
	return ExitOK
}

func runExecute(
	command string,
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var source, inputJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--input":
			if index+1 >= len(args) {
				return usageError(stderr, "--input requires a JSON object")
			}
			if inputJSON != "" {
				return usageError(stderr, "--input may only be specified once")
			}
			inputJSON = args[index+1]
			index++
		default:
			if source != "" {
				return usageError(stderr, command+" accepts exactly one MSQL source argument")
			}
			source = args[index]
		}
	}
	if source == "" {
		return usageError(stderr, command+" requires an MSQL source argument")
	}
	if command == "query" {
		if batch, err := parser.ParseBatch(source); err == nil {
			for _, statement := range batch.Statements {
				if !queryStatement(statement.Kind) {
					return usageError(
						stderr,
						"query only accepts SHOW, DESCRIBE, SELECT, or OPEN ROUTE",
					)
				}
			}
		}
	}
	statements := []executor.StatementInput{}
	if inputJSON != "" {
		var input executor.StatementInput
		decoder := json.NewDecoder(bytes.NewBufferString(inputJSON))
		decoder.DisallowUnknownFields()
		decoder.UseNumber()
		if err := decoder.Decode(&input); err != nil ||
			decoder.Decode(&struct{}{}) != io.EOF {
			return usageError(stderr, "--input must be one strict StatementInput JSON object")
		}
		statements = append(statements, input)
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	envelope, err := execute(context.Background(), dataDir, source, statements)
	if err != nil {
		return commandError(stderr, command+" MSQL", err)
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return writeFailure(stderr, err)
	}
	if !envelope.OK {
		return ExitFailure
	}
	return ExitOK
}

func runMutate(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var planJSON string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--plan":
			if index+1 >= len(args) {
				return usageError(stderr, "--plan requires a JSON object")
			}
			if planJSON != "" {
				return usageError(stderr, "--plan may only be specified once")
			}
			planJSON = args[index+1]
			index++
		default:
			return usageError(stderr, fmt.Sprintf("unknown mutate option: %q", args[index]))
		}
	}
	if planJSON == "" {
		return usageError(stderr, "mutate requires --plan JSON")
	}
	var plan skillwrite.Plan
	decoder := json.NewDecoder(bytes.NewBufferString(planJSON))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&plan); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return usageError(stderr, "--plan must be one strict Mutation Plan JSON object")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = daemon.Execute
	}
	tool := skillwrite.ToolFunc(func(ctx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements)
	})
	report, err := skillwrite.New(tool).Run(context.Background(), plan)
	if err != nil {
		return commandError(stderr, "execute Mutation Plan", err)
	}
	if err := json.NewEncoder(stdout).Encode(report.Receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if report.Receipt.Status == skillwrite.ReceiptCommittedUnverified {
		return ExitFailure
	}
	return ExitOK
}

func queryStatement(kind string) bool {
	switch kind {
	case "SHOW", "DESCRIBE", "SELECT", "OPEN_ROUTE":
		return true
	default:
		return false
	}
}

func runDoctor(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	if len(args) > 0 && args[0] == "repair" {
		return runDoctorRepair(args[1:], stdout, stderr, dependencies)
	}
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	report, err := daemon.Doctor(context.Background(), dataDir)
	if err != nil {
		return commandError(stderr, "inspect database integrity", err)
	}
	if err := json.NewEncoder(stdout).Encode(report); err != nil {
		return writeFailure(stderr, err)
	}
	if report.Status != "healthy" {
		return ExitFailure
	}
	return ExitOK
}

func runUpgrade(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	planRequested := false
	applyRequested := false
	approved := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--plan":
			planRequested = true
		case "--apply":
			applyRequested = true
		case "--yes":
			approved = true
		default:
			return usageError(stderr, fmt.Sprintf("unknown upgrade option: %q", args[index]))
		}
	}
	if planRequested == applyRequested {
		return usageError(stderr, "upgrade requires exactly one of --plan or --apply")
	}
	if planRequested && approved {
		return usageError(stderr, "--yes is only valid with upgrade --apply")
	}
	if applyRequested && !approved {
		return usageError(stderr, "upgrade --apply requires --yes after explicit approval")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if planRequested {
		preview := dependencies.PreviewUpgrade
		if preview == nil {
			preview = instanceupgrade.Preview
		}
		plan, err := preview(dataDir)
		if err != nil {
			return commandError(stderr, "plan Instance upgrade", err)
		}
		if err := json.NewEncoder(stdout).Encode(plan); err != nil {
			return writeFailure(stderr, err)
		}
		return ExitOK
	}
	apply := dependencies.ApplyUpgrade
	if apply == nil {
		apply = instanceupgrade.Apply
	}
	receipt, err := apply(context.Background(), dataDir, instanceupgrade.Options{
		Approved: true, Clock: dependencies.Clock, IDs: dependencies.IDs,
	})
	if err != nil {
		return commandError(stderr, "upgrade Instance", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func runDoctorRepair(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	var daemonArgs []string
	var backup string
	approved := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--backup":
			if index+1 >= len(args) || backup != "" {
				return usageError(stderr, "--backup requires one absolute path")
			}
			backup = args[index+1]
			index++
		case "--yes":
			approved = true
		default:
			return usageError(stderr, fmt.Sprintf("unknown doctor repair option: %q", args[index]))
		}
	}
	if !approved {
		return usageError(stderr, "doctor repair requires --yes after explicit approval")
	}
	if backup != "" && !filepath.IsAbs(backup) {
		return usageError(stderr, "--backup must be an absolute path")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	repair := dependencies.RepairUpgrade
	if repair == nil {
		repair = instanceupgrade.Repair
	}
	cleanBackup := ""
	if backup != "" {
		cleanBackup = filepath.Clean(backup)
	}
	receipt, err := repair(context.Background(), dataDir, cleanBackup, instanceupgrade.Options{
		Approved: true, Clock: dependencies.Clock, IDs: dependencies.IDs,
	})
	if err != nil {
		return commandError(stderr, "repair Instance format", err)
	}
	if err := json.NewEncoder(stdout).Encode(receipt); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func runParse(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var source string
	for index := 0; index < len(args); index++ {
		if args[index] == "--data-dir" {
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
			continue
		}
		if source != "" {
			return usageError(stderr, "parse accepts exactly one MSQL source argument")
		}
		source = args[index]
	}
	if source == "" {
		return usageError(stderr, "parse requires an MSQL source argument")
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	response, err := daemon.Parse(context.Background(), dataDir, source)
	if err != nil {
		return commandError(stderr, "parse MSQL", err)
	}
	if err := json.NewEncoder(stdout).Encode(response); err != nil {
		return writeFailure(stderr, err)
	}
	if !response.OK {
		return ExitFailure
	}
	return ExitOK
}

func runDaemon(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, "daemon requires start, run, status, ping, or stop")
	}
	action := args[0]
	dataDir, code := daemonDataDir(args[1:], stderr, dependencies)
	if code != ExitOK {
		return code
	}
	switch action {
	case "start":
		if _, err := instance.Read(dataDir); err != nil {
			return commandError(stderr, "open instance", err)
		}
		executable, err := os.Executable()
		if err != nil {
			return commandError(stderr, "resolve executable", err)
		}
		state, err := daemon.Start(context.Background(), executable, dataDir)
		if err != nil {
			return commandError(stderr, "start daemon", err)
		}
		return writeText(stdout, stderr, fmt.Sprintf("Memora daemon started with PID %d\n", state.PID))
	case "run":
		if _, err := instance.Read(dataDir); err != nil {
			return commandError(stderr, "open instance", err)
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := daemon.Run(ctx, dataDir, nil); err != nil {
			return commandError(stderr, "run daemon", err)
		}
		return ExitOK
	case "status":
		state, err := daemon.Inspect(dataDir)
		if err != nil {
			return commandError(stderr, "inspect daemon", err)
		}
		if state.Running {
			return writeText(stdout, stderr, fmt.Sprintf("Memora daemon is running with PID %d\n", state.PID))
		}
		return writeText(stdout, stderr, "Memora daemon is stopped\n")
	case "ping":
		if err := daemon.Ping(context.Background(), dataDir); err != nil {
			return commandError(stderr, "ping daemon", err)
		}
		return writeText(stdout, stderr, "pong\n")
	case "stop":
		if err := daemon.Stop(context.Background(), dataDir); err != nil {
			return commandError(stderr, "stop daemon", err)
		}
		return writeText(stdout, stderr, "Memora daemon stopped\n")
	default:
		return usageError(stderr, fmt.Sprintf("unknown daemon action: %q", action))
	}
}

func daemonDataDir(args []string, stderr io.Writer, dependencies Dependencies) (string, int) {
	var dataDirOverride *string
	for index := 0; index < len(args); index++ {
		if args[index] != "--data-dir" {
			return "", usageError(stderr, fmt.Sprintf("unknown daemon option: %q", args[index]))
		}
		index++
		if index >= len(args) {
			return "", usageError(stderr, "--data-dir requires a path")
		}
		value := args[index]
		if !filepath.IsAbs(value) {
			return "", usageError(stderr, "--data-dir must be an absolute path")
		}
		dataDirOverride = &value
	}
	homeDir := dependencies.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return "", commandError(stderr, "resolve user home", err)
	}
	configFile := ""
	defaultConfig, err := config.DefaultFile(home)
	if err != nil {
		return "", commandError(stderr, "resolve config file", err)
	}
	if _, statErr := os.Stat(defaultConfig); statErr == nil {
		configFile = defaultConfig
	} else if !os.IsNotExist(statErr) {
		return "", commandError(stderr, "inspect config file", statErr)
	}
	loaded, err := config.Load(config.LoadOptions{
		ConfigFile: configFile,
		LookupEnv:  dependencies.LookupEnv,
		Overrides:  config.Overrides{DataDir: dataDirOverride},
	})
	if err != nil {
		return "", commandError(stderr, "load config", err)
	}
	locations, err := instance.DefaultLocations(home, loaded.InstanceName, loaded.DataDir)
	if err != nil {
		return "", commandError(stderr, "resolve daemon data directory", err)
	}
	return locations.DataDir, ExitOK
}

func runInit(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var instanceOverride *string
	var dataDirOverride *string
	var logLevelOverride *string
	var configFileOverride *string
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			index++
			if index >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			value := args[index]
			dataDirOverride = &value
			if !filepath.IsAbs(value) {
				return usageError(stderr, "--data-dir must be an absolute path")
			}
		case "--instance":
			index++
			if index >= len(args) {
				return usageError(stderr, "--instance requires a name")
			}
			value := args[index]
			instanceOverride = &value
		case "--log-level":
			index++
			if index >= len(args) {
				return usageError(stderr, "--log-level requires a value")
			}
			value := args[index]
			logLevelOverride = &value
		case "--config":
			index++
			if index >= len(args) {
				return usageError(stderr, "--config requires a path")
			}
			value := args[index]
			if !filepath.IsAbs(value) {
				return usageError(stderr, "--config must be an absolute path")
			}
			configFileOverride = &value
		default:
			return usageError(stderr, fmt.Sprintf("unknown option for init: %q", args[index]))
		}
	}

	homeDir := dependencies.HomeDir
	if homeDir == nil {
		homeDir = os.UserHomeDir
	}
	home, err := homeDir()
	if err != nil {
		return commandError(stderr, "resolve user home", err)
	}
	configFile := ""
	if configFileOverride != nil {
		configFile = *configFileOverride
	} else {
		defaultConfig, configErr := config.DefaultFile(home)
		if configErr != nil {
			return commandError(stderr, "resolve config file", configErr)
		}
		if _, statErr := os.Stat(defaultConfig); statErr == nil {
			configFile = defaultConfig
		} else if !os.IsNotExist(statErr) {
			return commandError(stderr, "inspect config file", statErr)
		}
	}
	loaded, err := config.Load(config.LoadOptions{
		ConfigFile: configFile,
		LookupEnv:  dependencies.LookupEnv,
		Overrides: config.Overrides{
			InstanceName: instanceOverride,
			DataDir:      dataDirOverride,
			LogLevel:     logLevelOverride,
		},
	})
	if err != nil {
		return commandError(stderr, "load config", err)
	}
	locations, err := instance.DefaultLocations(home, loaded.InstanceName, loaded.DataDir)
	if err != nil {
		return usageError(stderr, err.Error())
	}
	result, err := instance.Initialize(context.Background(), locations.DataDir, instance.Options{
		Clock: dependencies.Clock,
		IDs:   dependencies.IDs,
	})
	if err != nil {
		return commandError(stderr, "initialize instance", err)
	}
	if result.Created {
		return writeText(stdout, stderr, fmt.Sprintf(
			"Initialized Memora instance %s at %s\n",
			result.Metadata.InstanceID,
			locations.DataDir,
		))
	}
	return writeText(stdout, stderr, fmt.Sprintf(
		"Memora instance %s already initialized at %s\n",
		result.Metadata.InstanceID,
		locations.DataDir,
	))
}

func runVersion(args []string, stdout, stderr io.Writer, build BuildInfo) int {
	if len(args) == 0 {
		return writeText(stdout, stderr, fmt.Sprintf("memora %s (%s)\n", build.Version, build.Commit))
	}
	if len(args) != 1 || args[0] != "--json" {
		option := args[0]
		return usageError(stderr, fmt.Sprintf("unknown option for version: %q", option))
	}

	result := versionOutput{
		Name:    "memora",
		Version: build.Version,
		Commit:  build.Commit,
		BuiltAt: build.BuiltAt,
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func usageError(stderr io.Writer, message string) int {
	if _, err := fmt.Fprintf(stderr, "memora: %s\n", message); err != nil {
		return ExitFailure
	}
	return ExitUsage
}

func commandError(stderr io.Writer, action string, err error) int {
	message := security.Redact(err.Error())
	if _, writeErr := fmt.Fprintf(stderr, "memora: %s: %s\n", action, message); writeErr != nil {
		return ExitFailure
	}
	return ExitFailure
}

func writeText(stdout, stderr io.Writer, value string) int {
	if _, err := io.WriteString(stdout, value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
}

func writeFailure(stderr io.Writer, err error) int {
	_, _ = fmt.Fprintf(stderr, "memora: write output: %v\n", err)
	return ExitFailure
}
