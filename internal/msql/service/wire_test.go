package service

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

// An option that does not survive this conversion is a feature no real caller
// can reach: the CLI, MCP and SDK all arrive as protocol requests, so dropping
// route_path here would leave it working only in-process.
func TestRoutePathSurvivesTheWireConversion(t *testing.T) {
	options := toMutationOptions(protocolmsql.MutationOptions{
		RoutePath: []protocolmsql.PathSegment{
			{Name: "architecture", Kind: "branch", Purpose: "架构决策"},
			{Name: "sqlite", Kind: "leaf", Purpose: "为什么选 SQLite"},
		},
	})
	if len(options.RoutePath) != 2 {
		t.Fatalf("route path = %+v", options.RoutePath)
	}
	if options.RoutePath[0].Kind != router.KindBranch || options.RoutePath[0].Purpose != "架构决策" {
		t.Fatalf("first segment = %+v", options.RoutePath[0])
	}
	if options.RoutePath[1].Kind != router.KindLeaf || options.RoutePath[1].Name != "sqlite" {
		t.Fatalf("last segment = %+v", options.RoutePath[1])
	}
}
