package release

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"debug/macho"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

const ManifestVersion = "memora.release/v1"
const SignatureVersion = "memora.release-signature/v1"

const (
	licenseName    = "LICENSE"
	commercialName = "COMMERCIAL-LICENSE.md"
	readmeName     = "README.md"
	binaryName     = "memora"
)

var (
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{7,64}$`)
	hashPattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Options struct {
	RepositoryRoot string
	OutputDir      string
	Version        string
	Commit         string
	SourceEpoch    int64
	GoCommand      string
	Signer         *Signer
}

type Signer struct {
	KeyID      string
	PrivateKey ed25519.PrivateKey
}

type Signature struct {
	Version         string `json:"version"`
	Algorithm       string `json:"algorithm"`
	KeyID           string `json:"key_id"`
	PublicKey       string `json:"public_key"`
	ManifestSHA256  string `json:"manifest_sha256"`
	ChecksumsSHA256 string `json:"checksums_sha256"`
	Signature       string `json:"signature"`
}

type Artifact struct {
	Name   string `json:"name"`
	OS     string `json:"os"`
	Arch   string `json:"arch"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Manifest struct {
	Version         string     `json:"version"`
	Product         string     `json:"product"`
	ProductVersion  string     `json:"product_version"`
	Commit          string     `json:"commit"`
	BuiltAt         string     `json:"built_at"`
	SourceDateEpoch int64      `json:"source_date_epoch"`
	GoVersion       string     `json:"go_version"`
	Artifacts       []Artifact `json:"artifacts"`
}

func Build(ctx context.Context, options Options) (Manifest, error) {
	normalized, builtAt, err := validateOptions(options)
	if err != nil {
		return Manifest{}, err
	}
	license, err := releaseFile(normalized.RepositoryRoot, licenseName, 256<<10)
	if err != nil {
		return Manifest{}, err
	}
	commercial, err := releaseFile(normalized.RepositoryRoot, commercialName, 256<<10)
	if err != nil {
		return Manifest{}, err
	}
	readme, err := releaseFile(normalized.RepositoryRoot, readmeName, 1<<20)
	if err != nil {
		return Manifest{}, err
	}
	parent := filepath.Dir(normalized.OutputDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create release parent: %w", err)
	}
	if _, err := os.Lstat(normalized.OutputDir); err == nil {
		return Manifest{}, errors.New("release output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Manifest{}, fmt.Errorf("inspect release output: %w", err)
	}
	staging, err := os.MkdirTemp(parent, ".memora-release-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create release staging directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(staging) }()

	manifest := Manifest{
		Version: ManifestVersion, Product: "memora",
		ProductVersion: normalized.Version, Commit: normalized.Commit,
		BuiltAt: builtAt.Format(time.RFC3339), SourceDateEpoch: normalized.SourceEpoch,
		GoVersion: runtime.Version(), Artifacts: []Artifact{},
	}
	for _, arch := range []string{"arm64", "amd64"} {
		binaryPath := filepath.Join(staging, "memora-"+arch)
		if err := buildBinary(ctx, normalized, builtAt, arch, binaryPath); err != nil {
			return Manifest{}, err
		}
		if err := validateMachO(binaryPath, arch); err != nil {
			return Manifest{}, err
		}
		archiveName := fmt.Sprintf("memora_%s_darwin_%s.tar.gz", normalized.Version, arch)
		archivePath := filepath.Join(staging, archiveName)
		if err := writeArchive(archivePath, binaryPath, license, commercial, readme, builtAt); err != nil {
			return Manifest{}, err
		}
		hash, size, err := hashFile(archivePath)
		if err != nil {
			return Manifest{}, err
		}
		manifest.Artifacts = append(manifest.Artifacts, Artifact{
			Name: archiveName, OS: "darwin", Arch: arch, SHA256: hash, Size: size,
		})
		if err := os.Remove(binaryPath); err != nil {
			return Manifest{}, fmt.Errorf("remove staged binary: %w", err)
		}
	}
	sort.Slice(manifest.Artifacts, func(left, right int) bool {
		return manifest.Artifacts[left].Name < manifest.Artifacts[right].Name
	})
	manifestPath := filepath.Join(staging, "release.json")
	encodedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("encode release manifest: %w", err)
	}
	encodedManifest = append(encodedManifest, '\n')
	if err := os.WriteFile(manifestPath, encodedManifest, 0o644); err != nil {
		return Manifest{}, fmt.Errorf("write release manifest: %w", err)
	}
	manifestHash, _, err := hashFile(manifestPath)
	if err != nil {
		return Manifest{}, err
	}
	checksums := make([]string, 0, len(manifest.Artifacts)+1)
	for _, artifact := range manifest.Artifacts {
		checksums = append(checksums, artifact.SHA256+"  "+artifact.Name)
	}
	checksums = append(checksums, manifestHash+"  release.json")
	sort.Strings(checksums)
	if err := os.WriteFile(
		filepath.Join(staging, "checksums.txt"),
		[]byte(strings.Join(checksums, "\n")+"\n"),
		0o644,
	); err != nil {
		return Manifest{}, fmt.Errorf("write release checksums: %w", err)
	}
	if normalized.Signer != nil {
		if err := signRelease(staging, *normalized.Signer); err != nil {
			return Manifest{}, err
		}
	}
	if _, err := Verify(staging); err != nil {
		return Manifest{}, fmt.Errorf("verify staged release: %w", err)
	}
	if err := os.Rename(staging, normalized.OutputDir); err != nil {
		return Manifest{}, fmt.Errorf("publish release directory: %w", err)
	}
	return manifest, nil
}

func Verify(directory string) (Manifest, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(directory, "release.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("read release manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, errors.New("release manifest is invalid")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Manifest{}, errors.New("release manifest has trailing content")
	}
	if err := validateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	hasSignature, err := hasRegularSignature(directory)
	if err != nil {
		return Manifest{}, err
	}
	if err := verifyReleaseFileSet(directory, manifest, hasSignature); err != nil {
		return Manifest{}, err
	}
	checksums, err := readChecksums(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return Manifest{}, err
	}
	if len(checksums) != len(manifest.Artifacts)+1 {
		return Manifest{}, errors.New("release checksum manifest has an unexpected file set")
	}
	manifestHash, _, err := hashFile(filepath.Join(directory, "release.json"))
	if err != nil || checksums["release.json"] != manifestHash {
		return Manifest{}, errors.New("release manifest checksum does not match")
	}
	for _, artifact := range manifest.Artifacts {
		path := filepath.Join(directory, artifact.Name)
		hash, size, err := hashFile(path)
		if err != nil {
			return Manifest{}, err
		}
		if hash != artifact.SHA256 || hash != checksums[artifact.Name] || size != artifact.Size {
			return Manifest{}, fmt.Errorf("release artifact %s checksum or size does not match", artifact.Name)
		}
		if err := verifyArchive(path, artifact.Arch, manifest); err != nil {
			return Manifest{}, err
		}
	}
	if hasSignature {
		if _, err := verifySignature(directory, nil, false); err != nil {
			return Manifest{}, err
		}
	}
	return manifest, nil
}

func VerifySigned(directory string, trusted map[string]ed25519.PublicKey) (Manifest, error) {
	manifest, err := Verify(directory)
	if err != nil {
		return Manifest{}, err
	}
	if _, err := verifySignature(directory, trusted, true); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateOptions(options Options) (Options, time.Time, error) {
	options.Version = strings.TrimPrefix(strings.TrimSpace(options.Version), "v")
	options.Commit = strings.ToLower(strings.TrimSpace(options.Commit))
	if !versionPattern.MatchString(options.Version) {
		return Options{}, time.Time{}, errors.New("release version must be a stable semantic version")
	}
	if !commitPattern.MatchString(options.Commit) {
		return Options{}, time.Time{}, errors.New("release commit must be a hexadecimal Git object ID")
	}
	if options.SourceEpoch <= 0 {
		return Options{}, time.Time{}, errors.New("release source date epoch must be positive")
	}
	if options.GoCommand == "" {
		options.GoCommand = "go"
	}
	if options.Signer != nil {
		if err := validateSigner(*options.Signer); err != nil {
			return Options{}, time.Time{}, err
		}
	}
	for label, path := range map[string]string{
		"repository root":  options.RepositoryRoot,
		"output directory": options.OutputDir,
	} {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return Options{}, time.Time{}, fmt.Errorf("release %s must be absolute and normalized", label)
		}
	}
	builtAt := time.Unix(options.SourceEpoch, 0).UTC()
	return options, builtAt, nil
}

func releaseFile(root, name string, maximum int64) ([]byte, error) {
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("release requires repository %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() == 0 || info.Size() > maximum {
		return nil, fmt.Errorf("release repository %s must be a bounded regular file", name)
	}
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release %s: %w", name, err)
	}
	return value, nil
}

func buildBinary(ctx context.Context, options Options, builtAt time.Time, arch, output string) error {
	ldflags := strings.Join([]string{
		"-s", "-w", "-buildid=",
		"-X", "main.version=" + options.Version,
		"-X", "main.commit=" + options.Commit,
		"-X", "main.builtAt=" + builtAt.Format(time.RFC3339),
	}, " ")
	command := exec.CommandContext(
		ctx, options.GoCommand, "build", "-trimpath", "-buildvcs=false",
		"-ldflags", ldflags, "-o", output, "./cmd/memora",
	)
	command.Dir = options.RepositoryRoot
	command.Env = append(releaseEnvironment(),
		"CGO_ENABLED=0", "GOOS=darwin", "GOARCH="+arch,
	)
	outputBytes, err := command.CombinedOutput()
	if err != nil {
		return fmt.Errorf("build darwin/%s binary: %w: %s", arch, err, strings.TrimSpace(string(outputBytes)))
	}
	if err := os.Chmod(output, 0o755); err != nil {
		return fmt.Errorf("set release binary mode: %w", err)
	}
	return nil
}

func writeArchive(
	path, binaryPath string,
	license, commercial, readme []byte,
	builtAt time.Time,
) error {
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read staged release binary: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create release archive: %w", err)
	}
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = builtAt
	gzipWriter.Header.OS = 3
	tarWriter := tar.NewWriter(gzipWriter)
	entries := []struct {
		name string
		mode int64
		data []byte
	}{
		{binaryName, 0o755, binary},
		{licenseName, 0o644, license},
		{commercialName, 0o644, commercial},
		{readmeName, 0o644, readme},
	}
	var writeErr error
	for _, entry := range entries {
		header := &tar.Header{
			Name: entry.name, Mode: entry.mode, Size: int64(len(entry.data)),
			ModTime: builtAt, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR,
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			writeErr = err
			break
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			writeErr = err
			break
		}
	}
	writeErr = errors.Join(writeErr, tarWriter.Close(), gzipWriter.Close(), file.Close())
	if writeErr != nil {
		return fmt.Errorf("write release archive: %w", writeErr)
	}
	return nil
}

func verifyArchive(path, arch string, manifest Manifest) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open release archive: %w", err)
	}
	defer func() { _ = file.Close() }()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return errors.New("release archive gzip stream is invalid")
	}
	defer func() { _ = gzipReader.Close() }()
	reproducibleTime := time.Unix(manifest.SourceDateEpoch, 0).UTC()
	if !gzipReader.Header.ModTime.Equal(reproducibleTime) ||
		gzipReader.Header.Name != "" ||
		gzipReader.Header.Comment != "" ||
		gzipReader.Header.OS != 3 {
		return errors.New("release archive gzip metadata is not reproducible")
	}
	tarReader := tar.NewReader(gzipReader)
	want := []string{binaryName, licenseName, commercialName, readmeName}
	for index, name := range want {
		header, err := tarReader.Next()
		if err != nil || header.Name != name || header.Typeflag != tar.TypeReg {
			return errors.New("release archive has an unexpected layout")
		}
		expectedMode := int64(0o644)
		if name == binaryName {
			expectedMode = 0o755
		}
		if header.Mode != expectedMode ||
			!header.ModTime.Equal(reproducibleTime) ||
			header.Uid != 0 ||
			header.Gid != 0 ||
			header.Uname != "" ||
			header.Gname != "" ||
			len(header.PAXRecords) != 0 {
			return errors.New("release archive metadata is not reproducible")
		}
		maximum := int64(1 << 20)
		if name == binaryName {
			maximum = 128 << 20
		} else if name == licenseName || name == commercialName {
			maximum = 256 << 10
		}
		if header.Size <= 0 || header.Size > maximum {
			return errors.New("release archive entry exceeds its size budget")
		}
		value, err := io.ReadAll(io.LimitReader(tarReader, header.Size+1))
		if err != nil || int64(len(value)) != header.Size {
			return errors.New("release archive entry is invalid")
		}
		if index == 0 {
			if err := validateMachOBytes(value, arch); err != nil {
				return err
			}
			for _, metadata := range []string{manifest.ProductVersion, manifest.Commit, manifest.BuiltAt} {
				if !bytes.Contains(value, []byte(metadata)) {
					return errors.New("release binary is missing traceable version metadata")
				}
			}
		} else if len(value) == 0 {
			return errors.New("release legal or readme content is empty")
		}
	}
	if _, err := tarReader.Next(); !errors.Is(err, io.EOF) {
		return errors.New("release archive has unexpected trailing entries")
	}
	return nil
}

func verifyReleaseFileSet(directory string, manifest Manifest, hasSignature bool) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	expected := map[string]bool{
		"checksums.txt": false,
		"release.json":  false,
	}
	for _, artifact := range manifest.Artifacts {
		expected[artifact.Name] = false
	}
	if hasSignature {
		expected["release.sig"] = false
	}
	if len(entries) != len(expected) {
		return errors.New("release directory has an unexpected file set")
	}
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return errors.New("release directory has an unexpected file set")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("release directory contains a non-regular file")
		}
		expected[entry.Name()] = true
	}
	for _, present := range expected {
		if !present {
			return errors.New("release directory is incomplete")
		}
	}
	return nil
}

func validateSigner(signer Signer) error {
	keyID := strings.TrimSpace(signer.KeyID)
	if keyID == "" || keyID != signer.KeyID || len(keyID) > 128 || len(signer.PrivateKey) != ed25519.PrivateKeySize {
		return errors.New("release signer is invalid")
	}
	for _, character := range keyID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character) {
			continue
		}
		return errors.New("release signer key ID is invalid")
	}
	return nil
}

func signRelease(directory string, signer Signer) error {
	manifestHash, _, err := hashFile(filepath.Join(directory, "release.json"))
	if err != nil {
		return err
	}
	checksumsHash, _, err := hashFile(filepath.Join(directory, "checksums.txt"))
	if err != nil {
		return err
	}
	publicKey := signer.PrivateKey.Public().(ed25519.PublicKey)
	signature := Signature{Version: SignatureVersion, Algorithm: "Ed25519", KeyID: signer.KeyID,
		PublicKey: base64.StdEncoding.EncodeToString(publicKey), ManifestSHA256: manifestHash,
		ChecksumsSHA256: checksumsHash}
	payload, err := signaturePayload(signature)
	if err != nil {
		return err
	}
	signature.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(signer.PrivateKey, payload))
	encoded, err := json.MarshalIndent(signature, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(directory, "release.sig"), encoded, 0o644); err != nil {
		return fmt.Errorf("write release signature: %w", err)
	}
	return nil
}

func verifySignature(directory string, trusted map[string]ed25519.PublicKey, requireTrust bool) (Signature, error) {
	encoded, err := os.ReadFile(filepath.Join(directory, "release.sig"))
	if err != nil {
		return Signature{}, errors.New("signed release requires release.sig")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var signature Signature
	if decoder.Decode(&signature) != nil || decoder.Decode(&struct{}{}) != io.EOF ||
		signature.Version != SignatureVersion || signature.Algorithm != "Ed25519" || !validHash(signature.ManifestSHA256) ||
		!validHash(signature.ChecksumsSHA256) {
		return Signature{}, errors.New("release signature is invalid")
	}
	if validateSigner(Signer{KeyID: signature.KeyID, PrivateKey: make(ed25519.PrivateKey, ed25519.PrivateKeySize)}) != nil {
		return Signature{}, errors.New("release signature key ID is invalid")
	}
	publicKey, publicErr := base64.StdEncoding.Strict().DecodeString(signature.PublicKey)
	signed, signatureErr := base64.StdEncoding.Strict().DecodeString(signature.Signature)
	if publicErr != nil || signatureErr != nil || len(publicKey) != ed25519.PublicKeySize || len(signed) != ed25519.SignatureSize {
		return Signature{}, errors.New("release signature encoding is invalid")
	}
	manifestHash, _, manifestErr := hashFile(filepath.Join(directory, "release.json"))
	checksumsHash, _, checksumsErr := hashFile(filepath.Join(directory, "checksums.txt"))
	if manifestErr != nil || checksumsErr != nil || signature.ManifestSHA256 != manifestHash || signature.ChecksumsSHA256 != checksumsHash {
		return Signature{}, errors.New("release signature payload hashes do not match")
	}
	payload, err := signaturePayload(signature)
	if err != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), payload, signed) {
		return Signature{}, errors.New("release signature verification failed")
	}
	if requireTrust {
		trustedKey, ok := trusted[signature.KeyID]
		if !ok || !bytes.Equal(trustedKey, publicKey) {
			return Signature{}, errors.New("release signer is not trusted")
		}
	}
	return signature, nil
}

func signaturePayload(signature Signature) ([]byte, error) {
	unsigned := struct {
		Version         string `json:"version"`
		Algorithm       string `json:"algorithm"`
		KeyID           string `json:"key_id"`
		PublicKey       string `json:"public_key"`
		ManifestSHA256  string `json:"manifest_sha256"`
		ChecksumsSHA256 string `json:"checksums_sha256"`
	}{signature.Version, signature.Algorithm, signature.KeyID, signature.PublicKey,
		signature.ManifestSHA256, signature.ChecksumsSHA256}
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return nil, err
	}
	return append([]byte("Memora Release Signature v1\n"), encoded...), nil
}

func hasRegularSignature(directory string) (bool, error) {
	info, err := os.Lstat(filepath.Join(directory, "release.sig"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("release signature is not a regular file")
	}
	return true, nil
}

func validateManifest(manifest Manifest) error {
	if manifest.Version != ManifestVersion || manifest.Product != "memora" ||
		!versionPattern.MatchString(manifest.ProductVersion) ||
		!commitPattern.MatchString(manifest.Commit) ||
		manifest.SourceDateEpoch <= 0 ||
		manifest.BuiltAt != time.Unix(manifest.SourceDateEpoch, 0).UTC().Format(time.RFC3339) ||
		!strings.HasPrefix(manifest.GoVersion, "go1.") ||
		len(manifest.Artifacts) != 2 {
		return errors.New("release manifest metadata is invalid")
	}
	seen := map[string]bool{}
	for _, artifact := range manifest.Artifacts {
		wantName := fmt.Sprintf(
			"memora_%s_darwin_%s.tar.gz", manifest.ProductVersion, artifact.Arch,
		)
		if artifact.OS != "darwin" || (artifact.Arch != "arm64" && artifact.Arch != "amd64") ||
			artifact.Name != wantName || !validHash(artifact.SHA256) || artifact.Size <= 0 ||
			seen[artifact.Arch] {
			return errors.New("release manifest artifact is invalid")
		}
		seen[artifact.Arch] = true
	}
	return nil
}

func readChecksums(path string) (map[string]string, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read release checksums: %w", err)
	}
	lines := strings.Split(strings.TrimSuffix(string(value), "\n"), "\n")
	checksums := make(map[string]string, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, "  ")
		if len(parts) != 2 || !validHash(parts[0]) || filepath.Base(parts[1]) != parts[1] ||
			checksums[parts[1]] != "" {
			return nil, errors.New("release checksum manifest is invalid")
		}
		checksums[parts[1]] = parts[0]
	}
	return checksums, nil
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, fmt.Errorf("open release file for hashing: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return "", 0, fmt.Errorf("hash release file: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func validateMachO(path, arch string) error {
	value, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read release binary: %w", err)
	}
	return validateMachOBytes(value, arch)
}

func validateMachOBytes(value []byte, arch string) error {
	file, err := macho.NewFile(bytes.NewReader(value))
	if err != nil {
		return errors.New("release binary is not a valid Mach-O executable")
	}
	defer func() { _ = file.Close() }()
	want := macho.CpuArm64
	if arch == "amd64" {
		want = macho.CpuAmd64
	}
	if file.Cpu != want {
		return fmt.Errorf("release binary CPU = %s, want %s", file.Cpu, arch)
	}
	return nil
}

func validHash(value string) bool {
	if !hashPattern.MatchString(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func releaseEnvironment() []string {
	environment := os.Environ()
	filtered := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "CGO_ENABLED=") ||
			strings.HasPrefix(value, "GOOS=") ||
			strings.HasPrefix(value, "GOARCH=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return filtered
}
