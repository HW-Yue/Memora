package pagestoremigration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	AuthorityMarkerFilename = "page-authority-v1.json"
	authorityMarkerVersion  = "memora.page-store-authority/v1"
	maxAuthorityMarkerBytes = 16 << 10
)

type authorityMarker struct {
	Version           string `json:"version"`
	Epoch             uint64 `json:"epoch,omitempty"`
	Generation        string `json:"generation"`
	PlanDigest        string `json:"plan_digest"`
	SourceFingerprint string `json:"source_sha256"`
	Digest            string `json:"digest"`
}

type authorityMarkerOperations struct {
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
	syncDir    func(string) error
}

func newAuthorityMarker(manifest generationManifest) (authorityMarker, error) {
	marker := authorityMarker{
		Version: authorityMarkerVersion, Generation: GenerationDirectory,
		PlanDigest: manifest.PlanDigest, SourceFingerprint: manifest.SourceFingerprint,
	}
	digest, err := authorityMarkerDigest(marker)
	marker.Digest = digest
	return marker, err
}

func newReplacementAuthorityMarker(manifest generationManifest, epoch uint64) (authorityMarker, error) {
	if epoch == 0 {
		return authorityMarker{}, fmt.Errorf("%w: replacement epoch", ErrInvalid)
	}
	marker := authorityMarker{
		Version: authorityMarkerVersion, Epoch: epoch,
		Generation: replacementGenerationName(epoch, manifest.PlanDigest),
		PlanDigest: manifest.PlanDigest, SourceFingerprint: manifest.SourceFingerprint,
	}
	digest, err := authorityMarkerDigest(marker)
	marker.Digest = digest
	if err == nil {
		err = marker.validate(manifest)
	}
	return marker, err
}

func (marker authorityMarker) validate(manifest generationManifest) error {
	if err := marker.validateSelf(); err != nil {
		return err
	}
	if marker.PlanDigest != manifest.PlanDigest || marker.SourceFingerprint != manifest.SourceFingerprint {
		return fmt.Errorf("%w: authority marker binding", ErrTargetCorrupt)
	}
	return nil
}

func (marker authorityMarker) validateSelf() error {
	if marker.Version != authorityMarkerVersion ||
		!canonicalSHA256(marker.PlanDigest) || !canonicalSHA256(marker.SourceFingerprint) ||
		!canonicalSHA256(marker.Digest) ||
		marker.Generation != replacementGenerationName(marker.Epoch, marker.PlanDigest) {
		return fmt.Errorf("%w: authority marker binding", ErrTargetCorrupt)
	}
	digest, err := authorityMarkerDigest(marker)
	if err != nil || digest != marker.Digest {
		return fmt.Errorf("%w: authority marker digest", ErrTargetCorrupt)
	}
	return nil
}

func authorityMarkerDigest(marker authorityMarker) (string, error) {
	if marker.Epoch != 0 {
		payload := struct {
			Version           string `json:"version"`
			Epoch             uint64 `json:"epoch"`
			Generation        string `json:"generation"`
			PlanDigest        string `json:"plan_digest"`
			SourceFingerprint string `json:"source_sha256"`
		}{marker.Version, marker.Epoch, marker.Generation, marker.PlanDigest, marker.SourceFingerprint}
		encoded, err := json.Marshal(payload)
		if err != nil {
			return "", err
		}
		digest := sha256.Sum256(encoded)
		return hex.EncodeToString(digest[:]), nil
	}
	payload := struct {
		Version           string `json:"version"`
		Generation        string `json:"generation"`
		PlanDigest        string `json:"plan_digest"`
		SourceFingerprint string `json:"source_sha256"`
	}{marker.Version, marker.Generation, marker.PlanDigest, marker.SourceFingerprint}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func replacementGenerationName(epoch uint64, planDigest string) string {
	if epoch == 0 {
		return GenerationDirectory
	}
	if !canonicalSHA256(planDigest) {
		return ""
	}
	return GenerationDirectory + ".g" + fmt.Sprintf("%020d", epoch) + "." + planDigest
}

func parseReplacementGenerationName(name string) (uint64, string, error) {
	if name == GenerationDirectory {
		return 0, "", nil
	}
	prefix := GenerationDirectory + ".g"
	if !strings.HasPrefix(name, prefix) {
		return 0, "", ErrTargetCorrupt
	}
	rest := strings.TrimPrefix(name, prefix)
	if len(rest) != 20+1+sha256.Size*2 || rest[20] != '.' {
		return 0, "", ErrTargetCorrupt
	}
	epoch, err := strconv.ParseUint(rest[:20], 10, 64)
	digest := rest[21:]
	if err != nil || epoch == 0 || !canonicalSHA256(digest) || replacementGenerationName(epoch, digest) != name {
		return 0, "", ErrTargetCorrupt
	}
	return epoch, digest, nil
}

func readAuthorityMarker(databaseDirectory string, manifest generationManifest) (authorityMarker, error) {
	marker, err := decodeAuthorityMarker(databaseDirectory)
	if err != nil {
		return authorityMarker{}, err
	}
	if err := marker.validate(manifest); err != nil {
		return authorityMarker{}, err
	}
	return marker, nil
}

func decodeAuthorityMarker(databaseDirectory string) (authorityMarker, error) {
	path := filepath.Join(databaseDirectory, AuthorityMarkerFilename)
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() {
		return authorityMarker{}, fmt.Errorf("%w: authority marker path type", ErrTargetCorrupt)
	}
	file, err := os.Open(path)
	if err != nil {
		return authorityMarker{}, fmt.Errorf("%w: open authority marker: %v", ErrTargetCorrupt, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxAuthorityMarkerBytes {
		return authorityMarker{}, fmt.Errorf("%w: authority marker type or size", ErrTargetCorrupt)
	}
	decoder := json.NewDecoder(io.LimitReader(file, maxAuthorityMarkerBytes+1))
	decoder.DisallowUnknownFields()
	var marker authorityMarker
	if err := decoder.Decode(&marker); err != nil {
		return authorityMarker{}, fmt.Errorf("%w: decode authority marker: %v", ErrTargetCorrupt, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return authorityMarker{}, fmt.Errorf("%w: authority marker trailing data", ErrTargetCorrupt)
	}
	if err := marker.validateSelf(); err != nil {
		return authorityMarker{}, err
	}
	return marker, nil
}

func writeAuthorityMarker(databaseDirectory string, marker authorityMarker) error {
	return writeAuthorityMarkerWithOperations(databaseDirectory, marker, authorityMarkerOperations{
		createTemp: os.CreateTemp, rename: os.Rename, syncDir: syncGenerationDirectory,
	})
}

func writeAuthorityMarkerWithOperations(
	databaseDirectory string,
	marker authorityMarker,
	operations authorityMarkerOperations,
) error {
	if operations.createTemp == nil || operations.rename == nil || operations.syncDir == nil {
		return fmt.Errorf("%w: authority marker operations", ErrInvalid)
	}
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	temporary, err := operations.createTemp(databaseDirectory, ".page-authority-v1-")
	if err != nil {
		return fmt.Errorf("create authority marker staging file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync authority marker staging file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := filepath.Join(databaseDirectory, AuthorityMarkerFilename)
	if err := operations.rename(temporaryPath, path); err != nil {
		return fmt.Errorf("publish authority marker: %w", err)
	}
	if err := operations.syncDir(databaseDirectory); err != nil {
		return fmt.Errorf("%w: sync authority marker directory: %v", ErrOutcomeUnknown, err)
	}
	return nil
}
