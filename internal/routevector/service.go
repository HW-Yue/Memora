package routevector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/router"
)

func New(root string, source RouteSource, options Options) (*Service, error) {
	if strings.TrimSpace(root) == "" || source == nil {
		return nil, invalid("root and Route source are required")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, invalid("resolve root: %v", err)
	}
	return &Service{root: absolute, source: source, fault: options.Fault}, nil
}

func (service *Service) Publish(ctx context.Context, request PublishRequest) (Manifest, Marker, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Manifest{}, Marker{}, err
	}
	if !validID(request.DatabaseID) || !validID(request.GenerationID) {
		return Manifest{}, Marker{}, invalid("publish identity is invalid")
	}
	routes, sourceDigest, err := service.currentSource(ctx, request.DatabaseID)
	if err != nil {
		return Manifest{}, Marker{}, err
	}
	manifest, vectorBytes, err := prepareGeneration(request, routes, sourceDigest)
	if err != nil {
		return Manifest{}, Marker{}, err
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return Manifest{}, Marker{}, invalid("encode manifest: %v", err)
	}
	manifestDigest := sha256Bytes(manifestBytes)
	current, found, err := service.loadMarker(request.DatabaseID)
	if err != nil && !errors.Is(err, ErrUnavailable) {
		return Manifest{}, Marker{}, err
	}
	if found && current.GenerationID == request.GenerationID && current.ManifestSHA256 == manifestDigest {
		generation, digest, openErr := openGenerationAt(
			generationPath(service.root, request.DatabaseID, request.GenerationID), request.DatabaseID, request.GenerationID,
		)
		if openErr != nil || digest != manifestDigest || generation.manifest.SourceSHA256 != sourceDigest {
			return Manifest{}, Marker{}, corrupt("idempotent generation does not match active marker")
		}
		return cloneManifest(generation.manifest), current, nil
	}
	currentRevision := uint64(0)
	if found {
		currentRevision = current.Revision
	}
	if request.ExpectedRevision != currentRevision {
		return Manifest{}, Marker{}, fmt.Errorf("%w: got %d, current %d", ErrRevisionConflict, request.ExpectedRevision, currentRevision)
	}
	if err := service.installGeneration(request.DatabaseID, request.GenerationID, manifestBytes, vectorBytes, manifestDigest); err != nil {
		return Manifest{}, Marker{}, err
	}
	_, verifiedDigest, err := service.currentSource(ctx, request.DatabaseID)
	if err != nil {
		return Manifest{}, Marker{}, err
	}
	if verifiedDigest != sourceDigest {
		return Manifest{}, Marker{}, ErrSourceChanged
	}
	marker := Marker{
		Version: MarkerVersion, DatabaseID: request.DatabaseID, Revision: currentRevision + 1,
		GenerationID: request.GenerationID, ManifestSHA256: manifestDigest,
	}
	if err := service.publishMarker(marker); err != nil {
		return Manifest{}, Marker{}, err
	}
	return cloneManifest(manifest), marker, nil
}

func (service *Service) OpenActive(ctx context.Context, databaseID string) (*Generation, Marker, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, Marker{}, err
	}
	marker, found, err := service.loadMarker(databaseID)
	if err != nil {
		return nil, Marker{}, err
	}
	if !found {
		return nil, Marker{}, ErrUnavailable
	}
	generation, err := service.generationFor(marker, databaseID)
	if err != nil {
		return nil, Marker{}, err
	}
	// The source digest is still recomputed every time: it is what detects a
	// Generation that has fallen behind the Routes it was built from, and that
	// can change without the marker changing.
	_, sourceDigest, err := service.currentSource(ctx, databaseID)
	if err != nil {
		return nil, Marker{}, err
	}
	if sourceDigest != generation.manifest.SourceSHA256 {
		return nil, Marker{}, ErrStale
	}
	return generation, marker, nil
}

// generationFor returns the Generation a marker names, reading it from disk
// only when the cached one is for a different marker.
func (service *Service) generationFor(marker Marker, databaseID string) (*Generation, error) {
	if cached, ok := service.opened[databaseID]; ok && cached.marker == marker {
		return cached.generation, nil
	}
	generation, manifestDigest, err := openGenerationAt(
		generationPath(service.root, databaseID, marker.GenerationID), databaseID, marker.GenerationID,
	)
	if err != nil {
		return nil, err
	}
	if manifestDigest != marker.ManifestSHA256 {
		return nil, corrupt("active marker manifest digest mismatch")
	}
	if service.opened == nil {
		service.opened = make(map[string]openedGeneration, 1)
	}
	service.opened[databaseID] = openedGeneration{marker: marker, generation: generation}
	return generation, nil
}

func (service *Service) Reclaim(ctx context.Context, databaseID string, retain []string) ([]string, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	marker, found, err := service.loadMarker(databaseID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrUnavailable
	}
	retained := make(map[string]struct{}, len(retain)+1)
	retained[marker.GenerationID] = struct{}{}
	for _, generationID := range retain {
		if !validID(generationID) {
			return nil, invalid("retained generation ID is invalid")
		}
		retained[generationID] = struct{}{}
	}
	directory := filepath.Join(databasePath(service.root, databaseID), generationsName)
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	removed := make([]string, 0)
	for _, entry := range entries {
		generationID := entry.Name()
		if !entry.IsDir() || !validID(generationID) || strings.HasPrefix(generationID, ".") {
			continue
		}
		if _, keep := retained[generationID]; keep {
			continue
		}
		path := generationPath(service.root, databaseID, generationID)
		if _, _, err := openGenerationAt(path, databaseID, generationID); err != nil {
			return nil, err
		}
		if err := os.RemoveAll(path); err != nil {
			return nil, err
		}
		removed = append(removed, generationID)
	}
	if len(removed) > 0 {
		if err := syncDirectory(directory); err != nil {
			return nil, err
		}
	}
	sort.Strings(removed)
	return removed, nil
}

func (service *Service) currentSource(ctx context.Context, databaseID string) ([]router.Node, string, error) {
	nodes, err := service.source.ListRouterNodes(ctx)
	if err != nil {
		return nil, "", err
	}
	return captureSource(nodes, databaseID)
}

func (service *Service) installGeneration(
	databaseID, generationID string,
	manifestBytes, vectorBytes []byte,
	manifestDigest string,
) error {
	databaseDirectory := databasePath(service.root, databaseID)
	generationsDirectory := filepath.Join(databaseDirectory, generationsName)
	if err := os.MkdirAll(generationsDirectory, 0o700); err != nil {
		return err
	}
	target := generationPath(service.root, databaseID, generationID)
	if info, err := os.Stat(target); err == nil {
		if !info.IsDir() {
			return ErrConflict
		}
		_, digest, openErr := openGenerationAt(target, databaseID, generationID)
		if openErr != nil || digest != manifestDigest {
			return ErrConflict
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := service.inject(FaultBeforeStageWrite); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(generationsDirectory, ".staging-")
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(staging)
		}
	}()
	vectorFile, err := writeFile(filepath.Join(staging, vectorsFileName), vectorBytes)
	if err != nil {
		return err
	}
	defer func() { _ = vectorFile.Close() }()
	manifestFile, err := writeFile(filepath.Join(staging, manifestFileName), manifestBytes)
	if err != nil {
		_ = vectorFile.Close()
		return err
	}
	defer func() { _ = manifestFile.Close() }()
	if err := service.inject(FaultBeforeStageSync); err != nil {
		_ = vectorFile.Close()
		_ = manifestFile.Close()
		return err
	}
	if err := vectorFile.Sync(); err != nil {
		return err
	}
	if err := manifestFile.Sync(); err != nil {
		return err
	}
	if err := vectorFile.Close(); err != nil {
		return err
	}
	if err := manifestFile.Close(); err != nil {
		return err
	}
	if err := syncDirectory(staging); err != nil {
		return err
	}
	_, digest, err := openGenerationAt(staging, databaseID, generationID)
	if err != nil || digest != manifestDigest {
		return corrupt("staged generation verification failed")
	}
	if err := service.inject(FaultBeforeGenerationRename); err != nil {
		return err
	}
	if err := os.Rename(staging, target); err != nil {
		return err
	}
	renamed = true
	if err := service.inject(FaultAfterGenerationRename); err != nil {
		return err
	}
	if err := service.inject(FaultBeforeGenerationsSync); err != nil {
		return err
	}
	return syncDirectory(generationsDirectory)
}

func (service *Service) publishMarker(marker Marker) error {
	directory := databasePath(service.root, marker.DatabaseID)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	if err := service.inject(FaultBeforeMarkerWrite); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".active-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	renamed := false
	defer func() {
		_ = temporary.Close()
		if !renamed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		return err
	}
	if err := service.inject(FaultBeforeMarkerSync); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := service.inject(FaultBeforeMarkerRename); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, markerPath(service.root, marker.DatabaseID)); err != nil {
		return err
	}
	renamed = true
	if err := service.inject(FaultAfterMarkerRename); err != nil {
		return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	if err := service.inject(FaultBeforeParentSync); err != nil {
		return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	if err := syncDirectory(directory); err != nil {
		return fmt.Errorf("%w: %v", ErrOutcomeUnknown, err)
	}
	return nil
}

func (service *Service) loadMarker(databaseID string) (Marker, bool, error) {
	if !validID(databaseID) {
		return Marker{}, false, invalid("Database ID is invalid")
	}
	encoded, err := os.ReadFile(markerPath(service.root, databaseID))
	if errors.Is(err, os.ErrNotExist) {
		return Marker{}, false, ErrUnavailable
	}
	if err != nil {
		return Marker{}, false, err
	}
	var marker Marker
	if decodeStrict(encoded, &marker) != nil || marker.Version != MarkerVersion ||
		marker.DatabaseID != databaseID || marker.Revision == 0 || !validID(marker.GenerationID) ||
		!validSHA256(marker.ManifestSHA256) {
		return Marker{}, false, corrupt("active marker is invalid")
	}
	return marker, true, nil
}

func (service *Service) inject(point FaultPoint) error {
	if service.fault == nil {
		return nil
	}
	if err := service.fault(point); err != nil {
		return fmt.Errorf("Route vector fault at %s: %w", point, err)
	}
	return nil
}

func writeFile(path string, content []byte) (*os.File, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}
