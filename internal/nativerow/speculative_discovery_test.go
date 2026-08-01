package nativerow

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routevector"
	"github.com/HW-Yue/Memora/internal/skilldiscovery"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestSpeculativeDiscoveryPlanRunsAgainstRealNativeMSQLWithoutFactEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	dictionary := nativecatalog.NewService(nativecatalog.New(file), nativecatalog.ServiceOptions{
		IDs: &testIDs{values: []string{"work", "notes", "title"}}, Clock: testClock{value: now},
	})
	baseRows := NewService(New(file), dictionary, ServiceOptions{
		IDs: &testIDs{values: []string{"root", "recovery"}}, Clock: testClock{value: now},
	})
	configuration, err := nativeconfig.New(file)
	if err != nil {
		t.Fatal(err)
	}
	rows := &speculativeConfigurationRows{Service: baseRows, configuration: configuration}
	engine := executor.New(dictionary, rows)
	for _, statement := range []string{
		"CREATE DATABASE work PURPOSE 'Work' SCOPE 'Engineering'",
		"CREATE TABLE work.notes PURPOSE 'Recovery notes' ROW SEMANTICS 'One procedure' (title TEXT(80) NOT NULL PURPOSE 'Title')",
		"CREATE ROUTE ROOT FOR TABLE work.notes PURPOSE 'Notes Router'",
		"CREATE ROUTE UNDER 'route_root' NAME 'recovery' KIND 'leaf' PURPOSE 'Crash recovery procedures'",
	} {
		options := executor.MutationOptions{}
		if len(statement) > 12 && statement[7:12] == "ROUTE" {
			options.MaxAffectedRows = 1
		}
		executeMSQL(t, ctx, engine, statement, executor.Parameters{}, options)
	}
	nodes, err := rows.ListRouterNodes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := routevector.New(t.TempDir(), rows, routevector.Options{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, _, err := vectors.Publish(ctx, vectorRequestForDatabase(t, nodes, "db_work", "gen_skill"))
	if err != nil {
		t.Fatal(err)
	}

	plan, err := skilldiscovery.Build(skilldiscovery.Request{
		TopicID: "topic:recovery", Actor: "agent:host", AuthorizedDatabases: []string{"work"},
		LexicalQuery: "crash recovery", Vector: &skilldiscovery.VectorQuery{
			Values: []float32{1, 0}, SpaceDigest: manifest.SpaceDigest,
		},
		PrefetchTables:       []skilldiscovery.TableRef{{Database: "work", Table: "notes"}},
		PrefetchFrameTopicID: "topic:recovery",
		CandidateLimit:       8, CandidateUTF8ByteLimit: 4096,
		AtlasEntryLimit: 64, AtlasUTF8ByteLimit: 4096, RootLimit: 12, ContextUTF8ByteLimit: 12000,
	})
	if err != nil {
		t.Fatal(err)
	}
	session := executor.NewBatchSessionWithPointReadsAndRouteVectors(ctx, dictionary, rows, nil, vectors)
	defer session.Close()
	responses := make([]result.Envelope, 0, len(plan.Calls))
	for _, call := range plan.Calls {
		inputs := []executor.StatementInput{call.Input}
		envelope := session.Execute(ctx, executor.BatchRequest{
			RequestID: call.ID, Source: call.MSQL, Statements: inputs,
		})
		if !envelope.OK {
			t.Fatalf("call %s = %#v", call.ID, envelope)
		}
		responses = append(responses, envelope)
	}
	frame, err := skilldiscovery.Finalize(plan, responses)
	if err != nil {
		t.Fatalf("Finalize() = %v, root = %#v", err, responses[len(responses)-1])
	}
	if frame.AnswerReady() || !frame.Reusable("topic:recovery") || len(frame.CatalogTables) != 1 ||
		len(frame.Predictors) != 2 || len(frame.Candidates) == 0 || len(frame.PrefetchedRoots) != 1 {
		t.Fatalf("native speculative Frame = %#v", frame)
	}
	selection, err := frame.SelectTable("topic:recovery", skilldiscovery.TableRef{Database: "work", Table: "notes"})
	if err != nil || !selection.UsedPrefetch || len(selection.Routes) != 1 {
		t.Fatalf("native root selection = %#v, %v", selection, err)
	}
	for _, route := range selection.Routes {
		if _, leaked := route["row_id"]; leaked {
			t.Fatalf("speculative root leaked fact locator: %#v", route)
		}
	}
}

type speculativeConfigurationRows struct {
	*Service
	configuration *nativeconfig.Service
}

func (rows *speculativeConfigurationRows) CurrentQueryBudgets(context.Context) (nativeconfig.Revision, error) {
	return rows.configuration.Current()
}

func (rows *speculativeConfigurationRows) QueryBudgetHistory(context.Context) ([]nativeconfig.Revision, error) {
	return rows.configuration.History()
}

func (rows *speculativeConfigurationRows) UpdateQueryBudgets(
	_ context.Context, budgets nativeconfig.QueryBudgets, expected uint64, actor, reason string,
) (nativeconfig.Revision, error) {
	return rows.configuration.Update(budgets, expected, actor, reason)
}

func (rows *speculativeConfigurationRows) RestoreQueryBudgets(
	_ context.Context, target, expected uint64, actor, reason string,
) (nativeconfig.Revision, error) {
	return rows.configuration.Restore(target, expected, actor, reason)
}
