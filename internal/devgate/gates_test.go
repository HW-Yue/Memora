package devgate

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
}

func TestCIFormatStageSucceeds(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--stage", "format")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("format stage: %v\n%s", err, out)
	}
}

func TestCIStagesMatchSQLiteKernel(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--list")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("list stages: %v\n%s", err, out)
	}
	got := strings.Fields(string(out))
	want := []string{"format", "vet", "lint", "unit", "race", "cgo-build"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("stages = %q, want %q", got, want)
	}
}

func TestCIScriptDoesNotDisableCGO(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts/ci.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(body, []byte("CGO_ENABLED=0 \"$go_command\" build")) {
		t.Fatal("ci.sh still builds with CGO_ENABLED=0; go-sqlite3 would link a mock that cannot open a database")
	}
	if !bytes.Contains(body, []byte("cgo-build")) {
		t.Fatal("ci.sh has no cgo-build stage")
	}
}

// ciStage returns the executable lines of one case arm of scripts/ci.sh.
// Comment lines are dropped: the assertions are about what the stage runs.
func ciStage(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts/ci.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for index, line := range lines {
		if line == "    "+name+")" {
			start = index + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("scripts/ci.sh has no %s stage", name)
	}
	for index := start; index < len(lines); index++ {
		if strings.TrimSpace(lines[index]) == ";;" {
			end := index
			kept := []string{}
			for _, line := range lines[start:end] {
				if trimmed := strings.TrimSpace(line); trimmed == "" || strings.HasPrefix(trimmed, "#") {
					continue
				}
				kept = append(kept, line)
			}
			return strings.Join(kept, "\n")
		}
	}
	t.Fatalf("stage %s is not terminated", name)
	return ""
}

// The cgo decision belongs to each stage, not to the caller's environment.
//
// Go auto-disables cgo for a cross-compile only while CGO_ENABLED is unset, so
// a job-level CGO_ENABLED=1 turns the GOOS sweep into a linux cgo runtime built
// by the host compiler — that is how this gate shipped red once. Conversely the
// stages that really run the kernel must keep cgo on, because go-sqlite3 with
// CGO_ENABLED=0 links a mock that cannot open a database.
func TestCICGOBelongsToStages(t *testing.T) {
	for _, name := range []string{"vet", "lint"} {
		if body := ciStage(t, name); !strings.Contains(body, "CGO_ENABLED=0") {
			t.Errorf("%s sweeps every supported platform and must disable cgo itself", name)
		}
	}
	// cgo-build has its own behavioural test below; these two are the stages
	// that run the kernel without also refusing a disabled cgo themselves.
	for _, name := range []string{"unit", "race"} {
		body := ciStage(t, name)
		if strings.Contains(body, "CGO_ENABLED=0") {
			t.Errorf("%s runs the kernel and must not disable cgo", name)
		}
		if !strings.Contains(body, "CGO_ENABLED=1") {
			t.Errorf("%s must set CGO_ENABLED=1 instead of inheriting it", name)
		}
	}
}

// TestCIScriptSurvivesAmbientCGOEnabled is the behavioural half of the same
// rule: whatever CGO_ENABLED the caller exports, the platform sweep must pass.
func TestCIScriptSurvivesAmbientCGOEnabled(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--stage", "vet")
	cmd.Dir = repoRoot(t)
	env := []string{}
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "CGO_ENABLED=") {
			continue
		}
		env = append(env, entry)
	}
	cmd.Env = append(env, "CGO_ENABLED=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vet stage with ambient CGO_ENABLED=1: %v\n%s", err, out)
	}
}

// TestCIWorkflowLeavesCGOToStages keeps the workflow from re-introducing the
// job-level variable that this rule exists to prevent. Comments may name it —
// an env key may not.
func TestCIWorkflowLeavesCGOToStages(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if comment := strings.Index(line, "#"); comment >= 0 {
			line = line[:comment]
		}
		if strings.Contains(line, "CGO_ENABLED:") {
			t.Fatal("ci.yml sets CGO_ENABLED; the multi-platform sweep needs each stage to decide cgo itself")
		}
	}
}

func TestCGOBuildRefusesDisabledCGO(t *testing.T) {
	cmd := exec.Command("bash", "scripts/ci.sh", "--stage", "cgo-build")
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("cgo-build with CGO_ENABLED=0 succeeded:\n%s", out)
	}
	if !bytes.Contains(out, []byte("CGO_ENABLED=0")) {
		t.Fatalf("cgo-build failure did not mention CGO_ENABLED=0:\n%s", out)
	}
}

func TestReleaseWorkflowDoesNotCallMissingTools(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), ".github/workflows/release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{
		"scripts/publication.sh",
		"scripts/smoke-release.sh",
		"scripts/clean-machine-acceptance.sh",
		"cmd/verify-publication",
		"cmd/validate-release-trigger",
		"cmd/build-publication",
		"cmd/verify-clean-machine-acceptance",
		"cmd/validate-release-draft",
	} {
		if bytes.Contains(body, []byte(missing)) {
			t.Errorf("release.yml still calls missing %s", missing)
		}
	}
}

func TestSkillSurfaceMatchesLiveCLI(t *testing.T) {
	root := repoRoot(t)
	help := cliHelp(t, root)
	deleted := []string{
		"memora assimilate",
		"memora capture",
		"memora decide",
		"memora feedback",
		"memora maintain",
		"memora reflect",
		"memora reindex",
		"memora upgrade",
		"doctor repair",
		"memora service",
		"UNARCHIVE",
		"SHOW ROUTE CANDIDATES",
		"SHOW LEXICAL LOCATIONS",
	}
	skillFiles := []string{
		"skills/memora/SKILL.md",
		"skills/memora/contract.json",
		"skills/memora/host-contract.json",
		"skills/memora/references/product-manual.md",
		"skills/memora/agents/openai.yaml",
		"adapters/codex/.agents/skills/memora/SKILL.md",
		"adapters/codex/.agents/skills/memora/agents/openai.yaml",
		"adapters/codex/.codex/rules/memora.rules",
		"adapters/claude-code/.claude/skills/memora/SKILL.md",
		"scripts/prototype_smoke.py",
		"docs/development/macos-launch-agent-v1.md",
		"internal/adminui/dist/index.html",
		"internal/adminui/dist/assets/app.js",
		"internal/adminui/dist/assets/catalog.js",
	}
	for _, rel := range skillFiles {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, cmd := range deleted {
			if bytes.Contains(body, []byte(cmd)) {
				t.Errorf("%s still teaches %s", rel, cmd)
			}
		}
	}

	shared := []string{
		"SKILL.md",
		"contract.json",
		"host-contract.json",
		"references/product-manual.md",
		"scripts/check.sh",
		"scripts/install.sh",
	}
	for _, rel := range shared {
		canonical, err := os.ReadFile(filepath.Join(root, "skills/memora", rel))
		if err != nil {
			t.Fatal(err)
		}
		for _, copyRel := range []string{
			filepath.Join("adapters/codex/.agents/skills/memora", rel),
			filepath.Join("adapters/claude-code/.claude/skills/memora", rel),
		} {
			got, err := os.ReadFile(filepath.Join(root, copyRel))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(canonical, got) {
				t.Errorf("%s does not match skills/memora/%s", copyRel, rel)
			}
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "skills/memora/contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		AllowedCommands []string `json:"allowed_commands"`
	}
	if err := json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if len(contract.AllowedCommands) == 0 {
		t.Fatal("contract.json has no allowed_commands")
	}
	for _, cmd := range contract.AllowedCommands {
		if !strings.Contains(help, cmd) {
			t.Errorf("contract.json allows %q but CLI help does not list it", cmd)
		}
	}
}

func TestInstallScriptEnablesCGOForSourceBuild(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{
		"skills/memora/scripts/install.sh",
		"adapters/codex/.agents/skills/memora/scripts/install.sh",
		"adapters/claude-code/.claude/skills/memora/scripts/install.sh",
	} {
		body, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(body, []byte(`GOBIN="$work_dir/go-bin" go install`)) {
			t.Errorf("%s go install path does not enable CGO", rel)
		}
		if !bytes.Contains(body, []byte("CGO_ENABLED=1")) {
			t.Errorf("%s has no CGO_ENABLED=1", rel)
		}
	}
}

func TestAdapterManifestsMatchFiles(t *testing.T) {
	root := repoRoot(t)
	type fileEntry struct {
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	type manifest struct {
		CanonicalDigest    string      `json:"canonical_digest"`
		ProtocolDigest     string      `json:"protocol_digest"`
		TaskContractDigest string      `json:"task_contract_digest"`
		Files              []fileEntry `json:"files"`
	}
	cases := []struct {
		dir, skill, contract, host string
	}{
		{
			dir:      "adapters/codex",
			skill:    ".agents/skills/memora/SKILL.md",
			contract: ".agents/skills/memora/contract.json",
			host:     ".agents/skills/memora/host-contract.json",
		},
		{
			dir:      "adapters/claude-code",
			skill:    ".claude/skills/memora/SKILL.md",
			contract: ".claude/skills/memora/contract.json",
			host:     ".claude/skills/memora/host-contract.json",
		},
	}
	for _, tc := range cases {
		raw, err := os.ReadFile(filepath.Join(root, tc.dir, "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		var man manifest
		if err := json.Unmarshal(raw, &man); err != nil {
			t.Fatalf("%s/manifest.json: %v", tc.dir, err)
		}
		for _, f := range man.Files {
			data, err := os.ReadFile(filepath.Join(root, tc.dir, f.Path))
			if err != nil {
				t.Errorf("%s missing %s: %v", tc.dir, f.Path, err)
				continue
			}
			sum := sha256.Sum256(data)
			got := hex.EncodeToString(sum[:])
			if got != f.SHA256 {
				t.Errorf("%s %s sha256 = %s, manifest has %s", tc.dir, f.Path, got, f.SHA256)
			}
		}
		digest := func(rel string) string {
			t.Helper()
			data, err := os.ReadFile(filepath.Join(root, tc.dir, rel))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			return hex.EncodeToString(sum[:])
		}
		if got := digest(tc.skill); got != man.CanonicalDigest {
			t.Errorf("%s canonical_digest = %s, SKILL.md is %s", tc.dir, man.CanonicalDigest, got)
		}
		if got := digest(tc.contract); got != man.ProtocolDigest {
			t.Errorf("%s protocol_digest = %s, contract.json is %s", tc.dir, man.ProtocolDigest, got)
		}
		if got := digest(tc.host); got != man.TaskContractDigest {
			t.Errorf("%s task_contract_digest = %s, host-contract.json is %s", tc.dir, man.TaskContractDigest, got)
		}
	}
}

func cliHelp(t *testing.T, root string) string {
	t.Helper()
	cmd := exec.Command("go", "run", "./cmd/memora", "help")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("memora help: %v\n%s", err, out)
	}
	return string(out)
}

// Optional SQLite modules arrive through build tags, and a stage that forgets
// one builds a binary where recall silently has no index. The tags live in one
// variable so there is a single place to read them from.
func TestCIScriptKeepsOptionalSQLiteModules(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(repoRoot(t), "scripts/ci.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("build_tags=sqlite_fts5")) {
		t.Fatal("ci.sh does not declare the optional-module build tags")
	}
	for _, stage := range []string{"vet", "unit", "race", "cgo-build"} {
		if !strings.Contains(ciStage(t, stage), `-tags "$build_tags"`) {
			t.Errorf("%s compiles without the optional-module build tags", stage)
		}
	}
	// The two analysers that load packages have to see the same package set the
	// build does, or a file behind a tag hides from the sweep. The stage body is
	// one command per line with continuations, so count the flag instead of
	// matching it against a tool name.
	if flags := strings.Count(ciStage(t, "lint"), `-tags "$build_tags"`); flags < 2 {
		t.Errorf("lint passes the optional-module build tags %d times; staticcheck and errcheck both need them", flags)
	}
}

// A hand-written build command is the easiest place to lose a tag.
func TestDocumentedBuildsCarryTheSQLiteModuleTags(t *testing.T) {
	for _, file := range []string{"README.md", filepath.Join("skills", "memora", "scripts", "install.sh")} {
		body, err := os.ReadFile(filepath.Join(repoRoot(t), file))
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(body), "\n") {
			if !strings.Contains(line, "go build") {
				continue
			}
			if !strings.Contains(line, "-tags sqlite_fts5") {
				t.Errorf("%s builds without the optional-module tags: %s", file, strings.TrimSpace(line))
			}
		}
	}
}

// The fourth retrieval path is a Skill-layer script, so nothing in the Go tree
// calls it — which is exactly why its request shape needs a check: it is the one
// place a route id could travel to a model, and the retrieval design says ids
// never leave. --dry-run builds the request without sending it.
func TestJevSelectorSendsNamesAndPurposesOnly(t *testing.T) {
	root := repoRoot(t)
	script := filepath.Join(root, "skills/memora/scripts/jev_select.py")
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not installed")
	}
	request := `{"intent":"where did we put the crash recovery notes",` +
		`"options":[{"name":"tech","purpose":"decisions","route_id":"route_secret_one"},` +
		`{"name":"life","purpose":"health","route_id":"route_secret_two"}]}`
	command := exec.Command(python, script, "--dry-run")
	command.Dir = root
	command.Stdin = strings.NewReader(request)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("jev_select.py --dry-run: %v", err)
	}
	if bytes.Contains(output, []byte("route_secret")) || bytes.Contains(output, []byte("route_id")) {
		t.Fatalf("a route id reached the request: %s", output)
	}
	var payload struct {
		Request struct {
			Model     string `json:"model"`
			Questions map[string]struct {
				Type         string            `json:"type"`
				Criteria     map[string]string `json:"criteria"`
				Instructions struct {
					Question string            `json:"question"`
					Places   map[string]string `json:"places"`
				} `json:"instructions"`
			} `json:"questions"`
		} `json:"request"`
	}
	if err := json.Unmarshal(output, &payload); err != nil {
		t.Fatalf("dry run did not answer with JSON: %v (%s)", err, output)
	}
	if payload.Request.Model == "" {
		t.Fatal("the request must name a model")
	}
	question, present := payload.Request.Questions["child"]
	if !present || question.Type != "choice" {
		t.Fatalf("the layer must travel as a Choice: %+v", payload.Request.Questions)
	}
	// Both the rubric and the restated options must carry the purpose, and only
	// the two options the caller offered.
	for name, purpose := range map[string]string{"tech": "decisions", "life": "health"} {
		if question.Criteria[name] != purpose || question.Instructions.Places[name] != purpose {
			t.Fatalf("option %q lost its purpose: %+v", name, question)
		}
	}
	if len(question.Criteria) != 2 {
		t.Fatalf("only the offered options may be sent: %+v", question.Criteria)
	}
	if question.Instructions.Question == "" {
		t.Fatal("the question must say what is being decided")
	}
}
