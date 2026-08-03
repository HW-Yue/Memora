package answercorpus_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/answercorpus"
)

func TestLoadManifestBuildsBlindTasksWithoutGroundTruth(t *testing.T) {
	t.Parallel()
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := answercorpus.EncodeManifest(bundle.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := answercorpus.LoadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(manifest, bundle.Manifest) {
		t.Fatalf("LoadManifest() differs from generated public manifest")
	}
	tasks, err := manifest.BlindTasks()
	if err != nil || len(tasks) != len(manifest.Cases) {
		t.Fatalf("BlindTasks() = %#v, %v", tasks, err)
	}
	for index, task := range tasks {
		if task.CaseID != manifest.Cases[index].ID || task.Question != manifest.Cases[index].Question ||
			task.AuthorizedDatabases == nil {
			t.Fatalf("BlindTask(%d) = %#v", index, task)
		}
	}
}
