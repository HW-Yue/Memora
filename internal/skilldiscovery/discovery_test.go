package skilldiscovery_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/discovery"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/skilldiscovery"
)

func TestBuildPlansOneTurnWithGlobalPredictorAndPrefetchBudgets(t *testing.T) {
	t.Parallel()
	request := specimenRequest()
	originalVector := append([]float32(nil), request.Vector.Values...)
	originalDatabases := append([]string(nil), request.AuthorizedDatabases...)
	plan, err := skilldiscovery.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Version != skilldiscovery.Version || plan.TopicID != request.TopicID ||
		len(plan.Calls) != 6 || plan.Budget.MaxToolCalls != 10 || plan.Budget.CandidateLimit != 8 ||
		plan.Budget.CandidateUTF8ByteLimit != 4096 || plan.Budget.PrefetchTableLimit != 2 {
		t.Fatalf("plan = %#v", plan)
	}
	wantKinds := []skilldiscovery.CallKind{
		skilldiscovery.CallCatalog, skilldiscovery.CallTables, skilldiscovery.CallTables,
		skilldiscovery.CallLexical, skilldiscovery.CallVector, skilldiscovery.CallRootPrefetch,
	}
	for index, call := range plan.Calls {
		if call.Kind != wantKinds[index] {
			t.Fatalf("call %d = %#v", index, call)
		}
		if call.Kind != skilldiscovery.CallCatalog {
			authorization := call.Input.Authorization
			if authorization.Version != security.AuthorizationVersion || authorization.Actor != "agent:host" ||
				!reflect.DeepEqual(authorization.AuthorizedDatabases, originalDatabases) {
				t.Fatalf("call authorization = %#v", authorization)
			}
		}
	}
	lexical, vector := plan.Calls[3], plan.Calls[4]
	if lexical.Input.Parameters.Named["lexical_limit"] != int64(4) || lexical.Input.Parameters.Named["lexical_bytes"] != int64(2048) ||
		vector.Input.Parameters.Named["vector_limit"] != int64(4) || vector.Input.Parameters.Named["vector_bytes"] != int64(2048) {
		t.Fatalf("predictor allocation = %#v, %#v", lexical.Input, vector.Input)
	}
	if !strings.Contains(lexical.MSQL, "USING LEXICAL :lexical_query") ||
		!strings.Contains(vector.MSQL, "USING VECTOR :query_vector SPACE :space_digest") {
		t.Fatalf("predictor calls = %#v, %#v", lexical, vector)
	}
	request.Vector.Values[0] = 0
	request.AuthorizedDatabases[0] = "changed"
	if !reflect.DeepEqual(plan.Calls[4].Input.Parameters.Named["query_vector"], originalVector) ||
		!reflect.DeepEqual(plan.Calls[1].Input.Authorization.AuthorizedDatabases, originalDatabases) {
		t.Fatal("plan shares request slices")
	}
}

func TestFinalizeAuditsSnapshotsAndNeverTurnsNavigationIntoEvidence(t *testing.T) {
	t.Parallel()
	request := specimenRequest()
	plan, err := skilldiscovery.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	responses := specimenResponses(t, plan)
	frame, err := skilldiscovery.Finalize(plan, responses)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Version != skilldiscovery.Version || frame.AnswerReady() || !frame.Reusable(request.TopicID) ||
		frame.Audit.ToolCalls != len(plan.Calls) || frame.Audit.OutputUTF8Bytes == 0 ||
		frame.Audit.CandidatesUsed != 2 || frame.Audit.CandidateUTF8BytesUsed == 0 ||
		len(frame.CatalogPageSnapshots) != 3 || len(frame.Predictors) != 2 ||
		len(frame.Candidates) != 2 || len(frame.PrefetchedRoots) != 1 {
		t.Fatalf("frame = %#v", frame)
	}
	if frame.Predictors[0].Snapshot == frame.Predictors[1].Snapshot ||
		frame.Predictors[0].CatalogRevision != frame.Predictors[1].CatalogRevision {
		t.Fatalf("predictor snapshots = %#v", frame.Predictors)
	}
	for _, candidate := range frame.Candidates {
		if candidate.RouteID == "" || candidate.Predictor == "" {
			t.Fatalf("candidate = %#v", candidate)
		}
	}

	prefetched, err := frame.SelectTable(request.TopicID, skilldiscovery.TableRef{Database: "work", Table: "notes"})
	if err != nil || !prefetched.UsedPrefetch || prefetched.Fallback != nil || len(prefetched.Routes) != 1 {
		t.Fatalf("prefetched selection = %#v, %v", prefetched, err)
	}
	// This Table has zero predictor hits but remains selectable and receives
	// the ordinary Router root fallback.
	fallback, err := frame.SelectTable(request.TopicID, skilldiscovery.TableRef{Database: "work", Table: "other"})
	if err != nil || fallback.UsedPrefetch || fallback.Fallback == nil ||
		!strings.Contains(fallback.Fallback.MSQL, "SHOW ROUTES FROM TABLE `work`.`other` AT ROOT") {
		t.Fatalf("zero-hit fallback = %#v, %v", fallback, err)
	}
	stale, err := frame.SelectTable("topic:new", skilldiscovery.TableRef{Database: "work", Table: "notes"})
	if err != nil || !stale.DiscardedStaleFrame || stale.UsedPrefetch || stale.Fallback == nil {
		t.Fatalf("stale topic selection = %#v, %v", stale, err)
	}
	overBudget := plan
	overBudget.Budget.ContextUTF8ByteLimit = 256
	discarded, err := skilldiscovery.Finalize(overBudget, responses)
	if err != nil || discarded.Reusable(request.TopicID) || !discarded.Truncated ||
		len(discarded.Candidates) != 0 || len(discarded.PrefetchedRoots) != 0 || discarded.Audit.OutputUTF8Bytes <= 256 {
		t.Fatalf("over-budget frame = %#v, %v", discarded, err)
	}
}

func TestFinalizeRejectsCatalogDriftAndBuildRejectsUnsafeSpeculation(t *testing.T) {
	t.Parallel()
	request := specimenRequest()
	plan, err := skilldiscovery.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	responses := specimenResponses(t, plan)
	responses[4].Results[0].Discovery.CatalogRevision = digest('c')
	if _, err := skilldiscovery.Finalize(plan, responses); err == nil {
		t.Fatal("Finalize accepted predictor Catalog drift")
	}
	for _, mutate := range []func(*skilldiscovery.Request){
		func(value *skilldiscovery.Request) { value.TopicID = "" },
		func(value *skilldiscovery.Request) { value.AuthorizedDatabases = []string{"a", "b", "c", "d", "e"} },
		func(value *skilldiscovery.Request) {
			value.PrefetchTables = append(value.PrefetchTables, skilldiscovery.TableRef{Database: "work", Table: "third"}, skilldiscovery.TableRef{Database: "work", Table: "fourth"})
		},
		func(value *skilldiscovery.Request) { value.PrefetchFrameTopicID = "topic:old" },
		func(value *skilldiscovery.Request) { value.Vector.SpaceDigest = "" },
		func(value *skilldiscovery.Request) { value.CandidateLimit = 25 },
	} {
		invalid := specimenRequest()
		mutate(&invalid)
		if _, err := skilldiscovery.Build(invalid); err == nil {
			t.Fatalf("Build(%#v) succeeded", invalid)
		}
	}
}

func specimenRequest() skilldiscovery.Request {
	return skilldiscovery.Request{
		TopicID: "topic:recovery", Actor: "agent:host",
		AuthorizedDatabases: []string{"work", "private"}, LexicalQuery: "crash recovery",
		Vector: &skilldiscovery.VectorQuery{
			Values:      []float32{1, 0},
			SpaceDigest: digest('s'),
		},
		PrefetchTables:       []skilldiscovery.TableRef{{Database: "work", Table: "notes"}},
		PrefetchFrameTopicID: "topic:recovery",
		CandidateLimit:       8, CandidateUTF8ByteLimit: 4096,
		TableLimit: 16, RootLimit: 12, ContextUTF8ByteLimit: 12000,
	}
}

func specimenResponses(t *testing.T, plan skilldiscovery.Plan) []result.Envelope {
	t.Helper()
	configuration := result.NewStatement(0, "SHOW", "SHOW CONFIGURATION")
	configuration.Rows = []result.Row{{"route_children": 12, "route_frame_nodes": 12}}
	databases := result.NewStatement(1, "SHOW", "SHOW DATABASES")
	databases.Rows = []result.Row{
		{"database_id": "db_work", "name": "work"},
		{"database_id": "db_private", "name": "private"},
	}
	databases.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 32, Snapshot: digest('d')}
	work := statement("SHOW_TABLES", result.Row{"database_id": "db_work", "table_id": "tbl_notes", "name": "notes"},
		result.Row{"database_id": "db_work", "table_id": "tbl_other", "name": "other"})
	private := statement("SHOW_TABLES", result.Row{"database_id": "db_private", "table_id": "tbl_secrets", "name": "secrets"})
	work.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 16, Snapshot: digest('w')}
	private.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 16, Snapshot: digest('p')}
	lexical := candidateEnvelope(t, "lexical-route/v1", discovery.ScoreMatchCount, digest('l'), digest('a'), "route_recovery", 2)
	vector := candidateEnvelope(t, "vector-route-exact/v1", discovery.ScoreDotProduct, digest('v'), digest('a'), "route_vector", 0.9)
	lexical.RequestID = plan.Calls[3].ID
	vector.RequestID = plan.Calls[4].ID
	root := statement("SHOW_ROUTES", result.Row{
		"database_id": "db_work", "table_id": "tbl_notes", "route_id": "route_root",
		"revision": uint64(1), "kind": "root", "purpose": "Notes root",
	})
	root.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 12, Snapshot: digest('r')}
	return []result.Envelope{
		result.NewEnvelope(plan.Calls[0].ID, configuration, databases),
		result.NewEnvelope(plan.Calls[1].ID, work), result.NewEnvelope(plan.Calls[2].ID, private),
		lexical, vector, result.NewEnvelope(plan.Calls[5].ID, root),
	}
}

func candidateEnvelope(
	t *testing.T, predictor string, kind discovery.ScoreKind,
	snapshot, catalogRevision, routeID string, score float64,
) result.Envelope {
	t.Helper()
	builder, err := discovery.NewBuilder(snapshot, catalogRevision, discovery.Budget{
		CandidateLimit: 4, UTF8ByteLimit: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := builder.Add(discovery.Batch{
		Snapshot: snapshot, CatalogRevision: catalogRevision,
		Predictor: predictor, Status: discovery.PredictorSucceeded, ScoreKind: kind,
		Reason: "bounded navigation predictor",
		Candidates: []discovery.CandidateInput{{
			DatabaseID: "db_work", TableID: "tbl_notes", RouteID: routeID,
			RouteRevision: 1, Score: &score, Reason: "navigation only",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	statement := result.NewStatement(0, "SHOW", "SHOW ROUTE CANDIDATES")
	frame := builder.Frame()
	statement.Discovery = &frame
	return result.NewEnvelope(predictor, statement)
}

func statement(kind string, rows ...result.Row) result.StatementResult {
	value := result.NewStatement(0, kind, kind)
	value.Rows = rows
	return value
}

func digest(character byte) string {
	const hexadecimal = "0123456789abcdef"
	return "sha256:" + strings.Repeat(string(hexadecimal[int(character)%len(hexadecimal)]), 64)
}
