package msql_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/protocol/msql"
)

func TestRequestValidationAndExplicitEmptyRouteMembership(t *testing.T) {
	t.Parallel()
	request := validProtocolRequest()
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(request.Statements[0].Mutation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(encoded, []byte(`"route_leaf_ids":[]`)) ||
		!bytes.Contains(encoded, []byte(`"target_route_leaf_ids":[]`)) {
		t.Fatalf("explicit empty membership lost: %s", encoded)
	}
	for _, mutate := range []func(*msql.Request){
		func(value *msql.Request) { value.Version = "future" },
		func(value *msql.Request) { value.Source = " " },
		func(value *msql.Request) { value.Statements = nil },
		func(value *msql.Request) { value.Statements[0].Authorization.Actor = "" },
		func(value *msql.Request) {
			value.Statements[0].Authorization.AuthorizedDatabases = []string{"work", " WORK "}
		},
	} {
		candidate := validProtocolRequest()
		mutate(&candidate)
		if err := candidate.Validate(); !errors.Is(err, msql.ErrInvalidRequest) {
			t.Fatalf("invalid Request error = %v", err)
		}
	}
}

func TestEnvelopePreservesWireNumbersAndRawJSON(t *testing.T) {
	t.Parallel()
	wire := []byte(`{"version":"memora.result/v1","request_id":"request-1","ok":true,"results":[{"index":0,"statement":"SHOW","source":"SHOW DATABASES LIMIT 8","status":"succeeded","columns":[],"rows":[{"schema_version":9007199254740993}],"affected_rows":0,"truncated":false,"warnings":[]}],"truncated":false,"warnings":[]}`)
	var envelope msql.Envelope
	if err := json.Unmarshal(wire, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatal(err)
	}
	if number, ok := envelope.Results[0].Rows[0]["schema_version"].(json.Number); !ok || number.String() != "9007199254740993" {
		t.Fatalf("wire number = %#v", envelope.Results[0].Rows[0]["schema_version"])
	}
	raw := envelope.RawJSON()
	if !bytes.Equal(raw, wire) {
		t.Fatalf("RawJSON() = %s", raw)
	}
	raw[0] = 'x'
	if bytes.Equal(raw, envelope.RawJSON()) {
		t.Fatal("RawJSON exposed mutable envelope storage")
	}
}

func TestProtocolProductionFilesDependOnlyOnStandardLibrary(t *testing.T) {
	t.Parallel()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime caller unavailable")
	}
	directory := filepath.Dir(current)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || strings.HasSuffix(entry.Name(), "_test.go") || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(strings.Split(path, "/")[0], ".") {
				t.Fatalf("protocol production file %s imports non-standard package %q", entry.Name(), path)
			}
		}
	}
}

func validProtocolRequest() msql.Request {
	return msql.Request{Version: msql.RequestVersion, Source: "SHOW DATABASES LIMIT 8",
		Statements: []msql.StatementInput{{Authorization: msql.Authorization{Version: msql.AuthorizationVersion,
			Actor: "agent:test", AuthorizedDatabases: []string{"work"}, DefaultLevel: msql.LevelRead},
			Mutation: msql.MutationOptions{RouteLeafIDs: []string{}, TargetRouteLeafIDs: [][]string{}}}}}
}
