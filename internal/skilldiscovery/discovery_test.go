package skilldiscovery_test

import (
	"fmt"
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
		len(plan.Calls) != 4 || plan.Budget.MaxToolCalls != 10 || plan.Budget.CandidateLimit != 8 ||
		plan.Budget.CandidateUTF8ByteLimit != 4096 || plan.Budget.PrefetchTableLimit != 2 ||
		plan.Budget.AtlasEntryLimit != 64 || plan.Budget.AtlasUTF8ByteLimit != 4096 {
		t.Fatalf("plan = %#v", plan)
	}
	wantKinds := []skilldiscovery.CallKind{
		skilldiscovery.CallCatalog, skilldiscovery.CallLexical,
		skilldiscovery.CallVector, skilldiscovery.CallRootPrefetch,
	}
	for index, call := range plan.Calls {
		if call.Kind != wantKinds[index] {
			t.Fatalf("call %d = %#v", index, call)
		}
		authorization := call.Input.Authorization
		if authorization.Version != security.AuthorizationVersion || authorization.Actor != "agent:host" ||
			!reflect.DeepEqual(authorization.AuthorizedDatabases, originalDatabases) {
			t.Fatalf("call authorization = %#v", authorization)
		}
	}
	lexical, vector := plan.Calls[1], plan.Calls[2]
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
	if !reflect.DeepEqual(plan.Calls[2].Input.Parameters.Named["query_vector"], originalVector) ||
		!reflect.DeepEqual(plan.Calls[0].Input.Authorization.AuthorizedDatabases, originalDatabases) {
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
		len(frame.CatalogPageSnapshots) != 1 || !frame.CatalogCoverage.Complete ||
		frame.CatalogCoverage.EntriesSeen != 5 || len(frame.Predictors) != 2 ||
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
	responses[2].Results[0].Discovery.CatalogRevision = digest('c')
	if _, err := skilldiscovery.Finalize(plan, responses); err == nil {
		t.Fatal("Finalize accepted predictor Catalog drift")
	}
	for _, mutate := range []func(*skilldiscovery.Request){
		func(value *skilldiscovery.Request) { value.TopicID = "" },
		func(value *skilldiscovery.Request) {
			value.AuthorizedDatabases = make([]string, 33)
			for index := range value.AuthorizedDatabases {
				value.AuthorizedDatabases[index] = fmt.Sprintf("database-%d", index)
			}
		},
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

func TestCatalogCoverageContinuesWithoutModelChoiceUntilColdTableIsVisible(t *testing.T) {
	t.Parallel()
	request := specimenRequest()
	request.AtlasEntryLimit = 3
	plan, err := skilldiscovery.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	responses := specimenResponses(t, plan)
	all := append([]result.Row{}, responses[0].Results[0].Rows...)
	responses[0].Results[0].Rows = all[:3]
	responses[0].Results[0].Truncated = true
	responses[0].Results[0].NextCursor = "cursor-page-2"
	responses[0].Results[0].Page = &result.ListPage{Version: result.ListPageVersion, Limit: 3,
		Snapshot: digest('d'), Truncated: true, NextCursor: "cursor-page-2"}
	responses[0] = result.NewEnvelope(plan.Calls[0].ID, responses[0].Results...)
	responses[0].Truncated = true
	responses[0].NextCursor = "cursor-page-2"
	frame, err := skilldiscovery.Finalize(plan, responses)
	if err != nil || frame.CatalogCoverage.Complete || frame.CatalogCoverage.Pages != 1 ||
		frame.CatalogCoverage.NextCursor != "cursor-page-2" {
		t.Fatalf("partial Frame = %#v, %v", frame, err)
	}
	// A predictor may name a Table outside the current Atlas page; partial
	// coverage cannot turn absence from the first page into exclusion.
	selection, err := frame.SelectTable(request.TopicID, skilldiscovery.TableRef{Database: "private", Table: "secrets"})
	if err != nil || selection.Fallback == nil {
		t.Fatalf("partial cold selection = %#v, %v", selection, err)
	}
	next, available, err := frame.NextCatalogPage(request.TopicID)
	if err != nil || !available || next.Input.Parameters.Named["atlas_cursor"] != "cursor-page-2" {
		t.Fatalf("NextCatalogPage() = %#v, %t, %v", next, available, err)
	}
	atlas := result.NewStatement(0, "SHOW", "SHOW CATALOG ATLAS")
	atlas.Rows = all[3:]
	atlas.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 3, Cursor: "cursor-page-2", Snapshot: digest('d')}
	continued, err := frame.ContinueCatalog(request.TopicID, next, result.NewEnvelope(next.ID, atlas))
	if err != nil || !continued.CatalogCoverage.Complete || continued.CatalogCoverage.Pages != 2 ||
		continued.CatalogCoverage.EntriesSeen != 5 {
		t.Fatalf("complete Frame = %#v, %v", continued, err)
	}
	if _, available, err := continued.NextCatalogPage(request.TopicID); err != nil || available {
		t.Fatalf("completed NextCatalogPage() available=%t err=%v", available, err)
	}
}

func TestBuildSupportsThirtyTwoAuthorizedDatabasesWithoutPerDatabaseCalls(t *testing.T) {
	t.Parallel()
	request := specimenRequest()
	request.AuthorizedDatabases = make([]string, 32)
	for index := range request.AuthorizedDatabases {
		request.AuthorizedDatabases[index] = fmt.Sprintf("database-%02d", index)
	}
	request.Vector, request.PrefetchTables, request.PrefetchFrameTopicID = nil, nil, ""
	plan, err := skilldiscovery.Build(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Calls) != 2 || plan.Calls[0].Kind != skilldiscovery.CallCatalog ||
		plan.Calls[1].Kind != skilldiscovery.CallLexical {
		t.Fatalf("32-Database plan = %#v", plan)
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
		AtlasEntryLimit: 64, AtlasUTF8ByteLimit: 4096, RootLimit: 12, ContextUTF8ByteLimit: 12000,
	}
}

func specimenResponses(t *testing.T, plan skilldiscovery.Plan) []result.Envelope {
	t.Helper()
	atlas := result.NewStatement(0, "SHOW", "SHOW CATALOG ATLAS")
	atlas.Rows = []result.Row{
		{"kind": "database", "database_id": "db_work", "database": "work"},
		{"kind": "table", "database_id": "db_work", "database": "work", "table_id": "tbl_notes", "table": "notes"},
		{"kind": "table", "database_id": "db_work", "database": "work", "table_id": "tbl_other", "table": "other"},
		{"kind": "database", "database_id": "db_private", "database": "private"},
		{"kind": "table", "database_id": "db_private", "database": "private", "table_id": "tbl_secrets", "table": "secrets"},
	}
	atlas.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 64, Snapshot: digest('d')}
	lexical := candidateEnvelope(t, "lexical-route/v1", discovery.ScoreMatchCount, digest('l'), digest('a'), "route_recovery", 2)
	vector := candidateEnvelope(t, "vector-route-exact/v1", discovery.ScoreDotProduct, digest('v'), digest('a'), "route_vector", 0.9)
	lexical.RequestID = plan.Calls[1].ID
	vector.RequestID = plan.Calls[2].ID
	root := statement("SHOW_ROUTES", result.Row{
		"database_id": "db_work", "table_id": "tbl_notes", "route_id": "route_root",
		"revision": uint64(1), "kind": "root", "purpose": "Notes root",
	})
	root.Page = &result.ListPage{Version: result.ListPageVersion, Limit: 12, Snapshot: digest('r')}
	return []result.Envelope{
		result.NewEnvelope(plan.Calls[0].ID, atlas),
		lexical, vector, result.NewEnvelope(plan.Calls[3].ID, root),
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
