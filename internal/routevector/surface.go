package routevector

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/router"
)

type surfaceWire struct {
	Version  string   `json:"version"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Path     string   `json:"path"`
	Purpose  string   `json:"purpose"`
	Boundary string   `json:"boundary,omitempty"`
}

type sourceRoute struct {
	DatabaseID    string `json:"database_id"`
	TableID       string `json:"table_id"`
	RouteID       string `json:"route_id"`
	RouteRevision uint64 `json:"route_revision"`
	SurfaceSHA256 string `json:"surface_sha256"`
}

func EncodeSurface(node router.Node) (Surface, error) {
	if err := validateRoute(node); err != nil {
		return Surface{}, err
	}
	aliases := append([]string{}, node.Aliases...)
	for _, alias := range aliases {
		if !validText(alias, 512, false) {
			return Surface{}, invalid("Route alias is invalid")
		}
	}
	sort.Strings(aliases)
	encoded, err := json.Marshal(surfaceWire{
		Version: SurfaceVersion, Name: node.Name, Aliases: aliases, Path: node.Path,
		Purpose: node.Purpose, Boundary: node.Synopsis,
	})
	if err != nil {
		return Surface{}, invalid("encode semantic surface: %v", err)
	}
	return Surface{Version: SurfaceVersion, Text: string(encoded), SHA256: sha256Bytes(encoded)}, nil
}

func captureSource(nodes []router.Node, databaseID string) ([]router.Node, string, error) {
	if nodes == nil || !validID(databaseID) {
		return nil, "", invalid("Route source or Database ID is invalid")
	}
	current := make(map[string]router.Node)
	for _, node := range nodes {
		if node.DatabaseID != databaseID || node.TableID == "" {
			continue
		}
		if err := validateRoute(node); err != nil {
			return nil, "", err
		}
		if _, duplicate := current[node.ID]; duplicate {
			return nil, "", invalid("Route %q is duplicated", node.ID)
		}
		if !node.Deleted {
			node.Aliases = append([]string{}, node.Aliases...)
			current[node.ID] = node
		}
	}
	routes := make([]router.Node, 0, len(current))
	for _, node := range current {
		routes = append(routes, node)
	}
	sort.Slice(routes, func(left, right int) bool { return routes[left].ID < routes[right].ID })
	snapshot := make([]sourceRoute, 0, len(routes))
	for _, node := range routes {
		surface, err := EncodeSurface(node)
		if err != nil {
			return nil, "", err
		}
		snapshot = append(snapshot, sourceRoute{
			DatabaseID: node.DatabaseID, TableID: node.TableID, RouteID: node.ID,
			RouteRevision: node.Revision, SurfaceSHA256: surface.SHA256,
		})
	}
	digest, err := digestJSON(snapshot)
	if err != nil {
		return nil, "", invalid("encode Route source: %v", err)
	}
	return routes, digest, nil
}

func validateRoute(node router.Node) error {
	if node.Version != router.Version || !validID(node.ID) || !validID(node.DatabaseID) ||
		!validID(node.TableID) || node.Revision == 0 || node.Deleted ||
		(node.Kind != router.KindRoot && node.Kind != router.KindBranch && node.Kind != router.KindLeaf) ||
		!validText(node.Name, 512, node.Kind != router.KindRoot) || !validText(node.Path, 2048, true) ||
		!validText(node.Purpose, 4096, true) || !validText(node.Synopsis, 4096, false) {
		return invalid("Route semantic surface is invalid")
	}
	return nil
}

func validText(value string, maximum int, required bool) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximum ||
		strings.ContainsAny(value, "\x00\r") {
		return false
	}
	return !required || strings.TrimSpace(value) != ""
}

func invalid(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
