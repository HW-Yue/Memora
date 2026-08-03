package answercorpus_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestGeneratedAnswerCorpusMatchesFrozenArtifacts(t *testing.T) {
	t.Parallel()

	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	if err := bundle.Validate(); err != nil {
		t.Fatal(err)
	}
	wantManifest, err := answercorpus.EncodeManifest(bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	wantTruth, err := answercorpus.EncodeGroundTruth(bundle.GroundTruth)
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..")
	manifestPath := filepath.Join(root, "benchmarks", "answer-retrieval-v1", "manifest.json")
	truthPath := filepath.Join(root, "benchmarks", "answer-retrieval-v1", "ground-truth.json")
	gotManifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	gotTruth, err := os.ReadFile(truthPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotManifest) != string(wantManifest) {
		t.Fatal("frozen answer manifest differs from deterministic generator")
	}
	if string(gotTruth) != string(wantTruth) {
		t.Fatal("frozen answer ground truth differs from deterministic generator")
	}
	loaded, err := answercorpus.Load(manifestPath, truthPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, bundle) {
		t.Fatalf("Load() differs from Generate()\n got=%#v\nwant=%#v", loaded, bundle)
	}
	second, err := answercorpus.Generate()
	if err != nil || !reflect.DeepEqual(second, bundle) {
		t.Fatalf("second Generate() = %#v, %v", second, err)
	}
}

func TestAnswerCorpusFreezesCoverageSourcesLicenseAndFixtureInvariants(t *testing.T) {
	t.Parallel()

	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest
	if manifest.Version != answercorpus.ManifestVersion || manifest.CorpusID != "answer-retrieval-v1" ||
		manifest.Revision != 1 || manifest.Fixture.Version != answercorpus.FixtureVersion ||
		len(manifest.Cases) != 12 || len(manifest.Fixture.Databases) != 3 {
		t.Fatalf("manifest identity/shape = %#v", manifest)
	}
	if manifest.License.SPDXID != "PolyForm-Noncommercial-1.0.0" || manifest.License.Path != "LICENSE" ||
		!strings.Contains(manifest.License.RequiredNotice, "Copyright 2026 HW-Yue") ||
		!strings.Contains(manifest.License.RequiredNotice, "Commercial use requires a separate paid commercial license") {
		t.Fatalf("license = %#v", manifest.License)
	}
	if len(manifest.Sources) != 1 || manifest.Sources[0].Kind != "synthetic" ||
		manifest.Sources[0].LicenseID != manifest.License.ID ||
		manifest.Sources[0].ContentSHA256 != manifest.Fixture.SnapshotSHA256 ||
		manifest.Sources[0].Locator != "repo:internal/answercorpus/generator.go" {
		t.Fatalf("sources = %#v fixture=%#v", manifest.Sources, manifest.Fixture)
	}

	languages, categories := map[string]int{}, map[string]int{}
	for _, testCase := range manifest.Cases {
		languages[testCase.Language]++
		categories[testCase.Category]++
		if !sort.StringsAreSorted(testCase.AuthorizedDatabases) || len(testCase.AuthorizedDatabases) == 0 {
			t.Fatalf("case scope = %#v", testCase)
		}
	}
	for _, language := range []string{"en", "mixed", "zh"} {
		if languages[language] == 0 {
			t.Errorf("language %q is absent", language)
		}
	}
	for _, category := range []string{"direct", "multi_fact", "negative", "paraphrase"} {
		if categories[category] == 0 {
			t.Errorf("category %q is absent", category)
		}
	}

	databaseIDs := map[string]bool{}
	tableIDs, columnIDs, rowValues := map[string]bool{}, map[string]bool{}, map[string]string{}
	leafRows := map[string]int{}
	for _, database := range manifest.Fixture.Databases {
		if databaseIDs[database.ID] {
			t.Fatalf("duplicate Database %q", database.ID)
		}
		databaseIDs[database.ID] = true
		if !sort.SliceIsSorted(database.Tables, func(left, right int) bool { return database.Tables[left].ID < database.Tables[right].ID }) {
			t.Fatalf("Tables are not stable-ID ordered: %#v", database.Tables)
		}
		for _, table := range database.Tables {
			tableIDs[table.ID] = true
			for _, column := range table.Columns {
				columnIDs[table.ID+"\x00"+column.ID] = true
			}
			for _, row := range table.Rows {
				if row.SourceID != manifest.Sources[0].ID {
					t.Fatalf("Row source = %#v", row)
				}
				for columnID, value := range row.Values {
					if !columnIDs[table.ID+"\x00"+columnID] {
						t.Fatalf("Row %q references Column %q", row.ID, columnID)
					}
					rowValues[table.ID+"\x00"+row.ID+"\x00"+columnID] = value
				}
			}
			for _, route := range table.Routes {
				if route.Kind == "leaf" {
					if route.RowID == "" {
						t.Fatalf("Leaf has no Row: %#v", route)
					}
					leafRows[table.ID+"\x00"+route.RowID]++
				} else if route.RowID != "" {
					t.Fatalf("Branch has a Row: %#v", route)
				}
			}
			for _, row := range table.Rows {
				if leafRows[table.ID+"\x00"+row.ID] != 1 {
					t.Fatalf("Row %q Leaf count = %d", row.ID, leafRows[table.ID+"\x00"+row.ID])
				}
			}
		}
	}
	if len(tableIDs) < 3 || len(rowValues) < 12 {
		t.Fatalf("fixture coverage tables=%d facts=%d", len(tableIDs), len(rowValues))
	}

	if bundle.GroundTruth.Version != answercorpus.GroundTruthVersion ||
		bundle.GroundTruth.CorpusID != manifest.CorpusID ||
		bundle.GroundTruth.SnapshotSHA256 != manifest.Fixture.SnapshotSHA256 ||
		bundle.GroundTruth.Hash != manifest.GroundTruthSHA256 ||
		len(bundle.GroundTruth.Cases) != len(manifest.Cases) {
		t.Fatalf("ground truth identity = %#v", bundle.GroundTruth)
	}
	for _, expected := range bundle.GroundTruth.Cases {
		if expected.Kind == "answerable" && len(expected.Facts) == 0 ||
			expected.Kind == "unanswerable" && len(expected.Facts) != 0 {
			t.Fatalf("expected fact shape = %#v", expected)
		}
		for _, fact := range expected.Facts {
			key := fact.TableID + "\x00" + fact.RowID + "\x00" + fact.ColumnID
			if !databaseIDs[fact.DatabaseID] || !tableIDs[fact.TableID] || !columnIDs[fact.TableID+"\x00"+fact.ColumnID] ||
				rowValues[key] != fact.Value {
				t.Fatalf("fact does not reference fixture authority: %#v", fact)
			}
		}
	}
}

func TestBlindTasksContainNoGroundTruthOrAnswerMaterial(t *testing.T) {
	t.Parallel()

	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	typeOfTask := reflect.TypeOf(answercorpus.BlindTask{})
	for index := 0; index < typeOfTask.NumField(); index++ {
		name := strings.ToLower(typeOfTask.Field(index).Name)
		for _, forbidden := range []string{"answer", "expected", "fact", "ground", "truth", "row", "route"} {
			if strings.Contains(name, forbidden) {
				t.Fatalf("BlindTask field %q leaks %q", name, forbidden)
			}
		}
	}
	truthByCase := map[string]answercorpus.ExpectedCase{}
	for _, expected := range bundle.GroundTruth.Cases {
		truthByCase[expected.CaseID] = expected
	}
	for _, testCase := range bundle.Manifest.Cases {
		task, err := bundle.BlindTask(testCase.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.Version != answercorpus.BlindTaskVersion || task.CorpusID != bundle.Manifest.CorpusID ||
			task.CaseID != testCase.ID || task.Question != testCase.Question ||
			task.SnapshotSHA256 != bundle.Manifest.Fixture.SnapshotSHA256 ||
			!reflect.DeepEqual(task.AuthorizedDatabases, testCase.AuthorizedDatabases) {
			t.Fatalf("BlindTask = %#v case=%#v", task, testCase)
		}
		encoded, err := json.Marshal(task)
		if err != nil {
			t.Fatal(err)
		}
		lower := strings.ToLower(string(encoded))
		for _, forbidden := range []string{"reference_answer", "ground_truth", "required_facts", "row_id", "route_id"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("BlindTask JSON leaked field %q: %s", forbidden, encoded)
			}
		}
		expected := truthByCase[testCase.ID]
		if strings.Contains(string(encoded), expected.ReferenceAnswer) {
			t.Fatalf("BlindTask leaked reference answer: %s", encoded)
		}
		for _, fact := range expected.Facts {
			for _, secret := range []string{fact.RowID, fact.Value} {
				if secret != "" && strings.Contains(string(encoded), secret) {
					t.Fatalf("BlindTask leaked fact %q: %s", secret, encoded)
				}
			}
		}
	}
	if _, err := bundle.BlindTask("case-missing"); err == nil {
		t.Fatal("BlindTask accepted an unknown case")
	}
}

func TestAnswerCorpusStrictlyRejectsTamperUnknownTrailingOrderAndDanglingFacts(t *testing.T) {
	t.Parallel()

	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	manifest, _ := answercorpus.EncodeManifest(bundle.Manifest)
	truth, _ := answercorpus.EncodeGroundTruth(bundle.GroundTruth)
	tests := []struct {
		name     string
		manifest []byte
		truth    []byte
	}{
		{
			name:     "manifest unknown field",
			manifest: []byte(strings.Replace(string(manifest), `"version":`, `"unknown":true,"version":`, 1)),
			truth:    truth,
		},
		{name: "manifest trailing", manifest: append(append([]byte(nil), manifest...), []byte(`{}`)...), truth: truth},
		{
			name:     "fixture tamper",
			manifest: []byte(strings.Replace(string(manifest), "Use a Memora-owned thin Go loop", "Use an unbound framework loop", 1)),
			truth:    truth,
		},
		{
			name:     "truth tamper",
			manifest: manifest,
			truth:    []byte(strings.Replace(string(truth), "Use a Memora-owned thin Go loop", "Use an unbound framework loop", 1)),
		},
		{name: "truth trailing", manifest: manifest, truth: append(append([]byte(nil), truth...), []byte(`[]`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			manifestPath := filepath.Join(directory, "manifest.json")
			truthPath := filepath.Join(directory, "ground-truth.json")
			if err := os.WriteFile(manifestPath, test.manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(truthPath, test.truth, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := answercorpus.Load(manifestPath, truthPath); err == nil {
				t.Fatal("Load() accepted malformed corpus")
			}
		})
	}

	t.Run("case order", func(t *testing.T) {
		mutated, _ := answercorpus.Generate()
		mutated.Manifest.Cases[0], mutated.Manifest.Cases[1] = mutated.Manifest.Cases[1], mutated.Manifest.Cases[0]
		if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "order") {
			t.Fatalf("Validate() reordered cases error = %v", err)
		}
	})
	t.Run("license", func(t *testing.T) {
		mutated, _ := answercorpus.Generate()
		mutated.Manifest.License.RequiredNotice = ""
		if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "license") {
			t.Fatalf("Validate() missing license error = %v", err)
		}
	})
	t.Run("dangling fact", func(t *testing.T) {
		mutated, _ := answercorpus.Generate()
		mutated.GroundTruth.Cases[0].Facts[0].RowID = "row_missing"
		if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "fact") {
			t.Fatalf("Validate() dangling fact error = %v", err)
		}
	})
	t.Run("duplicate ID", func(t *testing.T) {
		mutated, _ := answercorpus.Generate()
		mutated.Manifest.Cases[1].ID = mutated.Manifest.Cases[0].ID
		if err := mutated.Validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
			t.Fatalf("Validate() duplicate case error = %v", err)
		}
	})
}
