package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/HW-Yue/Memora/internal/adminapi"
	"github.com/HW-Yue/Memora/internal/config"
	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/embedding"
	"github.com/HW-Yue/Memora/internal/instance"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/mcpadapter"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/skillschema"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

const helpText = `Memora is an AI-maintained local personal database (SQLite).

Usage:
  memora <command> [options]

Commands:
  admin      Start a temporary local read-only Admin API
  daemon     Manage the local daemon
  doctor     Verify logical database integrity
  exec       Execute MSQL through the local daemon
  help       Show this help
  init       Initialize a local instance
  instance   Remove a local instance (irreversible; needs --yes)
  mcp        Serve MCP over newline-delimited stdio
  mutate     Execute a validated Mutation Plan
  parse      Parse an MSQL request through the local daemon
  query      Query MSQL through the local daemon
  schema     Execute a validated Schema Plan
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
		Stdin:   os.Stdin,
		UserID:  os.Getuid,
	})
}

// defaultExecute is the daemon round trip, with the read-only policy asked of the
// daemon rather than decided here.
// ensureDaemon starts the daemon for an instance when nothing is serving it.
//
// The line is deliberate: nobody running → start (idempotent, safe); somebody
// running → leave it alone. Restarting a live daemon would kill other sessions'
// in-flight work, and a version mismatch is not a reason to do that — it is a
// reason to say so.
func ensureDaemon(ctx context.Context, dataDir string, stderr io.Writer, dependencies Dependencies) error {
	state, err := daemon.Inspect(dataDir)
	if err != nil {
		return err
	}
	if state.Running {
		return nil
	}
	resolve := dependencies.Executable
	if resolve == nil {
		resolve = os.Executable
	}
	executable, err := resolve()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	started, err := daemon.Start(ctx, executable, dataDir)
	if err != nil && !errors.Is(err, daemon.ErrAlreadyRunning) {
		return fmt.Errorf("start daemon: %w", err)
	}
	if _, err := fmt.Fprintf(stderr, "memora: started the instance's daemon (pid %d); it was not running\n", started.PID); err != nil {
		return err
	}
	return nil
}

func defaultExecute(ctx context.Context, dataDir, source string, inputs []executor.StatementInput, readOnly bool) (result.Envelope, error) {
	if readOnly {
		return daemon.ExecuteReadOnly(ctx, dataDir, source, inputs)
	}
	return daemon.Execute(ctx, dataDir, source, inputs)
}

// withoutReadOnly adapts the daemon round trip to transports that decide their own
// policy (the admin API validates reads itself) and therefore carry no mode.
// decodeStatementInputs accepts one StatementInput object or a list of them.
//
// The language carries a batch as several statements, and each statement may need
// its own parameters, mutation and authorization — the Admin gateway has always
// sent them as an array. Refusing the array here made "batch = a batch of
// statements" unreachable from the CLI, so the only way to offer N vectors was N
// round trips. Both shapes stay strict: unknown fields and trailing content are
// rejected either way.
func decodeStatementInputs(source string) ([]executor.StatementInput, error) {
	trimmed := bytes.TrimSpace([]byte(source))
	if len(trimmed) == 0 {
		return nil, errors.New("the input is empty")
	}
	if trimmed[0] == '[' {
		inputs := []executor.StatementInput{}
		if err := decodeStrict(source, &inputs); err != nil {
			return nil, err
		}
		if len(inputs) == 0 {
			return nil, errors.New("an input array must carry at least one statement input")
		}
		return inputs, nil
	}
	input := executor.StatementInput{}
	if err := decodeStrict(source, &input); err != nil {
		return nil, err
	}
	return []executor.StatementInput{input}, nil
}

func decodeStrict(source string, target any) error {
	decoder := json.NewDecoder(bytes.NewBufferString(source))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("the input carries trailing content")
	}
	return nil
}

func withoutReadOnly(execute ExecuteMSQL) func(context.Context, string, string, []executor.StatementInput) (result.Envelope, error) {
	return func(ctx context.Context, dataDir, source string, inputs []executor.StatementInput) (result.Envelope, error) {
		return execute(ctx, dataDir, source, inputs, false)
	}
}

type Dependencies struct {
	HomeDir     func() (string, error)
	LookupEnv   func(string) (string, bool)
	Stdin       io.Reader
	UserID      func() int
	Clock       instance.Clock
	IDs         instance.IDSource
	ExecuteMSQL ExecuteMSQL
	ServeAdmin  func(context.Context, adminapi.Config, func(adminapi.Descriptor) error) error
	OpenBrowser func(string) error
	Executable  func() (string, error)
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
		return runDaemon(args[1:], stdout, stderr, build, dependencies)
	case "admin":
		return runAdmin(args[1:], stdout, stderr, dependencies)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr, dependencies)
	case "exec", "query":
		return runExecute(args[0], args[1:], stdout, stderr, dependencies)
	case "init":
		return runInit(args[1:], stdout, stderr, dependencies)
	case "instance":
		return runInstance(args[1:], stdout, stderr)
	case "mutate":
		return runMutate(args[1:], stdout, stderr, dependencies)
	case "mcp":
		return runMCP(args[1:], stdout, stderr, build, dependencies)
	case "parse":
		return runParse(args[1:], stdout, stderr, dependencies)
	case "schema":
		return runSchema(args[1:], stdout, stderr, dependencies)
	case "version":
		return runVersion(args[1:], stdout, stderr, build)
	default:
		if _, err := fmt.Fprintf(stderr, "memora: unknown command %q\nRun 'memora help' for usage.\n", args[0]); err != nil {
			return ExitFailure
		}
		return ExitUsage
	}
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
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = defaultExecute
	}
	tool := skillschema.ToolFunc(func(ctx context.Context, call skillschema.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements, false)
	})
	report, err := skillschema.New(tool).Run(context.Background(), plan)
	if err != nil {
		if report.Receipt.Version != "" {
			if encodeErr := json.NewEncoder(stdout).Encode(report.Receipt); encodeErr != nil {
				return writeFailure(stderr, encodeErr)
			}
		}
		return daemonFailure(stderr, dataDir, "execute Schema Plan", err)
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
	statements := []executor.StatementInput{}
	if inputJSON != "" {
		inputs, err := decodeStatementInputs(inputJSON)
		if err != nil {
			return usageError(stderr,
				"--input must be one strict StatementInput JSON object, or an array of them: "+err.Error())
		}
		statements = inputs
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = defaultExecute
	}
	envelope, err := execute(context.Background(), dataDir, source, statements, command == "query")
	if err != nil {
		return daemonFailure(stderr, dataDir, command+" MSQL", err)
	}
	if err := json.NewEncoder(stdout).Encode(envelope); err != nil {
		return writeFailure(stderr, err)
	}
	if !envelope.OK {
		return ExitFailure
	}
	// The write is committed and reported; the host's half of the vector path
	// runs after it and can never change that. A host without a provider has
	// nothing to do here, and a host with a broken one hears about it without
	// losing the fact it just recorded.
	if command == "exec" && len(statements) == 1 {
		if err := drainAfterWrite(context.Background(), dataDir, statements[0], execute, dependencies, stderr); err != nil {
			_, _ = fmt.Fprintf(stderr, "embeddings: %v; the units stay not-ready\n", err)
		}
	}
	return ExitOK
}

// drainAfterWrite runs the drain only when this host has an embedding provider.
func drainAfterWrite(
	ctx context.Context,
	dataDir string,
	caller executor.StatementInput,
	execute ExecuteMSQL,
	dependencies Dependencies,
	stderr io.Writer,
) error {
	lookup := dependencies.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}
	embedder, config, err := embedding.NewFromEnvLookup(func(name string) string {
		value, _ := lookup(name)
		return value
	})
	if err != nil {
		// A partly configured provider is worth saying out loud, and it is not a
		// reason to fail the write that already happened.
		return err
	}
	if embedder == nil {
		return nil
	}
	drainEmbeddings(ctx, dataDir, caller, execute, embedder, config.Batch, stderr)
	return nil
}

func runAdmin(args []string, stdout, stderr io.Writer, dependencies Dependencies) int {
	var daemonArgs []string
	var scopes []string
	noOpen := false
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--data-dir":
			if index+1 >= len(args) {
				return usageError(stderr, "--data-dir requires a path")
			}
			daemonArgs = append(daemonArgs, args[index], args[index+1])
			index++
		case "--scope":
			if index+1 >= len(args) {
				return usageError(stderr, "--scope requires a Database name or ID")
			}
			scopes = append(scopes, args[index+1])
			index++
		case "--no-open":
			if noOpen {
				return usageError(stderr, "--no-open may only be specified once")
			}
			noOpen = true
		default:
			return usageError(stderr, fmt.Sprintf("unknown admin option: %q", args[index]))
		}
	}
	if len(scopes) != 0 {
		authorization := security.Authorization{
			Version:             security.AuthorizationVersion,
			Actor:               "user:admin",
			AuthorizedDatabases: scopes,
		}
		if err := authorization.Validate(); err != nil {
			return usageError(stderr, "admin scope is invalid")
		}
	}
	dataDir, code := daemonDataDir(daemonArgs, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = defaultExecute
	}
	serve := dependencies.ServeAdmin
	if serve == nil {
		serve = serveAdmin
	}
	openBrowser := dependencies.OpenBrowser
	if openBrowser == nil {
		openBrowser = openSystemBrowser
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	err := serve(ctx, adminapi.Config{
		DataDir: dataDir,
		Scopes:  append([]string(nil), scopes...),
		Execute: withoutReadOnly(execute),
	}, func(descriptor adminapi.Descriptor) error {
		if err := json.NewEncoder(stdout).Encode(descriptor); err != nil {
			return err
		}
		if noOpen {
			return nil
		}
		return openBrowser(descriptor.URL)
	})
	if err != nil {
		return commandError(stderr, "serve Admin API", err)
	}
	return ExitOK
}

func openSystemBrowser(target string) error {
	if err := osexec.Command("open", target).Run(); err != nil {
		return fmt.Errorf("open system browser: %w", err)
	}
	return nil
}

func serveAdmin(
	ctx context.Context,
	config adminapi.Config,
	ready func(adminapi.Descriptor) error,
) error {
	gateway, err := adminapi.Start(ctx, config)
	if err != nil {
		return err
	}
	if err := ready(gateway.Descriptor()); err != nil {
		closeErr := gateway.Close()
		waitErr := gateway.Wait()
		return errors.Join(err, closeErr, waitErr)
	}
	return gateway.Wait()
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
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
	}
	execute := dependencies.ExecuteMSQL
	if execute == nil {
		execute = defaultExecute
	}
	tool := skillwrite.ToolFunc(func(ctx context.Context, call skillwrite.Call) (result.Envelope, error) {
		return execute(ctx, dataDir, call.Request.Source, call.Request.Statements, false)
	})
	report, err := skillwrite.New(tool).Run(context.Background(), plan)
	if err != nil {
		return daemonFailure(stderr, dataDir, "execute Mutation Plan", err)
	}
	if err := json.NewEncoder(stdout).Encode(report.Receipt); err != nil {
		return writeFailure(stderr, err)
	}
	if report.Receipt.Status == skillwrite.ReceiptCommittedUnverified {
		return ExitFailure
	}
	return ExitOK
}

func runDoctor(
	args []string,
	stdout, stderr io.Writer,
	dependencies Dependencies,
) int {
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
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
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
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

func runMCP(args []string, stdout, stderr io.Writer, build BuildInfo, dependencies Dependencies) int {
	dataDir, code := daemonDataDir(args, stderr, dependencies)
	if code != ExitOK {
		return code
	}
	if err := ensureDaemon(context.Background(), dataDir, stderr, dependencies); err != nil {
		return commandError(stderr, "reach the instance", err)
	}
	input := dependencies.Stdin
	if input == nil {
		input = os.Stdin
	}
	execute := dependencies.ExecuteMSQL
	server := mcpadapter.New(mcpadapter.Config{DataDir: dataDir, Version: build.Version, Execute: withoutReadOnly(execute)})
	if err := server.Serve(context.Background(), input, stdout); err != nil {
		return commandError(stderr, "serve MCP stdio", err)
	}
	return ExitOK
}

// daemonStatus answers "which build is serving this instance, and is it the one
// asking?". It asks the daemon itself: an instance directory outlives the process
// that wrote it, so anything recorded on disk is stale after a restart or after a
// binary swapped in place. A daemon that cannot even answer is treated as
// unconfirmed, which is not the same as agreement.
func daemonStatus(ctx context.Context, dataDir string, build BuildInfo, state daemon.State) (map[string]any, error) {
	report := map[string]any{
		"running": state.Running,
		"pid":     state.PID,
		"cli": map[string]any{
			"version": build.Version, "commit": build.Commit, "built_at": build.BuiltAt,
			"engine_protocol": ipc.EngineProtocol,
		},
		"skewed": false,
	}
	if !state.Running {
		return report, nil
	}
	identity, err := daemonIdentity(ctx, dataDir)
	if err != nil {
		report["skewed"] = true
		report["daemon_error"] = security.Redact(err.Error())
		return report, nil
	}
	report["daemon"] = map[string]any{
		"version": identity.Version, "commit": identity.Commit, "built_at": identity.BuiltAt,
		"engine_protocol": identity.EngineProtocol,
	}
	report["skewed"] = identity.Commit != build.Commit || identity.EngineProtocol != ipc.EngineProtocol
	return report, nil
}

// daemonIdentity asks the running daemon what it is.
func daemonIdentity(ctx context.Context, dataDir string) (daemon.Identity, error) {
	path, err := daemon.SocketPath(dataDir)
	if err != nil {
		return daemon.Identity{}, err
	}
	client, err := ipc.Dial(ctx, path)
	if err != nil {
		return daemon.Identity{}, err
	}
	defer func() { _ = client.Close() }()
	identity := daemon.Identity{}
	if err := client.Call(ctx, "build", nil, &identity); err != nil {
		return daemon.Identity{}, err
	}
	return identity, nil
}

func skewNote(report map[string]any) string {
	if skewed, _ := report["skewed"].(bool); skewed {
		return " (SKEWED: the daemon is a different build; restart it with the binary you are running)"
	}
	return ""
}

func runDaemon(args []string, stdout, stderr io.Writer, build BuildInfo, dependencies Dependencies) int {
	if len(args) == 0 {
		return usageError(stderr, "daemon requires start, run, status, ping, or stop")
	}
	action := args[0]
	// --json is a flag of the command, not of the data-dir parser, so it is taken
	// out before that parser sees it.
	rest := make([]string, 0, len(args))
	jsonOutput := false
	for _, argument := range args[1:] {
		if argument == "--json" {
			jsonOutput = true
			continue
		}
		rest = append(rest, argument)
	}
	dataDir, code := daemonDataDir(rest, stderr, dependencies)
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
		if err := daemon.Run(ctx, dataDir, daemon.Identity{
			Version: build.Version, Commit: build.Commit, BuiltAt: build.BuiltAt,
			EngineProtocol: ipc.EngineProtocol,
		}, nil); err != nil {
			return commandError(stderr, "run daemon", err)
		}
		return ExitOK
	case "status":
		state, err := daemon.Inspect(dataDir)
		if err != nil {
			return commandError(stderr, "inspect daemon", err)
		}
		report, err := daemonStatus(context.Background(), dataDir, build, state)
		if err != nil {
			return commandError(stderr, "inspect daemon", err)
		}
		if jsonOutput {
			return writeJSON(stdout, stderr, report)
		}
		if state.Running {
			return writeText(stdout, stderr, fmt.Sprintf("Memora daemon is running with PID %d%s\n",
				state.PID, skewNote(report)))
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

// daemonFailure reports a daemon round trip that failed. A protocol skew gets its
// own shape: the client cannot guess the daemon's syntax, so it refuses and says
// exactly how to fix it — one instance has one daemon, and the fix is to restart
// it with the binary you are running.
func daemonFailure(stderr io.Writer, dataDir, action string, err error) int {
	var skewed *ipc.SkewedError
	if errors.As(err, &skewed) {
		if _, writeErr := fmt.Fprintf(stderr,
			"memora: %s: %s\n"+
				"memora: this client and this instance's daemon are different builds, so the client cannot know the daemon's syntax.\n"+
				"  restart the daemon with the binary you are running:\n"+
				"    memora daemon stop --data-dir %q && memora daemon start --data-dir %q\n",
			action, security.Redact(skewed.Error()), dataDir, dataDir); writeErr != nil {
			return ExitFailure
		}
		return ExitFailure
	}
	return commandError(stderr, action, err)
}

func commandError(stderr io.Writer, action string, err error) int {
	message := security.Redact(err.Error())
	if _, writeErr := fmt.Fprintf(stderr, "memora: %s: %s\n", action, message); writeErr != nil {
		return ExitFailure
	}
	return ExitFailure
}

// writeJSON prints one JSON value on stdout, which is what a tool reads; the
// human reading is left to the caller's other path.
func writeJSON(stdout, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return writeFailure(stderr, err)
	}
	return ExitOK
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
