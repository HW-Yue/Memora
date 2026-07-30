package daemon

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/ipc"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/semantichealth"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
)

func TestSemanticHealthHandlerReportsAndReplaysNoopMaintenance(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	dictionary := catalog.New(database, catalog.Options{})
	handler := newDatabaseHandler(ctx, dictionary, row.New(database, dictionary, row.Options{}), database)
	defer handler.Close()

	encoded, err := handler.Handle(ctx, ipc.Session{ID: "health"}, ipc.Request{
		Version: ipc.Version, RequestID: "health-report", Method: "semantic_health.report",
	})
	if err != nil {
		t.Fatal(err)
	}
	var report semantichealth.Report
	if err := json.Unmarshal(encoded, &report); err != nil || report.Status != "healthy" || report.Hash == "" {
		t.Fatalf("health report = %#v, %v", report, err)
	}
	request := semantichealth.Request{
		Version: semantichealth.RequestVersion, RequestID: "maintenance-noop",
		Trigger: semantichealth.TriggerUserRequest, Actor: "agent:test",
		ExpectedReportHash: report.Hash, IssueIDs: []string{},
	}
	payload, _ := json.Marshal(request)
	for index := 0; index < 2; index++ {
		encoded, err = handler.Handle(ctx, ipc.Session{ID: "health"}, ipc.Request{
			Version: ipc.Version, RequestID: "maintenance", Method: "semantic_health.maintain", Payload: payload,
		})
		if err != nil {
			t.Fatal(err)
		}
		var receipt semantichealth.Receipt
		if err := json.Unmarshal(encoded, &receipt); err != nil || receipt.Status != "noop" || receipt.Replayed != (index == 1) {
			t.Fatalf("maintenance receipt %d = %#v, %v", index, receipt, err)
		}
	}
}
