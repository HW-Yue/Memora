package routebenchmark_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/hostcontract"
	"github.com/HW-Yue/Memora/internal/routebenchmark"
)

func TestFrozenCorpusMatchesGeneratorAndDigest(t *testing.T) {
	t.Parallel()

	generated, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := routebenchmark.Load(filepath.Join(repositoryRoot(t), "benchmarks", "route-retrieval-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frozen, generated) {
		t.Fatal("frozen Route corpus drifted from deterministic generator")
	}
	wantBytes, err := routebenchmark.Encode(generated)
	if err != nil {
		t.Fatal(err)
	}
	gotBytes, err := os.ReadFile(filepath.Join(repositoryRoot(t), "benchmarks", "route-retrieval-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotBytes, wantBytes) {
		t.Fatal("frozen Route corpus bytes differ from generator output")
	}
	if generated.Version != routebenchmark.CorpusVersion ||
		generated.GeneratorVersion != routebenchmark.GeneratorVersion || len(generated.Hash) != 64 {
		t.Fatalf("corpus identity = %#v", generated)
	}
}

func TestCorpusCoversFullFanoutDepthDifficultyLanguageMatrix(t *testing.T) {
	t.Parallel()

	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(corpus.Scenarios) != 30 {
		t.Fatalf("scenario count = %d, want 30", len(corpus.Scenarios))
	}
	wantFanouts := []int{4, 8, 12, 16, 24, 32}
	wantDepths := []int{1, 2, 3, 4, 6}
	combinations := map[string]bool{}
	difficulties, languages, topics := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, scenario := range corpus.Scenarios {
		combinations[matrixKey(scenario.Fanout, scenario.Depth)] = true
		difficulties[scenario.Difficulty] = true
		languages[scenario.Language] = true
		topics[scenario.Topic] = true
	}
	for _, fanout := range wantFanouts {
		for _, depth := range wantDepths {
			if !combinations[matrixKey(fanout, depth)] {
				t.Errorf("corpus omits fanout=%d depth=%d", fanout, depth)
			}
		}
	}
	if len(difficulties) != 5 || len(languages) != 3 || len(topics) != 6 {
		t.Fatalf("coverage dimensions = difficulties %v, languages %v, topics %v",
			difficulties, languages, topics)
	}
}

func TestScenarioTreesBindContinuousGroundTruthAndNegativeStopping(t *testing.T) {
	t.Parallel()

	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	negativeCount, shuffledCount := 0, 0
	for _, scenario := range corpus.Scenarios {
		if err := scenario.Validate(); err != nil {
			t.Errorf("scenario %q: %v", scenario.ID, err)
			continue
		}
		if strings.Contains(scenario.Question, "db_") || strings.Contains(scenario.Question, "tbl_") ||
			strings.Contains(scenario.Question, "route_") || strings.Contains(scenario.Question, "row_") {
			t.Errorf("scenario %q leaks a stable identity in the question", scenario.ID)
		}
		if scenario.Limits.MaxFallbacks != 4 || scenario.Limits.MaxToolCalls != 64 ||
			scenario.Limits.MaxContextCharacters != 12000 || scenario.Limits.MaxElapsedMillis != 600000 {
			t.Errorf("scenario %q limits = %#v", scenario.ID, scenario.Limits)
		}
		sortedRoots := append([]string(nil), scenario.Table.RootRouteIDs...)
		sort.Strings(sortedRoots)
		if !reflect.DeepEqual(sortedRoots, scenario.Table.RootRouteIDs) {
			shuffledCount++
		}
		if !scenario.Expected.Match {
			negativeCount++
			if len(scenario.Expected.Path) != 0 || scenario.Expected.RowID != "" ||
				scenario.Limits.StopCondition != "no_matching_route" {
				t.Errorf("negative scenario %q has answer leakage: %#v", scenario.ID, scenario.Expected)
			}
			continue
		}
		if len(scenario.Expected.Path) != scenario.Depth || scenario.Expected.RowID == "" ||
			scenario.Limits.StopCondition != "rowid_selected" {
			t.Errorf("positive scenario %q ground truth = %#v", scenario.ID, scenario.Expected)
			continue
		}
		nodes := make(map[string]routebenchmark.Node, len(scenario.Nodes))
		for _, node := range scenario.Nodes {
			nodes[node.RouteID] = node
		}
		parent := ""
		for index, routeID := range scenario.Expected.Path {
			node := nodes[routeID]
			if node.ParentRouteID != parent {
				t.Errorf("scenario %q path breaks at %d", scenario.ID, index)
			}
			if index < len(scenario.Expected.Path)-1 && node.Kind != "branch" {
				t.Errorf("scenario %q intermediate target is not a branch", scenario.ID)
			}
			if index < len(scenario.Expected.Path)-1 {
				siblings := scenario.Table.RootRouteIDs
				if parent != "" {
					siblings = nodes[parent].ChildRouteIDs
				}
				if len(siblings) != scenario.Fanout {
					t.Errorf("scenario %q target frame %d has %d candidates", scenario.ID, index, len(siblings))
				}
				for _, siblingID := range siblings {
					if nodes[siblingID].Kind != "branch" {
						t.Errorf("scenario %q exposes target kind at frame %d", scenario.ID, index)
					}
				}
			}
			parent = routeID
		}
		leaf := nodes[scenario.Expected.Path[len(scenario.Expected.Path)-1]]
		if leaf.Kind != "leaf" || !contains(leaf.RowIDs, scenario.Expected.RowID) {
			t.Errorf("scenario %q expected Row is outside the target leaf", scenario.ID)
		}
	}
	if negativeCount != 6 || shuffledCount < 20 {
		t.Fatalf("negative/shuffle coverage = %d/%d", negativeCount, shuffledCount)
	}
}

func TestCorpusLoaderRejectsUnknownFieldsAndSnapshotTampering(t *testing.T) {
	t.Parallel()

	corpus, err := routebenchmark.Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	unknown := strings.Replace(string(encoded), "{", `{"unknown":true,`, 1)
	unknownPath := filepath.Join(t.TempDir(), "unknown.json")
	if err := os.WriteFile(unknownPath, []byte(unknown), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := routebenchmark.Load(unknownPath); err == nil {
		t.Fatal("unknown corpus field was accepted")
	}
	corpus.Scenarios[0].Nodes[0].Purpose += " tampered"
	tampered, err := json.Marshal(corpus)
	if err != nil {
		t.Fatal(err)
	}
	tamperedPath := filepath.Join(t.TempDir(), "tampered.json")
	if err := os.WriteFile(tamperedPath, tampered, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := routebenchmark.Load(tamperedPath); err == nil || !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("tampered snapshot error = %v", err)
	}
}

func TestEveryScenarioMapsToTheSameRealHostTaskContract(t *testing.T) {
	t.Parallel()

	root := repositoryRoot(t)
	contract, _, err := hostcontract.Load(filepath.Join(root, "skills", "memora", "host-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	corpus, err := routebenchmark.Load(filepath.Join(root, "benchmarks", "route-retrieval-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	seenDigests := map[string]bool{}
	for _, scenario := range corpus.Scenarios {
		task, err := corpus.HostTask(scenario.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Prompt != scenario.Question || task.CorpusDigest != corpus.Hash ||
			len(task.AuthorizedDatabases) != 1 || task.AuthorizedDatabases[0] != scenario.Database.DatabaseID {
			t.Fatalf("scenario %q Host Task binding = %#v", scenario.ID, task)
		}
		digest, err := contract.TaskDigest(task)
		if err != nil {
			t.Fatalf("scenario %q Host Task: %v", scenario.ID, err)
		}
		if seenDigests[digest] {
			t.Fatalf("scenario %q duplicated a Host Task digest", scenario.ID)
		}
		seenDigests[digest] = true
	}
	if len(seenDigests) != 30 {
		t.Fatalf("Host Task digest count = %d", len(seenDigests))
	}
}

func matrixKey(fanout, depth int) string {
	return fmt.Sprintf("%d/%d", fanout, depth)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
