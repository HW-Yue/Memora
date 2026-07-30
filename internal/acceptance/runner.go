package acceptance

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/releasepublish"
	"github.com/HW-Yue/Memora/internal/result"
)

const maxCommandOutput = 1 << 20

type Options struct {
	PublicationDir string
	OutputDir      string
	Version        string
	Arch           string
	Clock          func() time.Time
}

type journey struct {
	ctx         context.Context
	options     Options
	started     time.Time
	sandbox     string
	home        string
	temp        string
	installDir  string
	dataDir     string
	binary      string
	skillDir    string
	serverURL   string
	certificate string
	environment []string
	publication releasepublish.Publication
	report      Report
	diagnostics Diagnostics
	failure     error
}

func Run(ctx context.Context, options Options) (Report, error) {
	if err := validateOptions(options); err != nil {
		return Report{}, err
	}
	if options.Clock == nil {
		options.Clock = time.Now
	}
	started := options.Clock().UTC()
	sandbox, err := os.MkdirTemp("", "memora-clean-machine-*")
	if err != nil {
		return Report{}, err
	}
	defer func() { _ = os.RemoveAll(sandbox) }()
	value := &journey{
		ctx: optionsContext(ctx), options: options, started: started, sandbox: sandbox,
		home: filepath.Join(sandbox, "home"), temp: filepath.Join(sandbox, "tmp"),
		installDir: filepath.Join(sandbox, "home", ".local", "bin"),
		dataDir:    filepath.Join(sandbox, "home", "Library", "Application Support", "Memora", "instances", "default"),
	}
	value.binary = filepath.Join(value.installDir, "memora")
	value.skillDir = filepath.Join(sandbox, "skill-bundle", "skills", "memora")
	value.report = Report{
		Version: ReportVersion, Status: "failed", ProductVersion: options.Version,
		OS: "darwin", Arch: normalizeArch(options.Arch),
		StartedAt: started.Format(time.RFC3339Nano), Steps: []Step{},
	}
	value.diagnostics = Diagnostics{
		Version: DiagnosticsVersion, Status: "failed", ProductVersion: options.Version,
		OS: "darwin", Arch: normalizeArch(options.Arch), Steps: []DiagnosticStep{},
	}
	for _, directory := range []string{value.home, value.temp, value.installDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return Report{}, err
		}
	}
	defer value.stopDaemon()

	value.step("verify_publication", value.verifyPublication)
	value.step("extract_canonical_skill", value.extractSkill)
	value.step("install_verified_release", value.install)
	value.step("verify_initialized_daemon", value.verifyInstall)
	value.step("summarize_project", value.summarizeProject)
	value.step("restart_daemon", value.restartDaemon)
	value.step("query_after_restart", value.queryAfterRestart)
	value.step("final_doctor", value.finalDoctor)

	completed := options.Clock().UTC()
	if value.failure == nil && len(value.report.Steps) == len(requiredSteps) {
		value.report.Status = "passed"
		value.diagnostics.Status = "passed"
	}
	value.report.CompletedAt = completed.Format(time.RFC3339Nano)
	value.report.Commit = value.publication.Release.Commit
	value.diagnostics.Commit = value.report.Commit
	if err := os.MkdirAll(filepath.Dir(options.OutputDir), 0o755); err != nil {
		return value.report, err
	}
	if err := writeArtifacts(options.OutputDir, value.report, value.diagnostics, completed); err != nil {
		return value.report, fmt.Errorf("write clean-machine acceptance artifacts: %w", err)
	}
	if value.failure != nil {
		return value.report, value.failure
	}
	return value.report, nil
}

func validateOptions(options Options) error {
	osName, arch := runtimePlatform()
	if osName != "darwin" {
		return errors.New("clean-machine acceptance must run on macOS")
	}
	if !absoluteClean(options.PublicationDir) || !absoluteClean(options.OutputDir) {
		return errors.New("acceptance publication and output paths must be absolute and normalized")
	}
	if !stableVersion.MatchString(options.Version) {
		return errors.New("acceptance version must be stable semantic version")
	}
	if normalizeArch(options.Arch) != arch {
		return fmt.Errorf("acceptance architecture %s does not match runner %s", options.Arch, arch)
	}
	return nil
}

func optionsContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func (value *journey) step(name string, action func() ([]byte, int, error)) {
	if value.failure != nil {
		return
	}
	output, exitCode, err := action()
	sanitized := value.sanitize(output)
	status := "passed"
	if err != nil {
		status = "failed"
		value.failure = fmt.Errorf("%s: %w", name, err)
	}
	value.report.Steps = append(value.report.Steps, Step{
		Name: name, Status: status, ExitCode: exitCode, OutputSHA256: digest([]byte(sanitized)),
	})
	value.diagnostics.Steps = append(value.diagnostics.Steps, DiagnosticStep{
		Name: name, Status: status, ExitCode: exitCode, Output: sanitized,
	})
}

func (value *journey) verifyPublication() ([]byte, int, error) {
	publication, err := releasepublish.Verify(value.options.PublicationDir, value.options.Version)
	if err != nil {
		return nil, -1, err
	}
	if publication.Release.Commit == "" || !commitPattern.MatchString(publication.Release.Commit) {
		return nil, -1, errors.New("publication commit is invalid")
	}
	value.publication = publication
	value.report.Commit = publication.Release.Commit
	value.diagnostics.Commit = publication.Release.Commit
	return []byte(fmt.Sprintf(
		"verified publication %s for commit %s\n",
		value.options.Version,
		publication.Release.Commit,
	)), 0, nil
}

func (value *journey) extractSkill() ([]byte, int, error) {
	archive := filepath.Join(
		value.options.PublicationDir,
		fmt.Sprintf("memora_%s_skill_bundle.tar.gz", value.options.Version),
	)
	if err := extractSkillBundle(archive, filepath.Join(value.sandbox, "skill-bundle"), value.publication.Bundle); err != nil {
		return nil, -1, err
	}
	installer := filepath.Join(value.skillDir, "scripts", "install.sh")
	info, err := os.Lstat(installer)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o755 {
		return nil, -1, errors.New("canonical Skill installer is missing or not executable")
	}
	return []byte("extracted verified canonical Skill bundle\n"), 0, nil
}

func (value *journey) install() ([]byte, int, error) {
	server, err := startReleaseServer(value.options.PublicationDir, value.options.Version, value.options.Arch)
	if err != nil {
		return nil, -1, err
	}
	defer server.Close()
	value.serverURL = server.URL
	value.certificate = filepath.Join(value.sandbox, "release-ca.pem")
	if err := os.WriteFile(value.certificate, server.Certificate, 0o600); err != nil {
		return nil, -1, err
	}
	value.environment = []string{
		"HOME=" + value.home,
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=" + value.temp,
		"LC_ALL=C",
		"MEMORA_RELEASE_BASE=" + value.serverURL,
		"CURL_CA_BUNDLE=" + value.certificate,
		"NO_PROXY=127.0.0.1,localhost",
		"no_proxy=127.0.0.1,localhost",
	}
	return value.command(
		"/bin/sh",
		filepath.Join(value.skillDir, "scripts", "install.sh"),
		"--yes",
		"--version", value.options.Version,
		"--install-dir", value.installDir,
		"--data-dir", value.dataDir,
		"--os", "darwin",
		"--arch", value.options.Arch,
	)
}

func (value *journey) verifyInstall() ([]byte, int, error) {
	var output bytes.Buffer
	version, code, err := value.command(value.binary, "version", "--json")
	output.Write(version)
	if err != nil {
		return output.Bytes(), code, err
	}
	var metadata struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(version, &metadata); err != nil || metadata.Version != value.options.Version {
		return output.Bytes(), -1, errors.New("installed binary version does not match")
	}
	ping, code, err := value.command(value.binary, "daemon", "ping", "--data-dir", value.dataDir)
	output.Write(ping)
	if err != nil {
		return output.Bytes(), code, err
	}
	doctor, code, err := value.command(value.binary, "doctor", "--data-dir", value.dataDir)
	output.Write(doctor)
	if err != nil {
		return output.Bytes(), code, err
	}
	if err := requireHealthyDoctor(doctor, 0, 0); err != nil {
		return output.Bytes(), -1, err
	}
	return output.Bytes(), 0, nil
}

func (value *journey) summarizeProject() ([]byte, int, error) {
	var output bytes.Buffer
	ddl := "CREATE DATABASE clean PURPOSE 'Acceptance project knowledge' SCOPE 'Clean-machine fixture'; " +
		"CREATE TABLE clean.project_summaries PURPOSE 'Project summaries' " +
		"ROW SEMANTICS 'One reviewed project summary' " +
		"(title TEXT(200) NOT NULL PURPOSE 'Project title', " +
		"summary TEXT(1200) NOT NULL PURPOSE 'Reviewed project summary')"
	ddlOutput, code, err := value.command(value.binary, "exec", "--data-dir", value.dataDir, ddl)
	output.Write(ddlOutput)
	if err != nil {
		return output.Bytes(), code, err
	}
	if err := requireEnvelope(ddlOutput); err != nil {
		return output.Bytes(), -1, err
	}
	input := map[string]any{
		"parameters": map[string]any{"named": map[string]any{
			"title":   "Memora",
			"summary": "Memora is a local personal database maintained through versioned MSQL.",
		}},
		"mutation": map[string]any{
			"expected_schema_version": 1,
			"max_affected_rows":       1,
			"index_terms":             []string{"memora", "local", "personal database", "MSQL"},
			"route_leaf_ids":          []string{},
			"actor":                   "agent:clean-machine",
			"source":                  "acceptance:project-summary",
			"reason":                  "summarize the clean-machine fixture project",
		},
		"authorization": map[string]any{
			"version":              "memora.authorization/v1",
			"actor":                "agent:clean-machine",
			"authorized_databases": []string{"clean"},
		},
	}
	encoded, _ := json.Marshal(input)
	insertOutput, code, err := value.command(
		value.binary,
		"exec",
		"--data-dir", value.dataDir,
		"--input", string(encoded),
		"INSERT INTO clean.project_summaries (title, summary) VALUES (:title, :summary)",
	)
	output.Write(insertOutput)
	if err != nil {
		return output.Bytes(), code, err
	}
	if err := requireEnvelope(insertOutput); err != nil {
		return output.Bytes(), -1, err
	}
	return output.Bytes(), 0, nil
}

func (value *journey) restartDaemon() ([]byte, int, error) {
	var output bytes.Buffer
	stopped, code, err := value.command(value.binary, "daemon", "stop", "--data-dir", value.dataDir)
	output.Write(stopped)
	if err != nil {
		return output.Bytes(), code, err
	}
	started, code, err := value.command(value.binary, "daemon", "start", "--data-dir", value.dataDir)
	output.Write(started)
	if err != nil {
		return output.Bytes(), code, err
	}
	ping, code, err := value.command(value.binary, "daemon", "ping", "--data-dir", value.dataDir)
	output.Write(ping)
	return output.Bytes(), code, err
}

func (value *journey) queryAfterRestart() ([]byte, int, error) {
	input := map[string]any{
		"authorization": map[string]any{
			"version":              "memora.authorization/v1",
			"actor":                "agent:clean-machine",
			"authorized_databases": []string{"clean"},
		},
	}
	encoded, _ := json.Marshal(input)
	output, code, err := value.command(
		value.binary,
		"query",
		"--data-dir", value.dataDir,
		"--input", string(encoded),
		"SELECT title, summary, row_id, revision FROM clean.project_summaries LIMIT 1",
	)
	if err != nil {
		return output, code, err
	}
	var envelope result.Envelope
	if err := json.Unmarshal(output, &envelope); err != nil ||
		!envelope.OK ||
		len(envelope.Results) != 1 ||
		len(envelope.Results[0].Rows) != 1 ||
		envelope.Results[0].Rows[0]["title"] != "Memora" ||
		envelope.Results[0].Rows[0]["summary"] != "Memora is a local personal database maintained through versioned MSQL." {
		return output, -1, errors.New("restarted query did not return the saved project summary")
	}
	return output, 0, nil
}

func (value *journey) finalDoctor() ([]byte, int, error) {
	var output bytes.Buffer
	doctor, code, err := value.command(value.binary, "doctor", "--data-dir", value.dataDir)
	output.Write(doctor)
	if err != nil {
		return output.Bytes(), code, err
	}
	if err := requireHealthyDoctor(doctor, 1, 1); err != nil {
		return output.Bytes(), -1, err
	}
	stopped, code, err := value.command(value.binary, "daemon", "stop", "--data-dir", value.dataDir)
	output.Write(stopped)
	return output.Bytes(), code, err
}

func requireEnvelope(encoded []byte) error {
	var envelope result.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return fmt.Errorf("decode MSQL result: %w", err)
	}
	if !envelope.OK {
		return errors.New("MSQL result is not successful")
	}
	return nil
}

func requireHealthyDoctor(encoded []byte, databases, rows int) error {
	var report struct {
		Status    string `json:"status"`
		Databases int    `json:"databases"`
		Rows      int    `json:"rows"`
	}
	if err := json.Unmarshal(encoded, &report); err != nil {
		return fmt.Errorf("decode doctor report: %w", err)
	}
	if report.Status != "healthy" || report.Databases != databases || report.Rows != rows {
		return fmt.Errorf("doctor report is not healthy: %#v", report)
	}
	return nil
}

func (value *journey) command(name string, arguments ...string) ([]byte, int, error) {
	command := exec.CommandContext(value.ctx, name, arguments...)
	command.Env = value.environment
	command.Dir = value.home
	var output bytes.Buffer
	limited := &cappedWriter{destination: &output, remaining: maxCommandOutput}
	command.Stdout = limited
	command.Stderr = limited
	err := command.Run()
	if err == nil {
		return output.Bytes(), 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return output.Bytes(), exitError.ExitCode(), err
	}
	return output.Bytes(), -1, err
}

type cappedWriter struct {
	destination *bytes.Buffer
	remaining   int
}

func (writer *cappedWriter) Write(value []byte) (int, error) {
	original := len(value)
	if writer.remaining <= 0 {
		return original, nil
	}
	if len(value) > writer.remaining {
		value = value[:writer.remaining]
	}
	_, err := writer.destination.Write(value)
	writer.remaining -= len(value)
	return original, err
}

func (value *journey) sanitize(output []byte) string {
	text := string(output)
	for _, replacement := range []struct{ old, new string }{
		{value.sandbox, "<sandbox>"},
		{value.serverURL, "https://<local-release-server>"},
	} {
		if replacement.old != "" {
			text = strings.ReplaceAll(text, replacement.old, replacement.new)
		}
	}
	if len(text) > maxDiagnosticText {
		text = text[:maxDiagnosticText-32] + "\n<diagnostic output truncated>\n"
	}
	return text
}

func (value *journey) stopDaemon() {
	if value.binary == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, value.binary, "daemon", "stop", "--data-dir", value.dataDir)
	command.Env = value.environment
	command.Dir = value.home
	_ = command.Run()
}

type releaseServer struct {
	URL         string
	Certificate []byte
	server      *http.Server
	listener    net.Listener
}

func (server *releaseServer) Close() {
	_ = server.server.Close()
	_ = server.listener.Close()
}

func startReleaseServer(directory, version, arch string) (*releaseServer, error) {
	asset := fmt.Sprintf("memora_%s_darwin_%s.tar.gz", version, normalizeArch(arch))
	files := map[string]string{
		"/v" + version + "/" + asset:      filepath.Join(directory, "release", asset),
		"/v" + version + "/checksums.txt": filepath.Join(directory, "release", "checksums.txt"),
	}
	for _, name := range files {
		info, err := os.Lstat(name)
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("local release server input is incomplete")
		}
	}
	certificate, key, certificatePEM, err := localCertificate()
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		name, ok := files[request.URL.Path]
		if !ok || (request.Method != http.MethodGet && request.Method != http.MethodHead) {
			http.NotFound(writer, request)
			return
		}
		http.ServeFile(writer, request, name)
	})
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}
	tlsListener := tls.NewListener(listener, &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{certificate.Raw}, PrivateKey: key}},
	})
	server := &releaseServer{
		URL: "https://" + listener.Addr().String(), Certificate: certificatePEM,
		server: httpServer, listener: tlsListener,
	}
	go func() { _ = httpServer.Serve(tlsListener) }()
	return server, nil
}

func localCertificate() (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return nil, nil, nil, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: "Memora local release acceptance"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true, IsCA: true,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:    []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, err
	}
	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func extractSkillBundle(
	archiveName, destination string,
	manifest releasepublish.BundleManifest,
) error {
	if err := os.Mkdir(destination, 0o700); err != nil {
		return err
	}
	file, err := os.Open(archiveName)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	header, err := reader.Next()
	if err != nil ||
		header.Name != "bundle.json" ||
		header.Typeflag != tar.TypeReg ||
		header.Size <= 0 ||
		header.Size > 1<<20 {
		return errors.New("Skill bundle manifest entry is invalid")
	}
	if copied, err := io.Copy(io.Discard, io.LimitReader(reader, header.Size)); err != nil {
		return err
	} else if copied != header.Size {
		return errors.New("Skill bundle manifest entry is truncated")
	}
	for _, expected := range manifest.Files {
		header, err := reader.Next()
		mode, parseErr := strconv.ParseInt(expected.Mode, 8, 64)
		if err != nil ||
			parseErr != nil ||
			header.Name != expected.Path ||
			header.Typeflag != tar.TypeReg ||
			header.Mode != mode ||
			header.Size != expected.Size ||
			!safeBundlePath(header.Name) {
			return fmt.Errorf("Skill bundle entry %s is invalid", expected.Path)
		}
		target := filepath.Join(destination, filepath.FromSlash(header.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		content, err := io.ReadAll(io.LimitReader(reader, header.Size+1))
		if err != nil ||
			int64(len(content)) != header.Size ||
			digest(content) != expected.SHA256 {
			return fmt.Errorf("Skill bundle entry %s content is invalid", expected.Path)
		}
		output, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, os.FileMode(mode))
		if err != nil {
			return err
		}
		_, copyErr := output.Write(content)
		closeErr := output.Close()
		if err := errors.Join(copyErr, closeErr); err != nil {
			return err
		}
	}
	if _, err := reader.Next(); !errors.Is(err, io.EOF) {
		return errors.New("Skill bundle has trailing entries")
	}
	return nil
}

func safeBundlePath(name string) bool {
	return name != "" &&
		name == path.Clean(name) &&
		name != "." &&
		!strings.HasPrefix(name, "/") &&
		!strings.HasPrefix(name, "../") &&
		!strings.Contains(name, `\`) &&
		!strings.ContainsRune(name, 0)
}
