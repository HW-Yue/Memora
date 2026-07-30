package security_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestAuditStoresOnlyBoundedMetadataAndDoctorRejectsCorruption(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	service := security.New(database, security.Options{
		Clock: testkit.NewFakeClock(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)),
		IDs:   testkit.NewFakeIDs("audit-1", "audit-2"),
	})
	secret := "sk-do-not-store-this"
	if err := service.Record(ctx, security.AuditInput{
		RequestID: "request-1", Method: "msql.execute", Actor: "agent:host",
		AuthorizedDatabases: []string{"work"},
		PayloadSHA256:       security.HashPayload([]byte(secret)),
		Status:              security.AuditSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(ctx)
	if err != nil || len(events) != 1 || events[0].PayloadSHA256 == "" {
		t.Fatalf("Events() = %#v, %v", events, err)
	}
	if strings.Contains(events[0].String(), secret) {
		t.Fatalf("audit event leaked payload: %#v", events[0])
	}
	report, err := service.Doctor(ctx)
	if err != nil || report.Status != security.DoctorHealthy || report.AuditEvents != 1 ||
		report.ExternalModels != security.ExternalModelsDisabled {
		t.Fatalf("Doctor() = %#v, %v", report, err)
	}

	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(ctx, security.AuditBucket, "corrupt", []byte(events[0].String()+` trailing`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Doctor(ctx); err == nil {
		t.Fatal("Doctor() accepted a corrupt audit event")
	}
}
