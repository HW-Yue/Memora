package security

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	AuditVersion           = "memora.security-audit/v1"
	PolicyVersion          = "memora.security-policy/v1"
	AuditBucket            = "security_audit"
	AuditSucceeded         = "succeeded"
	AuditFailed            = "failed"
	DoctorHealthy          = "healthy"
	ExternalModelsDisabled = "disabled_host_only"
)

type Clock interface{ Now() time.Time }
type IDSource interface{ Next() (string, error) }

type Options struct {
	Clock Clock
	IDs   IDSource
}

type AuditInput struct {
	RequestID           string
	Method              string
	Actor               string
	AuthorizedDatabases []string
	PayloadSHA256       string
	Status              string
	ErrorCode           string
}

type AuditEvent struct {
	Version             string    `json:"version"`
	ID                  string    `json:"id"`
	OccurredAt          time.Time `json:"occurred_at"`
	RequestID           string    `json:"request_id"`
	Method              string    `json:"method"`
	Actor               string    `json:"actor"`
	AuthorizedDatabases []string  `json:"authorized_databases"`
	PayloadSHA256       string    `json:"payload_sha256"`
	Status              string    `json:"status"`
	ErrorCode           string    `json:"error_code,omitempty"`
}

type DoctorReport struct {
	Status         string `json:"status"`
	PolicyVersion  string `json:"policy_version"`
	ExternalModels string `json:"external_models"`
	AuditEvents    int    `json:"audit_events"`
}

type Service struct {
	mu      sync.Mutex
	store   store.Store
	clock   Clock
	ids     IDSource
	pending []AuditEvent
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

type uuidSource struct{}

func (uuidSource) Next() (string, error) { return uuid.NewString(), nil }

func New(database store.Store, options Options) *Service {
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.IDs == nil {
		options.IDs = uuidSource{}
	}
	return &Service{store: database, clock: options.Clock, ids: options.IDs}
}

func HashPayload(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func (service *Service) Record(ctx context.Context, input AuditInput) error {
	if service == nil || service.store == nil {
		return securityError(result.CodeInternal, "security audit Store is unavailable")
	}
	id, err := service.ids.Next()
	if err != nil {
		return securityError(result.CodeInternal, "security audit ID could not be allocated")
	}
	event := AuditEvent{
		Version: AuditVersion, ID: id, OccurredAt: service.clock.Now().UTC(),
		RequestID: input.RequestID, Method: input.Method, Actor: input.Actor,
		AuthorizedDatabases: append([]string{}, input.AuthorizedDatabases...),
		PayloadSHA256:       input.PayloadSHA256, Status: input.Status, ErrorCode: input.ErrorCode,
	}
	sort.Strings(event.AuthorizedDatabases)
	if err := validateAudit(event); err != nil {
		return err
	}
	service.mu.Lock()
	service.pending = append(service.pending, event)
	service.mu.Unlock()
	_ = service.Flush(ctx)
	return nil
}

func (service *Service) Flush(ctx context.Context) error {
	if service == nil || service.store == nil {
		return securityError(result.CodeInternal, "security audit Store is unavailable")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	for len(service.pending) > 0 {
		if err := service.persist(ctx, service.pending[0]); err != nil {
			return err
		}
		service.pending = service.pending[1:]
	}
	return nil
}

func (service *Service) persist(ctx context.Context, event AuditEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return securityError(result.CodeInternal, "security audit event could not be encoded")
	}
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return securityError(result.CodeInternal, "security audit transaction could not start")
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, AuditBucket, event.ID, encoded); err != nil {
		return securityError(result.CodeInternal, "security audit event could not be stored")
	}
	if err := tx.Commit(); err != nil {
		return securityError(result.CodeInternal, "security audit event could not be committed")
	}
	return nil
}

func (service *Service) Events(ctx context.Context) ([]AuditEvent, error) {
	if service == nil || service.store == nil {
		return nil, securityError(result.CodeInternal, "security audit Store is unavailable")
	}
	if err := service.Flush(ctx); err != nil {
		return nil, err
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return nil, securityError(result.CodeInternal, "security audit transaction could not start")
	}
	defer func() { _ = tx.Rollback() }()
	entries, err := tx.Scan(ctx, AuditBucket)
	if err != nil {
		return nil, securityError(result.CodeInternal, "security audit could not be scanned")
	}
	events := make([]AuditEvent, 0, len(entries))
	for _, entry := range entries {
		var event AuditEvent
		decoder := json.NewDecoder(strings.NewReader(string(entry.Value)))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&event) != nil || validateAudit(event) != nil {
			return nil, securityError(result.CodeValidation, "security audit contains a corrupt event")
		}
		var extra any
		if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
			return nil, securityError(result.CodeValidation, "security audit contains trailing content")
		}
		events = append(events, event)
	}
	sort.Slice(events, func(left, right int) bool {
		if events[left].OccurredAt.Equal(events[right].OccurredAt) {
			return events[left].ID < events[right].ID
		}
		return events[left].OccurredAt.Before(events[right].OccurredAt)
	})
	return events, nil
}

func (service *Service) Doctor(ctx context.Context) (DoctorReport, error) {
	events, err := service.Events(ctx)
	if err != nil {
		return DoctorReport{}, err
	}
	return DoctorReport{
		Status: DoctorHealthy, PolicyVersion: PolicyVersion,
		ExternalModels: ExternalModelsDisabled, AuditEvents: len(events),
	}, nil
}

func (event AuditEvent) String() string {
	encoded, _ := json.Marshal(event)
	return string(encoded)
}

func validateAudit(event AuditEvent) error {
	if event.Version != AuditVersion || invalidText(event.ID, 200, true) ||
		event.OccurredAt.IsZero() || invalidText(event.RequestID, 200, true) ||
		invalidText(event.Method, 120, true) || invalidText(event.Actor, 160, true) ||
		!validSHA256(event.PayloadSHA256) ||
		(event.Status != AuditSucceeded && event.Status != AuditFailed) {
		return securityError(result.CodeValidation, "security audit event is invalid")
	}
	if event.Status == AuditSucceeded && event.ErrorCode != "" {
		return securityError(result.CodeValidation, "successful security audit event has an error code")
	}
	if event.Status == AuditFailed && strings.TrimSpace(event.ErrorCode) == "" {
		return securityError(result.CodeValidation, "failed security audit event requires an error code")
	}
	if len(event.AuthorizedDatabases) > maxDatabaseScope {
		return securityError(result.CodeValidation, "security audit Database scope exceeds its budget")
	}
	for _, database := range event.AuthorizedDatabases {
		if invalidText(database, 200, true) {
			return securityError(result.CodeValidation, "security audit Database scope is invalid")
		}
	}
	return nil
}

func (report DoctorReport) String() string {
	return fmt.Sprintf("%s/%s/%d", report.Status, report.PolicyVersion, report.AuditEvents)
}
