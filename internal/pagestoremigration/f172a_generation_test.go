package pagestoremigration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestGenerationBuildIncludesDurableFulltextTree(t *testing.T) {
	directory := t.TempDir()
	reader, plan, _ := faultPlan(t)
	applier, err := NewApplier(reader, directory)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := applier.Apply(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, relative := range []string{"fulltext.pages", "fulltext.wal"} {
		info, err := os.Stat(filepath.Join(receipt.Directory, relative))
		if err != nil || (relative == "fulltext.wal" && !info.IsDir()) {
			t.Fatalf("generation entry %q = %+v, %v", relative, info, err)
		}
	}
	manifest, err := readManifest(receipt.Directory)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Version != "memora.page-index-generation/v2" ||
		manifest.PlanVersion != "memora.page-index-migration-plan/v2" || len(manifest.Trees) != 4 {
		t.Fatalf("generation v2 manifest = %#v", manifest)
	}
}
