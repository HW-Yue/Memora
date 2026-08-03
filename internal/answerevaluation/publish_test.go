package answerevaluation_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/answercorpus"
	"github.com/HW-Yue/Memora/internal/answerevaluation"
)

func TestLoadReportsStrictAndPublishPublicReportAtomically(t *testing.T) {
	bundle, err := answercorpus.Generate()
	if err != nil {
		t.Fatal(err)
	}
	public, private := evaluationArtifacts(t, bundle)
	root := t.TempDir()
	publicPath, privatePath := filepath.Join(root, "scorecard.json"), filepath.Join(root, "diagnostics.json")
	writeJSONFixture(t, publicPath, public)
	writeJSONFixture(t, privatePath, private)
	loadedPublic, loadedPrivate, err := answerevaluation.LoadReports(publicPath, privatePath)
	if err != nil || loadedPublic.Hash != public.Hash || loadedPrivate.Hash != private.Hash {
		t.Fatalf("LoadReports() = %#v, %#v, %v", loadedPublic, loadedPrivate, err)
	}

	encoded, err := os.ReadFile(publicPath)
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	if err := os.WriteFile(publicPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := answerevaluation.LoadReports(publicPath, privatePath); err == nil {
		t.Fatal("LoadReports accepted an unknown scorecard field")
	}
	writeJSONFixture(t, publicPath, public)

	runner, err := answerevaluation.NewRunner(&scriptedEvaluator{})
	if err != nil {
		t.Fatal(err)
	}
	report, err := runner.Run(context.Background(), bundle, public, private)
	if err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(root, "evaluation.json")
	if err := answerevaluation.PublishReport(outputPath, report); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(outputPath)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("published report mode = %v; err = %v", info, err)
	}
	var decoded answerevaluation.Report
	if err := json.Unmarshal(published, &decoded); err != nil || decoded.Validate() != nil || decoded.Hash != report.Hash {
		t.Fatalf("published report = %#v; err = %v", decoded, err)
	}
	for _, forbidden := range []string{
		bundle.GroundTruth.Cases[0].ReferenceAnswer, "Use a Memora-owned thin Go loop", "SELECT row_id", "row_actual",
	} {
		if strings.Contains(string(published), forbidden) {
			t.Fatalf("public report leaked %q", forbidden)
		}
	}
	if err := answerevaluation.PublishReport(outputPath, report); !errors.Is(err, answerevaluation.ErrReportOutputExists) {
		t.Fatalf("second PublishReport() = %v", err)
	}
	again, err := os.ReadFile(outputPath)
	if err != nil || string(again) != string(published) {
		t.Fatal("existing report changed after rejected overwrite")
	}
	if err := answerevaluation.PublishReport("relative.json", report); !errors.Is(err, answerevaluation.ErrInvalidReportOutput) {
		t.Fatalf("relative PublishReport() = %v", err)
	}
}

func writeJSONFixture(t *testing.T, path string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
}
