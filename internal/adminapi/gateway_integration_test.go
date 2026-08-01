package adminapi

import (
	"bytes"
	"context"
	"encoding/json"
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
		"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Projects'; CREATE DATABASE secret PURPOSE 'Secret' SCOPE 'Private'", nil)
	if err != nil || !created.OK {
		t.Fatalf("create databases = %#v, %v", created, err)
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
