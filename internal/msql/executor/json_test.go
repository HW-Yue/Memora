package executor_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
)

func TestMutationOptionsJSONDistinguishesMissingAndExplicitEmptySnapshots(t *testing.T) {
	t.Parallel()

	missing, err := json.Marshal(executor.MutationOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(missing), "index_terms") ||
		strings.Contains(string(missing), "route_leaf_ids") {
		t.Fatalf("missing snapshots encoded as %s", missing)
	}

	encoded, err := json.Marshal(executor.MutationOptions{
		IndexTerms: []string{}, RouteLeafIDs: []string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"index_terms":[],"route_leaf_ids":[]}` {
		t.Fatalf("explicit empty snapshots encoded as %s", encoded)
	}

	var decoded executor.MutationOptions
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.IndexTerms == nil || decoded.RouteLeafIDs == nil {
		t.Fatalf("explicit empty snapshots decoded as %#v", decoded)
	}
}
