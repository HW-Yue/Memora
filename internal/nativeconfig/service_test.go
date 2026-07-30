package nativeconfig

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

func TestQueryBudgetsAreVersionedAndRestoredCompensatingly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	current, err := service.Current()
	if err != nil || current.Revision != 1 || current.Budgets != Defaults() {
		t.Fatalf("initial = %#v, %v", current, err)
	}
	changed := current.Budgets
	changed.SelectRows = 3
	second, err := service.Update(changed, 1, "agent:test", "exercise smaller output")
	if err != nil || second.Revision != 2 {
		t.Fatalf("update = %#v, %v", second, err)
	}
	restored, err := service.Restore(1, 2, "agent:test", "undo experiment")
	if err != nil || restored.Revision != 3 || restored.RestoredRevision != 1 || restored.Budgets != Defaults() {
		t.Fatalf("restore = %#v, %v", restored, err)
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
	current, err = service.Current()
	if err != nil || current.Revision != 3 {
		t.Fatalf("reopened = %#v, %v", current, err)
	}
}

func TestQueryBudgetMutationRequiresRevisionActorAndReason(t *testing.T) {
	file, err := nativestore.Create(filepath.Join(t.TempDir(), "database.memora"), nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	service, err := New(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		expected uint64
		actor    string
		reason   string
		code     result.Code
	}{
		{0, "agent:test", "reason", result.CodeValidation},
		{1, "", "reason", result.CodeValidation},
		{1, "agent:test", "", result.CodeValidation},
		{2, "agent:test", "reason", result.CodeRevisionConflict},
	} {
		_, err := service.Update(Defaults(), test.expected, test.actor, test.reason)
		var stable interface{ StableCode() string }
		if !errors.As(err, &stable) || stable.StableCode() != string(test.code) {
			t.Fatalf("error = %v, want %s", err, test.code)
		}
	}
}
