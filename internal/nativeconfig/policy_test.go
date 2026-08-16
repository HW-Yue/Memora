package nativeconfig

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestRoutePolicyDefaultsToTwelveAndSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.CurrentPolicy()
	if err != nil || current.Revision != 1 || current.Policy != DefaultRoutePolicy() {
		t.Fatalf("initial = %#v, %v", current, err)
	}
	if current.Policy.BranchFanout != 12 {
		t.Fatalf("branch fan-out = %d, want 12", current.Policy.BranchFanout)
	}
	raised, err := service.UpdatePolicy(RoutePolicy{BranchFanout: 16}, 1, "agent:test", "domain needs wider fan-out")
	if err != nil || raised.Revision != 2 || raised.Policy.BranchFanout != 16 {
		t.Fatalf("update = %#v, %v", raised, err)
	}
	restored, err := service.RestorePolicy(1, 2, "agent:test", "undo experiment")
	if err != nil || restored.Revision != 3 || restored.RestoredRevision != 1 ||
		restored.Policy != DefaultRoutePolicy() {
		t.Fatalf("restore = %#v, %v", restored, err)
	}
	budgets, err := service.Current()
	if err != nil || budgets.Revision != 1 || budgets.Budgets != Defaults() {
		t.Fatalf("query budgets must keep their own revision chain: %#v, %v", budgets, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := nativestore.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	service, err = New(reopened)
	if err != nil {
		t.Fatal(err)
	}
	current, err = service.CurrentPolicy()
	if err != nil || current.Revision != 3 || current.Policy.BranchFanout != 12 {
		t.Fatalf("reopened = %#v, %v", current, err)
	}
	history, err := service.PolicyHistory()
	if err != nil || len(history) != 3 {
		t.Fatalf("history = %d entries, %v", len(history), err)
	}
}

func TestRoutePolicyIsMaterializedForDatabasesCreatedBeforeIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	legacy := &Service{file: file}
	initial := Revision{
		Version: Version, Key: QueryBudgetsKey, Revision: 1, Budgets: Defaults(),
		Actor: "engine:bootstrap", Reason: "materialize database query budget defaults",
	}
	if err := legacy.put(initial); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.CurrentPolicy(); !errors.Is(err, nativestore.ErrNotFound) {
		t.Fatalf("legacy database must not carry a Route policy yet: %v", err)
	}
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.CurrentPolicy()
	if err != nil || current.Revision != 1 || current.Policy.BranchFanout != 12 {
		t.Fatalf("materialized = %#v, %v", current, err)
	}
	budgets, err := service.Current()
	if err != nil || budgets.Revision != 1 {
		t.Fatalf("existing query budgets must be left alone: %#v, %v", budgets, err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRoutePolicyRejectsUnusableFanoutAndUnauthenticatedMutation(t *testing.T) {
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, fanout := range []int{0, 1, 101} {
		if _, err := service.UpdatePolicy(RoutePolicy{BranchFanout: fanout}, 1, "agent:test", "try"); !stableCode(err, result.CodeConstraint) {
			t.Fatalf("fan-out %d = %v, want constraint violation", fanout, err)
		}
	}
	if _, err := service.UpdatePolicy(RoutePolicy{BranchFanout: 16}, 1, "", "try"); !stableCode(err, result.CodeValidation) {
		t.Fatalf("missing actor = %v, want validation error", err)
	}
	if _, err := service.UpdatePolicy(RoutePolicy{BranchFanout: 16}, 7, "agent:test", "try"); !stableCode(err, result.CodeRevisionConflict) {
		t.Fatalf("stale expected revision = %v, want revision conflict", err)
	}
}

func stableCode(err error, code result.Code) bool {
	var stable interface{ StableCode() string }
	return errors.As(err, &stable) && stable.StableCode() == string(code)
}
