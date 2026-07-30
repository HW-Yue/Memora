package acceptance

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestWriteArtifactsBindsCompleteReportToDiagnostics(t *testing.T) {
	t.Parallel()

	completed := time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC)
	report := Report{
		Version: ReportVersion, Status: "passed", ProductVersion: "1.2.3",
		Commit: strings.Repeat("a", 40), OS: "darwin", Arch: "arm64",
		StartedAt:   completed.Add(-time.Minute).Format(time.RFC3339Nano),
		CompletedAt: completed.Format(time.RFC3339Nano),
		Steps:       []Step{},
	}
	diagnostics := Diagnostics{
		Version: DiagnosticsVersion, Status: "passed", ProductVersion: report.ProductVersion,
		Commit: report.Commit, OS: report.OS, Arch: report.Arch, Steps: []DiagnosticStep{},
	}
	for _, name := range requiredSteps {
		output := name + " passed\n"
		report.Steps = append(report.Steps, Step{
			Name: name, Status: "passed", ExitCode: 0, OutputSHA256: digest([]byte(output)),
		})
		diagnostics.Steps = append(diagnostics.Steps, DiagnosticStep{
			Name: name, Status: "passed", ExitCode: 0, Output: output,
		})
	}
	output := filepath.Join(t.TempDir(), "acceptance")
	if err := writeArtifacts(output, report, diagnostics, completed); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(output)
	if err != nil || len(entries) != 2 {
		t.Fatalf("artifact entries = %v, %v", entries, err)
	}
	written, err := readStrictJSON[Report](filepath.Join(output, ReportFilename), 128<<10)
	if err != nil {
		t.Fatal(err)
	}
	hash, _, err := hashFile(filepath.Join(output, DiagnosticsArchive))
	if err != nil ||
		written.DiagnosticsFile != DiagnosticsArchive ||
		written.DiagnosticsSHA256 != hash {
		t.Fatalf("written report = %#v, hash=%q, err=%v", written, hash, err)
	}
	decoded, err := readDiagnosticsArchive(filepath.Join(output, DiagnosticsArchive))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, diagnostics) {
		t.Fatalf("diagnostics = %#v, want %#v", decoded, diagnostics)
	}
	if err := validateSteps(written.Steps, true); err != nil {
		t.Fatal(err)
	}
}

func TestPassingReportRejectsIncompleteOrFailedJourney(t *testing.T) {
	t.Parallel()

	steps := make([]Step, 0, len(requiredSteps))
	for _, name := range requiredSteps {
		steps = append(steps, Step{
			Name: name, Status: "passed", ExitCode: 0, OutputSHA256: digest(nil),
		})
	}
	if err := validateSteps(steps, true); err != nil {
		t.Fatal(err)
	}
	if err := validateSteps(steps[:len(steps)-1], true); err == nil {
		t.Fatal("incomplete journey passed validation")
	}
	steps[3].Status = "failed"
	steps[3].ExitCode = 1
	if err := validateSteps(steps, true); err == nil {
		t.Fatal("failed journey passed validation")
	}
}

func TestCappedWriterBoundsUntrustedCommandOutput(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	writer := &cappedWriter{destination: &buffer, remaining: 5}
	if count, err := writer.Write([]byte("123456789")); err != nil || count != 9 {
		t.Fatalf("Write() = %d, %v", count, err)
	}
	if got := buffer.String(); got != "12345" {
		t.Fatalf("bounded output = %q", got)
	}
}

func TestRunWritesDiagnosticsWhenPublicationVerificationFails(t *testing.T) {
	t.Parallel()

	osName, arch := runtimePlatform()
	if osName != "darwin" {
		t.Skip("clean-machine acceptance is macOS-only")
	}
	root := t.TempDir()
	publication := filepath.Join(root, "publication")
	if err := os.Mkdir(publication, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(root, "acceptance")
	_, err := Run(context.Background(), Options{
		PublicationDir: publication,
		OutputDir:      output,
		Version:        "1.2.3",
		Arch:           arch,
		Clock:          func() time.Time { return time.Date(2026, 7, 30, 18, 0, 0, 0, time.UTC) },
	})
	if err == nil || !strings.Contains(err.Error(), "verify_publication") {
		t.Fatalf("Run() error = %v", err)
	}
	for _, name := range []string{ReportFilename, DiagnosticsArchive} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("failure artifact %s: %v", name, err)
		}
	}
	report, err := readStrictJSON[Report](filepath.Join(output, ReportFilename), 128<<10)
	if err != nil || report.Status != "failed" ||
		len(report.Steps) != 1 || report.Steps[0].Status != "failed" {
		t.Fatalf("failure report = %#v, %v", report, err)
	}
}
