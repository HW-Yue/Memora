package executor_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

func TestMutationOptionsJSONDistinguishesMissingAndExplicitEmptyRouteSnapshot(t *testing.T) {
	t.Parallel()
	missing, err := json.Marshal(executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(missing), "route_leaf_ids") {
		t.Fatalf("missing Route snapshot encoded as %s", missing)
	}
	encoded, err := json.Marshal(executor.MutationOptions{RouteLeafIDs: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"route_leaf_ids":[]}` {
		t.Fatalf("explicit empty Route snapshot encoded as %s", encoded)
	}
	var decoded executor.MutationOptions
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.RouteLeafIDs == nil {
		t.Fatalf("explicit empty Route snapshot decoded as %#v", decoded)
	}
}
