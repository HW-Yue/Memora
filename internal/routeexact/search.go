// Package routeexact implements the deterministic pure-Go reference backend
// for matching query vectors against Route-only vector generations.
package routeexact

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/routevector"
)

const (
	maxDimensions = 4096
	maxTopK       = 64
)

var (
	ErrInvalidQuery = errors.New("invalid exact Route query")
	ErrInvalidScope = errors.New("invalid exact Route scope")
)

type Query struct {
	SpaceDigest string
	Vector      []float32
	Limit       int
}

type Scope struct {
	DatabaseID      string
	Generation      *routevector.Generation
	AllowedTableIDs map[string]struct{}
}

type Match struct {
	DatabaseID    string
	TableID       string
	RouteID       string
	RouteRevision uint64
	Score         float64
}

type Result struct {
	Matches []Match
	Scanned uint64
}

// GenerationReceipt identifies exactly which immutable generations
// participated in a discovery snapshot.
type GenerationReceipt struct {
	DatabaseID     string `json:"database_id"`
	GenerationID   string `json:"generation_id"`
	MarkerRevision uint64 `json:"marker_revision"`
	ManifestSHA256 string `json:"manifest_sha256"`
}

func Search(query Query, scopes []Scope) (Result, error) {
	queryVector := append([]float32(nil), query.Vector...)
	if err := validateQuery(query.SpaceDigest, queryVector, query.Limit); err != nil {
		return Result{}, err
	}
	seenDatabases := make(map[string]struct{}, len(scopes))
	matches := make([]Match, 0)
	var scanned uint64
	for _, scope := range scopes {
		if scope.DatabaseID == "" || scope.Generation == nil || scope.AllowedTableIDs == nil {
			return Result{}, scopeError("Database, generation, and Table scope are required")
		}
		if _, duplicate := seenDatabases[scope.DatabaseID]; duplicate {
			return Result{}, scopeError("Database %q is duplicated", scope.DatabaseID)
		}
		seenDatabases[scope.DatabaseID] = struct{}{}
		manifest := scope.Generation.Manifest()
		if manifest.DatabaseID != scope.DatabaseID || manifest.SpaceDigest != query.SpaceDigest {
			return Result{}, scopeError("Database %q generation is outside the query embedding space", scope.DatabaseID)
		}
		if int(manifest.Space.Dimensions) != len(queryVector) {
			return Result{}, queryError("vector dimensions do not match the requested embedding space")
		}
		allowed := make(map[string]struct{}, len(scope.AllowedTableIDs))
		for tableID := range scope.AllowedTableIDs {
			if tableID == "" {
				return Result{}, scopeError("Table scope contains an empty identity")
			}
			allowed[tableID] = struct{}{}
		}
		for _, entry := range manifest.Routes {
			// Authorization and Table scope are checked before the vector is
			// fetched or a component participates in a dot product.
			if _, ok := allowed[entry.TableID]; !ok {
				continue
			}
			vector, ok := scope.Generation.Vector(entry.RouteID)
			if !ok || len(vector) != len(queryVector) {
				return Result{}, scopeError("Route %q vector is unavailable", entry.RouteID)
			}
			var score float64
			for index := range queryVector {
				score += float64(queryVector[index]) * float64(vector[index])
			}
			scanned++
			matches = append(matches, Match{
				DatabaseID: entry.DatabaseID, TableID: entry.TableID, RouteID: entry.RouteID,
				RouteRevision: entry.RouteRevision, Score: score,
			})
		}
	}
	sort.Slice(matches, func(left, right int) bool {
		if matches[left].Score != matches[right].Score {
			return matches[left].Score > matches[right].Score
		}
		if matches[left].RouteID != matches[right].RouteID {
			return matches[left].RouteID < matches[right].RouteID
		}
		if matches[left].DatabaseID != matches[right].DatabaseID {
			return matches[left].DatabaseID < matches[right].DatabaseID
		}
		return matches[left].TableID < matches[right].TableID
	})
	if len(matches) > query.Limit {
		matches = matches[:query.Limit]
	}
	return Result{Matches: append([]Match(nil), matches...), Scanned: scanned}, nil
}

// Snapshot binds a current Catalog/Route snapshot to the requested embedding
// space and the exact active generation markers used by Search.
func Snapshot(baseSnapshot, spaceDigest string, receipts []GenerationReceipt) (string, error) {
	if !validDigest(baseSnapshot) || !validDigest(spaceDigest) {
		return "", scopeError("base snapshot and embedding space digest are required")
	}
	cloned := append([]GenerationReceipt(nil), receipts...)
	sort.Slice(cloned, func(left, right int) bool { return cloned[left].DatabaseID < cloned[right].DatabaseID })
	for index, receipt := range cloned {
		if receipt.DatabaseID == "" || receipt.GenerationID == "" || receipt.MarkerRevision == 0 ||
			!validDigest(receipt.ManifestSHA256) ||
			(index > 0 && cloned[index-1].DatabaseID == receipt.DatabaseID) {
			return "", scopeError("generation receipt is invalid")
		}
	}
	encoded, err := json.Marshal(struct {
		Version      string              `json:"version"`
		BaseSnapshot string              `json:"base_snapshot"`
		SpaceDigest  string              `json:"space_digest"`
		Generations  []GenerationReceipt `json:"generations"`
	}{
		Version: "memora.route-exact-snapshot/v1", BaseSnapshot: baseSnapshot,
		SpaceDigest: spaceDigest, Generations: cloned,
	})
	if err != nil {
		return "", scopeError("encode exact snapshot: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validateQuery(spaceDigest string, vector []float32, limit int) error {
	if !validDigest(spaceDigest) {
		return queryError("embedding space digest is invalid")
	}
	if len(vector) == 0 || len(vector) > maxDimensions {
		return queryError("vector dimensions must be between 1 and %d", maxDimensions)
	}
	if limit < 1 || limit > maxTopK {
		return queryError("Top K must be between 1 and %d", maxTopK)
	}
	var norm float64
	for _, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return queryError("vector contains a non-finite component")
		}
		norm += float64(value) * float64(value)
	}
	if math.Abs(norm-1) > 1e-4 {
		return queryError("vector must be L2 normalized")
	}
	return nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != 71 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}

func queryError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, arguments...))
}

func scopeError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidScope, fmt.Sprintf(format, arguments...))
}
