package routevector

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/router"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func prepareGeneration(request PublishRequest, routes []router.Node, sourceSHA256 string) (Manifest, []byte, error) {
	if !validID(request.DatabaseID) || !validID(request.GenerationID) || request.Vectors == nil {
		return Manifest{}, nil, invalid("publish identity and vectors are required")
	}
	spaceDigest, err := validateSpace(request.Space)
	if err != nil {
		return Manifest{}, nil, err
	}
	provided := make(map[string]EncodedVector, len(request.Vectors))
	for _, vector := range request.Vectors {
		if !validID(vector.RouteID) || vector.RouteRevision == 0 || !validSHA256(vector.SurfaceSHA256) {
			return Manifest{}, nil, invalid("encoded Route receipt is invalid")
		}
		if _, duplicate := provided[vector.RouteID]; duplicate {
			return Manifest{}, nil, invalid("encoded Route %q is duplicated", vector.RouteID)
		}
		vector.Values = append([]float32(nil), vector.Values...)
		provided[vector.RouteID] = vector
	}
	if len(provided) != len(routes) {
		return Manifest{}, nil, invalid("generation must cover every current Route exactly once")
	}
	entries := make([]RouteEntry, 0, len(routes))
	encoded := make([]byte, 0, len(routes)*int(request.Space.Dimensions)*4)
	for _, node := range routes {
		vector, exists := provided[node.ID]
		if !exists || vector.RouteRevision != node.Revision {
			return Manifest{}, nil, invalid("Route %q revision receipt is missing or stale", node.ID)
		}
		surface, err := EncodeSurface(node)
		if err != nil || surface.SHA256 != vector.SurfaceSHA256 {
			return Manifest{}, nil, invalid("Route %q surface receipt does not match", node.ID)
		}
		if err := validateVector(vector.Values, request.Space.Dimensions); err != nil {
			return Manifest{}, nil, err
		}
		offset := uint64(len(encoded))
		for _, value := range vector.Values {
			bits := math.Float32bits(value)
			encoded = append(encoded, byte(bits), byte(bits>>8), byte(bits>>16), byte(bits>>24))
		}
		entries = append(entries, RouteEntry{
			DatabaseID: node.DatabaseID, TableID: node.TableID, RouteID: node.ID,
			RouteRevision: node.Revision, SurfaceSHA256: surface.SHA256, VectorOffset: offset,
		})
	}
	return Manifest{
		Version: Version, DatabaseID: request.DatabaseID, GenerationID: request.GenerationID,
		Space: request.Space, SpaceDigest: spaceDigest, SourceSHA256: sourceSHA256,
		VectorCount: uint64(len(entries)), VectorsSHA256: sha256Bytes(encoded), Routes: entries,
	}, encoded, nil
}

func validateSpace(space Space) (string, error) {
	if !validText(space.ModelID, 256, true) || !validText(space.ModelRevision, 128, true) ||
		!validSHA256(space.ModelSHA256) || space.Dimensions == 0 || space.Dimensions > 4096 ||
		space.Scalar != ScalarFloat32 || space.Normalization != NormalizationL2 ||
		space.Similarity != SimilarityDotProduct {
		return "", invalid("embedding space is invalid")
	}
	digest, err := digestJSON(space)
	if err != nil {
		return "", invalid("encode embedding space: %v", err)
	}
	return digest, nil
}

func validateVector(values []float32, dimensions uint32) error {
	if uint32(len(values)) != dimensions {
		return invalid("vector dimensions do not match embedding space")
	}
	var norm float64
	for _, value := range values {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return invalid("vector contains a non-finite component")
		}
		norm += float64(value) * float64(value)
	}
	if math.Abs(norm-1) > 1e-4 {
		return invalid("vector must be L2 normalized")
	}
	return nil
}

func openGenerationAt(path, databaseID, generationID string) (*Generation, string, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(path, manifestFileName))
	if err != nil {
		return nil, "", corrupt("read generation manifest: %v", err)
	}
	var manifest Manifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil || validateManifest(manifest, databaseID, generationID) != nil {
		return nil, "", corrupt("generation manifest is invalid")
	}
	manifestDigest := sha256Bytes(manifestBytes)
	vectorBytes, err := os.ReadFile(filepath.Join(path, vectorsFileName))
	if err != nil || sha256Bytes(vectorBytes) != manifest.VectorsSHA256 {
		return nil, "", corrupt("generation vector checksum mismatch")
	}
	expectedLength := uint64(manifest.Space.Dimensions) * manifest.VectorCount * 4
	if uint64(len(vectorBytes)) != expectedLength {
		return nil, "", corrupt("generation vector length mismatch")
	}
	vectors := make(map[string][]float32, len(manifest.Routes))
	width := uint64(manifest.Space.Dimensions) * 4
	for index, entry := range manifest.Routes {
		if entry.VectorOffset != uint64(index)*width {
			return nil, "", corrupt("generation vector offset mismatch")
		}
		values := make([]float32, manifest.Space.Dimensions)
		for component := range values {
			offset := int(entry.VectorOffset) + component*4
			bits := uint32(vectorBytes[offset]) | uint32(vectorBytes[offset+1])<<8 |
				uint32(vectorBytes[offset+2])<<16 | uint32(vectorBytes[offset+3])<<24
			values[component] = math.Float32frombits(bits)
		}
		if err := validateVector(values, manifest.Space.Dimensions); err != nil {
			return nil, "", corrupt("generation vector is invalid")
		}
		vectors[entry.RouteID] = values
	}
	return &Generation{manifest: manifest, vectors: vectors}, manifestDigest, nil
}

func validateManifest(manifest Manifest, databaseID, generationID string) error {
	spaceDigest, err := validateSpace(manifest.Space)
	if err != nil || manifest.Version != Version || manifest.DatabaseID != databaseID ||
		manifest.GenerationID != generationID || manifest.SpaceDigest != spaceDigest ||
		!validSHA256(manifest.SourceSHA256) || !validSHA256(manifest.VectorsSHA256) ||
		manifest.Routes == nil || manifest.VectorCount != uint64(len(manifest.Routes)) {
		return ErrCorrupt
	}
	for index, entry := range manifest.Routes {
		if entry.DatabaseID != databaseID || !validID(entry.TableID) || !validID(entry.RouteID) ||
			entry.RouteRevision == 0 || !validSHA256(entry.SurfaceSHA256) ||
			(index > 0 && manifest.Routes[index-1].RouteID >= entry.RouteID) {
			return ErrCorrupt
		}
	}
	return nil
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON content")
	}
	return nil
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return sha256Bytes(encoded), nil
}

func sha256Value(value string) string { return sha256Bytes([]byte(value)) }

func sha256Bytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validSHA256(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func validID(value string) bool {
	return value != "." && value != ".." && utf8.ValidString(value) && idPattern.MatchString(value)
}

func markerPath(root, databaseID string) string {
	return filepath.Join(databasePath(root, databaseID), markerFileName)
}

func databasePath(root, databaseID string) string {
	return filepath.Join(root, layoutDirName, databaseID)
}

func generationPath(root, databaseID, generationID string) string {
	return filepath.Join(databasePath(root, databaseID), generationsName, generationID)
}

func corrupt(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrCorrupt, fmt.Sprintf(format, arguments...))
}
