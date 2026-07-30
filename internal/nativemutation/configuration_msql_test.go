package nativemutation

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestDatabaseConfigurationMSQLIsDiscoverableVersionedAndRestorable(t *testing.T) {
	t.Parallel()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{})
	rowRepository := nativerow.New(file)
	routeRepository := nativerouter.New(file)
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	rows := NewService(
		nativerow.NewService(rowRepository, dictionary, nativerow.ServiceOptions{}),
		dictionary, rowRepository, routeRepository, New(file, rowRepository, routeRepository),
		configuration,
	)
	session := executor.NewBatchSession(context.Background(), dictionary, rows)
	t.Cleanup(func() { _ = session.Close() })
	response := session.Execute(context.Background(), executor.BatchRequest{
		RequestID: "database-configuration",
		Source: "SHOW CONFIGURATION; " +
			"ALTER CONFIGURATION QUERY_BUDGETS SET ROUTE_CHILDREN 2, OPEN_LOCATORS 3, " +
			"SELECT_SCAN 4, SELECT_ROWS 1, ROUTE_FRAME_NODES 2; " +
			"SHOW CONFIGURATION; " +
			"RESTORE CONFIGURATION QUERY_BUDGETS TO REVISION 1; SHOW CONFIGURATION HISTORY LIMIT 10",
		Statements: []executor.StatementInput{
			{},
			{Mutation: executor.MutationOptions{
				ExpectedRevision: 1, Actor: "agent:test", Reason: "exercise tight budget",
			}},
			{},
			{Mutation: executor.MutationOptions{
				ExpectedRevision: 2, Actor: "agent:test", Reason: "undo tight budget",
			}},
			{},
		},
	})
	if !response.OK || len(response.Results) != 5 {
		t.Fatalf("configuration response = %#v", response)
	}
	for _, statement := range response.Results {
		if statement.Status != result.StatusSucceeded {
			t.Fatalf("configuration statement = %#v", statement)
		}
	}
	if response.Results[0].Rows[0]["revision"] != uint64(1) ||
		response.Results[2].Rows[0]["select_rows"] != 1 ||
		response.Results[4].Rows[0]["revision"] != uint64(3) ||
		response.Results[4].Rows[0]["select_rows"] != 10 ||
		response.Results[4].Rows[0]["restored_revision"] != uint64(1) {
		t.Fatalf("configuration revisions = %#v", response.Results)
	}
	if len(response.Results[4].Rows) != 3 ||
		response.Results[4].Rows[1]["revision"] != uint64(2) ||
		response.Results[4].Rows[2]["revision"] != uint64(1) {
		t.Fatalf("configuration history = %#v", response.Results[4].Rows)
	}
}
