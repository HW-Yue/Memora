package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/releasepublish"
)

const (
	ReportVersion      = "memora.clean-machine-acceptance/v1"
	DiagnosticsVersion = "memora.install-diagnostics/v1"
	ReportFilename     = "report.json"
	DiagnosticsArchive = "install-diagnostics.tar.gz"
	diagnosticsEntry   = "diagnostics.json"
	maxDiagnosticText  = 8 << 10
)

var (
	stableVersion = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	commitPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
	requiredSteps = []string{
		"verify_publication",
		"extract_canonical_skill",
		"install_verified_release",
		"verify_initialized_daemon",
		"summarize_project",
		"restart_daemon",
		"query_after_restart",
		"final_doctor",
	}
)

type Step struct {
	Name         string `json:"name"`
	Status       string `json:"status"`
	ExitCode     int    `json:"exit_code"`
	OutputSHA256 string `json:"output_sha256"`
}

type Report struct {
	Version           string `json:"version"`
	Status            string `json:"status"`
	ProductVersion    string `json:"product_version"`
	Commit            string `json:"commit"`
	OS                string `json:"os"`
	Arch              string `json:"arch"`
	StartedAt         string `json:"started_at"`
	CompletedAt       string `json:"completed_at"`
	Steps             []Step `json:"steps"`
	DiagnosticsFile   string `json:"diagnostics_file"`
	DiagnosticsSHA256 string `json:"diagnostics_sha256"`
}

type DiagnosticStep struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

type Diagnostics struct {
	Version        string           `json:"version"`
	Status         string           `json:"status"`
	ProductVersion string           `json:"product_version"`
	Commit         string           `json:"commit"`
	OS             string           `json:"os"`
	Arch           string           `json:"arch"`
	Steps          []DiagnosticStep `json:"steps"`
}

func Verify(directory, publicationDirectory, version, arch string) (Report, error) {
	if !absoluteClean(directory) || !absoluteClean(publicationDirectory) {
		return Report{}, errors.New("acceptance and publication directories must be absolute and normalized")
	}
	publication, err := releasepublish.Verify(publicationDirectory, version)
	if err != nil {
		return Report{}, fmt.Errorf("verify acceptance publication: %w", err)
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 2 {
		return Report{}, errors.New("acceptance output has an unexpected file set")
	}
	expected := map[string]bool{ReportFilename: false, DiagnosticsArchive: false}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok || !entry.Type().IsRegular() {
			return Report{}, errors.New("acceptance output has an unexpected file set")
		}
		expected[entry.Name()] = true
	}
	report, err := readStrictJSON[Report](filepath.Join(directory, ReportFilename), 128<<10)
	if err != nil {
		return Report{}, fmt.Errorf("read acceptance report: %w", err)
	}
	if report.Version != ReportVersion ||
		report.Status != "passed" ||
		report.ProductVersion != version ||
		report.Commit != publication.Release.Commit ||
		report.OS != "darwin" ||
		report.Arch != normalizeArch(arch) ||
		report.DiagnosticsFile != DiagnosticsArchive ||
		!validHash(report.DiagnosticsSHA256) {
		return Report{}, errors.New("acceptance report metadata is invalid or not passing")
	}
	started, err := time.Parse(time.RFC3339Nano, report.StartedAt)
	if err != nil {
		return Report{}, errors.New("acceptance report start time is invalid")
	}
	completed, err := time.Parse(time.RFC3339Nano, report.CompletedAt)
	if err != nil || completed.Before(started) {
		return Report{}, errors.New("acceptance report completion time is invalid")
	}
	if err := validateSteps(report.Steps, true); err != nil {
		return Report{}, err
	}
	archivePath := filepath.Join(directory, DiagnosticsArchive)
	hash, _, err := hashFile(archivePath)
	if err != nil || hash != report.DiagnosticsSHA256 {
		return Report{}, errors.New("acceptance diagnostics checksum does not match")
	}
	diagnostics, err := readDiagnosticsArchive(archivePath)
	if err != nil {
		return Report{}, err
	}
	if diagnostics.Version != DiagnosticsVersion ||
		diagnostics.Status != report.Status ||
		diagnostics.ProductVersion != report.ProductVersion ||
		diagnostics.Commit != report.Commit ||
		diagnostics.OS != report.OS ||
		diagnostics.Arch != report.Arch ||
		len(diagnostics.Steps) != len(report.Steps) {
		return Report{}, errors.New("acceptance diagnostics metadata does not match report")
	}
	for index, step := range diagnostics.Steps {
		reportStep := report.Steps[index]
		if step.Name != reportStep.Name ||
			step.Status != reportStep.Status ||
			step.ExitCode != reportStep.ExitCode ||
			digest([]byte(step.Output)) != reportStep.OutputSHA256 ||
			len(step.Output) > maxDiagnosticText {
			return Report{}, errors.New("acceptance diagnostics steps do not match report")
		}
	}
	return report, nil
}

func writeArtifacts(
	directory string,
	report Report,
	diagnostics Diagnostics,
	completed time.Time,
) error {
	if !absoluteClean(directory) {
		return errors.New("acceptance output directory must be absolute and normalized")
	}
	if _, err := os.Lstat(directory); err == nil {
		return errors.New("acceptance output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(directory), ".memora-acceptance-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staging) }()
	diagnosticsPath := filepath.Join(staging, DiagnosticsArchive)
	if err := writeDiagnosticsArchive(diagnosticsPath, diagnostics, completed); err != nil {
		return err
	}
	hash, _, err := hashFile(diagnosticsPath)
	if err != nil {
		return err
	}
	report.DiagnosticsFile = DiagnosticsArchive
	report.DiagnosticsSHA256 = hash
	if err := writeJSON(filepath.Join(staging, ReportFilename), report, 0o644); err != nil {
		return err
	}
	if err := os.Rename(staging, directory); err != nil {
		return err
	}
	return nil
}

func validateSteps(steps []Step, requirePassed bool) error {
	if len(steps) != len(requiredSteps) {
		return errors.New("acceptance report does not contain the complete journey")
	}
	for index, name := range requiredSteps {
		step := steps[index]
		if step.Name != name ||
			(requirePassed && (step.Status != "passed" || step.ExitCode != 0)) ||
			!validHash(step.OutputSHA256) {
			return errors.New("acceptance report journey step is invalid")
		}
	}
	return nil
}

func writeDiagnosticsArchive(name string, diagnostics Diagnostics, timestamp time.Time) error {
	encoded, err := json.MarshalIndent(diagnostics, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = timestamp.UTC()
	gzipWriter.Header.OS = 3
	tarWriter := tar.NewWriter(gzipWriter)
	header := &tar.Header{
		Name: diagnosticsEntry, Mode: 0o600, Size: int64(len(encoded)),
		ModTime: timestamp.UTC(), Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
	}
	writeErr := tarWriter.WriteHeader(header)
	if writeErr == nil {
		_, writeErr = tarWriter.Write(encoded)
	}
	return errors.Join(writeErr, tarWriter.Close(), gzipWriter.Close(), file.Close())
}

func readDiagnosticsArchive(name string) (Diagnostics, error) {
	file, err := os.Open(name)
	if err != nil {
		return Diagnostics{}, err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return Diagnostics{}, errors.New("acceptance diagnostics gzip stream is invalid")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	header, err := tarReader.Next()
	if err != nil ||
		header.Name != diagnosticsEntry ||
		header.Typeflag != tar.TypeReg ||
		header.Mode != 0o600 ||
		header.Size <= 0 ||
		header.Size > 128<<10 {
		return Diagnostics{}, errors.New("acceptance diagnostics archive layout is invalid")
	}
	encoded, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
	if err != nil || int64(len(encoded)) != header.Size {
		return Diagnostics{}, errors.New("acceptance diagnostics archive is truncated")
	}
	if _, err := tarReader.Next(); !errors.Is(err, io.EOF) {
		return Diagnostics{}, errors.New("acceptance diagnostics archive has trailing entries")
	}
	return decodeStrict[Diagnostics](encoded)
}

func readStrictJSON[T any](name string, maximum int64) (T, error) {
	var zero T
	file, err := os.Open(name)
	if err != nil {
		return zero, err
	}
	defer file.Close()
	encoded, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(encoded)) > maximum {
		return zero, errors.New("JSON file exceeds its size budget")
	}
	return decodeStrict[T](encoded)
}

func decodeStrict[T any](encoded []byte) (T, error) {
	var value T
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return value, errors.New("JSON has trailing content")
	}
	return value, nil
}

func writeJSON(name string, value any, mode os.FileMode) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(name, append(encoded, '\n'), mode)
}

func hashFile(name string) (string, int64, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func absoluteClean(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func normalizeArch(value string) string {
	switch strings.ToLower(value) {
	case "arm64", "aarch64":
		return "arm64"
	case "amd64", "x86_64":
		return "amd64"
	default:
		return value
	}
}

func runtimePlatform() (string, string) {
	return runtime.GOOS, normalizeArch(runtime.GOARCH)
}
