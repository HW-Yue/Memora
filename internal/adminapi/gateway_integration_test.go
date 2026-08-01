package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/daemon"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func TestGatewayRealDaemonJourneyMatchesScopedMSQLContract(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "instance")
	daemonContext, stopDaemon := context.WithCancel(context.Background())
	ready := make(chan daemon.State, 1)
	daemonDone := make(chan error, 1)
	go func() { daemonDone <- daemon.Run(daemonContext, dataDir, ready) }()
	select {
	case state := <-ready:
		if !state.Running {
			t.Fatalf("daemon state = %#v", state)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("daemon did not become ready")
	}
	t.Cleanup(func() {
		stopDaemon()
		if err := <-daemonDone; err != nil {
			t.Errorf("daemon shutdown = %v", err)
		}
	})

	created, err := daemon.Execute(context.Background(), dataDir,
		"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; "+
			"CREATE DATABASE secret PURPOSE 'Secret' SCOPE 'Private'; "+
			"CREATE TABLE work.notes PURPOSE 'Reviewed notes' ROW SEMANTICS 'One note' "+
			"(title TEXT NOT NULL PURPOSE 'Title' ROLE title, body TEXT PURPOSE 'Body' ROLE summary)", nil)
	if err != nil || !created.OK {
		t.Fatalf("create databases = %#v, %v", created, err)
	}
	databaseID, _ := created.Results[0].Rows[0]["database_id"].(string)
	tableID, _ := created.Results[2].Rows[0]["table_id"].(string)
	if databaseID == "" || tableID == "" {
		t.Fatalf("created stable IDs = %q, %q", databaseID, tableID)
	}
	rootRoute, err := daemon.Execute(context.Background(), dataDir,
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{MaxAffectedRows: 1}}})
	if err != nil || !rootRoute.OK {
		t.Fatalf("create Route root = %#v, %v", rootRoute, err)
	}
	rootRouteID, _ := rootRoute.Results[0].Rows[0]["route_id"].(string)
	branchRoute, err := daemon.Execute(context.Background(), dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose SYNOPSIS :synopsis",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": rootRouteID, "name": "architecture", "kind": "branch",
				"purpose": "Architecture knowledge", "synopsis": "Reviewed architecture decisions",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}})
	if err != nil || !branchRoute.OK {
		t.Fatalf("create Route branch = %#v, %v", branchRoute, err)
	}
	branchRouteID, _ := branchRoute.Results[0].Rows[0]["route_id"].(string)
	leafRoute, err := daemon.Execute(context.Background(), dataDir,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		[]executor.StatementInput{{
			Parameters: executor.Parameters{Named: map[string]any{
				"parent": branchRouteID, "name": "storage", "kind": "leaf", "purpose": "Storage decisions",
			}},
			Mutation: executor.MutationOptions{MaxAffectedRows: 1},
		}})
	if err != nil || !leafRoute.OK {
		t.Fatalf("create Route leaf = %#v, %v", leafRoute, err)
	}
	leafRouteID, _ := leafRoute.Results[0].Rows[0]["route_id"].(string)
	inserted, err := daemon.Execute(context.Background(), dataDir,
		"INSERT INTO work.notes (title, body) VALUES ('manifest', 'private body')",
		[]executor.StatementInput{{Mutation: executor.MutationOptions{
			ExpectedSchemaVersion: 1, MaxAffectedRows: 1, RouteLeafIDs: []string{leafRouteID},
		}}})
	if err != nil || !inserted.OK {
		t.Fatalf("insert routed Row = %#v, %v", inserted, err)
	}
	rowID, _ := inserted.Results[0].Rows[0]["row_id"].(string)
	if rootRouteID == "" || branchRouteID == "" || leafRouteID == "" || rowID == "" {
		t.Fatalf("Route journey IDs = %q, %q, %q, %q", rootRouteID, branchRouteID, leafRouteID, rowID)
	}
	gateway, err := Start(context.Background(), Config{
		DataDir:    dataDir,
		Scopes:     []string{"work"},
		Execute:    daemon.Execute,
		Random:     bytes.NewReader(bytes.Repeat([]byte{0x71}, 96)),
		SessionTTL: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gateway.Close() })
	receipt, cookie := bootstrap(t, gateway.Descriptor())

	source := "SHOW DATABASES LIMIT 16 COMPACT"
	response := callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken,
		`{"source":"SHOW DATABASES LIMIT 16 COMPACT"}`)
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("gateway status = %d, body = %s", response.StatusCode, readBody(t, response))
	}
	var fromAPI result.Envelope
	if err := json.NewDecoder(response.Body).Decode(&fromAPI); err != nil {
		t.Fatal(err)
	}
	if !fromAPI.OK || len(fromAPI.Results) != 1 || len(fromAPI.Results[0].Rows) != 1 ||
		fromAPI.Results[0].Rows[0]["name"] != "work" {
		t.Fatalf("scoped API envelope = %#v", fromAPI)
	}
	authorization := security.Authorization{
		Version:             security.AuthorizationVersion,
		Actor:               "user:admin",
		AuthorizedDatabases: []string{"work"},
	}
	fromDaemon, err := daemon.Execute(context.Background(), dataDir, source,
		[]executor.StatementInput{{Authorization: authorization}})
	if err != nil {
		t.Fatal(err)
	}
	fromAPI.RequestID = ""
	fromDaemon.RequestID = ""
	if !reflect.DeepEqual(fromAPI, fromDaemon) {
		t.Fatalf("API envelope differs from daemon envelope\nAPI: %#v\ndaemon: %#v", fromAPI, fromDaemon)
	}

	for _, journey := range []struct {
		source      string
		resultCount int
		childName   string
	}{
		{
			source: fmt.Sprintf(
				`DESCRIBE DATABASE %q COMPACT; SHOW TABLES FROM %q LIMIT 32 COMPACT`,
				databaseID, databaseID,
			),
			resultCount: 2, childName: "notes",
		},
		{
			source: fmt.Sprintf(
				`DESCRIBE TABLE %q.%q COMPACT; SHOW COLUMNS FROM %q.%q LIMIT 32 COMPACT`,
				databaseID, tableID, databaseID, tableID,
			),
			resultCount: 2, childName: "body",
		},
	} {
		payload, err := json.Marshal(map[string]any{"source": journey.source})
		if err != nil {
			t.Fatal(err)
		}
		response = callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken, string(payload))
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			t.Fatalf("Catalog journey status = %d", response.StatusCode)
		}
		var catalogEnvelope result.Envelope
		if err := json.NewDecoder(response.Body).Decode(&catalogEnvelope); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if !catalogEnvelope.OK || len(catalogEnvelope.Results) != journey.resultCount ||
			catalogEnvelope.Results[1].Page == nil || catalogEnvelope.Results[1].Page.Limit != 32 ||
			len(catalogEnvelope.Results[1].Rows) == 0 || catalogEnvelope.Results[1].Rows[0]["name"] != journey.childName {
			t.Fatalf("Catalog journey %q = %#v", journey.source, catalogEnvelope)
		}
	}

	for _, journey := range []struct {
		source     string
		statements []map[string]any
		resultRows int
		pageLimit  uint64
		assert     func(t *testing.T, statement result.StatementResult)
	}{
		{
			source:     fmt.Sprintf("SHOW ROUTES FROM TABLE %q.%q AT ROOT LIMIT 12", databaseID, tableID),
			resultRows: 1,
			pageLimit:  12,
			assert: func(t *testing.T, statement result.StatementResult) {
				if statement.Rows[0]["route_id"] != branchRouteID || statement.Rows[0]["kind"] != "branch" {
					t.Fatalf("Route root rows = %#v", statement.Rows)
				}
			},
		},
		{
			source:     "DESCRIBE ROUTE :route",
			statements: []map[string]any{{"parameters": executor.Parameters{Named: map[string]any{"route": branchRouteID}}}},
			resultRows: 1,
			assert: func(t *testing.T, statement result.StatementResult) {
				row := statement.Rows[0]
				if row["database_id"] != databaseID || row["table_id"] != tableID ||
					row["synopsis"] != "Reviewed architecture decisions" {
					t.Fatalf("Route point scope = %#v", row)
				}
			},
		},
		{
			source:     "SHOW ROUTES UNDER :route LIMIT 12",
			statements: []map[string]any{{"parameters": executor.Parameters{Named: map[string]any{"route": branchRouteID}}}},
			resultRows: 1,
			pageLimit:  12,
			assert: func(t *testing.T, statement result.StatementResult) {
				if statement.Rows[0]["route_id"] != leafRouteID || statement.Rows[0]["kind"] != "leaf" {
					t.Fatalf("Route child rows = %#v", statement.Rows)
				}
			},
		},
		{
			source:     "OPEN ROUTE :route LIMIT 20",
			statements: []map[string]any{{"parameters": executor.Parameters{Named: map[string]any{"route": leafRouteID}}}},
			resultRows: 1,
			pageLimit:  20,
			assert: func(t *testing.T, statement result.StatementResult) {
				row := statement.Rows[0]
				if len(row) != 4 || row["database_id"] != databaseID || row["table_id"] != tableID ||
					row["row_id"] != rowID || row["body"] != nil {
					t.Fatalf("Route locator frame = %#v", row)
				}
			},
		},
	} {
		payload, err := json.Marshal(map[string]any{"source": journey.source, "statements": journey.statements})
		if err != nil {
			t.Fatal(err)
		}
		response = callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken, string(payload))
		var routeEnvelope result.Envelope
		if err := json.NewDecoder(response.Body).Decode(&routeEnvelope); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK || !routeEnvelope.OK || len(routeEnvelope.Results) != 1 ||
			len(routeEnvelope.Results[0].Rows) != journey.resultRows ||
			(journey.pageLimit > 0 && (routeEnvelope.Results[0].Page == nil ||
				routeEnvelope.Results[0].Page.Limit != journey.pageLimit)) {
			t.Fatalf("Route journey %q = status %d, %#v", journey.source, response.StatusCode, routeEnvelope)
		}
		journey.assert(t, routeEnvelope.Results[0])
	}

	response = callMSQL(t, gateway.Descriptor(), cookie, receipt.CSRFToken,
		`{"source":"SHOW TABLES FROM secret LIMIT 16 COMPACT"}`)
	defer response.Body.Close()
	if err := json.NewDecoder(response.Body).Decode(&fromAPI); err != nil {
		t.Fatal(err)
	}
	if fromAPI.OK || len(fromAPI.Results) != 1 || fromAPI.Results[0].Error == nil ||
		fromAPI.Results[0].Error.Code != result.CodePermissionDenied {
		t.Fatalf("outside-scope envelope = %#v", fromAPI)
	}
}
