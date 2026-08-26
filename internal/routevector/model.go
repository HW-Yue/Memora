package routevector

import (
	"context"
	"errors"
	"sync"

	"github.com/HW-Yue/Memora/internal/router"
)

const (
	Version        = "memora.route-vector-generation/v1"
	MarkerVersion  = "memora.route-vector-marker/v1"
	SurfaceVersion = "memora.route-vector-surface/v1"

	ScalarFloat32        = "float32"
	NormalizationL2      = "l2"
	SimilarityDotProduct = "dot_product"
)

const (
	layoutDirName    = "route-vector-v1"
	generationsName  = "generations"
	manifestFileName = "manifest.json"
	vectorsFileName  = "vectors.bin"
	markerFileName   = "active.json"
)

var (
	ErrInvalid          = errors.New("invalid Route vector generation")
	ErrUnavailable      = errors.New("Route vector generation unavailable")
	ErrStale            = errors.New("Route vector generation stale")
	ErrSourceChanged    = errors.New("Route vector source changed during build")
	ErrCorrupt          = errors.New("Route vector generation corrupt")
	ErrConflict         = errors.New("Route vector generation conflict")
	ErrRevisionConflict = errors.New("Route vector marker revision conflict")
	ErrOutcomeUnknown   = errors.New("Route vector marker outcome unknown")
)

type FaultPoint string

const (
	FaultBeforeStageWrite       FaultPoint = "before_stage_write"
	FaultBeforeStageSync        FaultPoint = "before_stage_sync"
	FaultBeforeGenerationRename FaultPoint = "before_generation_rename"
	FaultAfterGenerationRename  FaultPoint = "after_generation_rename"
	FaultBeforeGenerationsSync  FaultPoint = "before_generations_sync"
	FaultBeforeMarkerWrite      FaultPoint = "before_marker_write"
	FaultBeforeMarkerSync       FaultPoint = "before_marker_sync"
	FaultBeforeMarkerRename     FaultPoint = "before_marker_rename"
	FaultAfterMarkerRename      FaultPoint = "after_marker_rename"
	FaultBeforeParentSync       FaultPoint = "before_parent_sync"
)

type RouteSource interface {
	ListRouterNodes(context.Context) ([]router.Node, error)
}

type Options struct {
	Fault func(FaultPoint) error
}

type Space struct {
	ModelID       string `json:"model_id"`
	ModelRevision string `json:"model_revision"`
	ModelSHA256   string `json:"model_sha256"`
	Dimensions    uint32 `json:"dimensions"`
	Scalar        string `json:"scalar"`
	Normalization string `json:"normalization"`
	Similarity    string `json:"similarity"`
}

type Surface struct {
	Version string
	Text    string
	SHA256  string
}

type EncodedVector struct {
	RouteID       string
	RouteRevision uint64
	SurfaceSHA256 string
	Values        []float32
}

type PublishRequest struct {
	DatabaseID       string
	GenerationID     string
	ExpectedRevision uint64
	Space            Space
	Vectors          []EncodedVector
}

type RouteEntry struct {
	DatabaseID    string `json:"database_id"`
	TableID       string `json:"table_id"`
	RouteID       string `json:"route_id"`
	RouteRevision uint64 `json:"route_revision"`
	SurfaceSHA256 string `json:"surface_sha256"`
	VectorOffset  uint64 `json:"vector_offset"`
}

type Manifest struct {
	Version       string       `json:"version"`
	DatabaseID    string       `json:"database_id"`
	GenerationID  string       `json:"generation_id"`
	Space         Space        `json:"space"`
	SpaceDigest   string       `json:"space_digest"`
	SourceSHA256  string       `json:"source_sha256"`
	VectorCount   uint64       `json:"vector_count"`
	VectorsSHA256 string       `json:"vectors_sha256"`
	Routes        []RouteEntry `json:"routes"`
}

type Marker struct {
	Version        string `json:"version"`
	DatabaseID     string `json:"database_id"`
	Revision       uint64 `json:"revision"`
	GenerationID   string `json:"generation_id"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

type Generation struct {
	manifest Manifest
	vectors  map[string][]float32
}

type Service struct {
	root   string
	source RouteSource
	fault  func(FaultPoint) error
	mu     sync.Mutex
	// opened caches the Generation each Database is currently serving.
	//
	// A published Generation is immutable and identified by its manifest
	// digest, so a cache entry is either the right one or plainly not — there
	// is nothing to invalidate, only a digest to compare. Without it every
	// query re-read and re-verified every Route vector in the Generation.
	opened map[string]openedGeneration
}

type openedGeneration struct {
	marker     Marker
	generation *Generation
}

func (generation *Generation) Manifest() Manifest {
	if generation == nil {
		return Manifest{}
	}
	return cloneManifest(generation.manifest)
}

func (generation *Generation) Vector(routeID string) ([]float32, bool) {
	if generation == nil {
		return nil, false
	}
	value, ok := generation.vectors[routeID]
	return append([]float32(nil), value...), ok
}

func cloneManifest(value Manifest) Manifest {
	value.Routes = append([]RouteEntry(nil), value.Routes...)
	return value
}
