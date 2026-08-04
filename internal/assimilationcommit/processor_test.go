package assimilationcommit

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestProcessorExecutesOneMSQLTransactionPersistsRedactedReceiptAndReplays(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := nativekvstore.Open(t.TempDir() + "/assimilation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var calls atomic.Int64
	executor := ExecutorFunc(func(_ context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
		calls.Add(1)
		if request.Source != "BEGIN;INSERT INTO work.notes (title) VALUES (:title);COMMIT" || len(request.Statements) != 3 {
			t.Fatalf("request = %#v", request)
		}
		for _, input := range request.Statements {
			if input.Authorization.Actor != "agent:author" || input.Authorization.DefaultLevel != protocolmsql.LevelWrite ||
				len(input.Authorization.AuthorizedDatabases) != 1 || input.Authorization.AuthorizedDatabases[0] != "work" {
				t.Fatalf("authorization = %#v", input.Authorization)
			}
		}
		return committedEnvelope(request), nil
	})
	processor := New(database, executor)
	plan := sealedCommitPlan(t, "Private semantic body")
	receipt, err := processor.SubmitAssimilation(ctx, plan)
	if err != nil || receipt.Status != protocolmsql.AssimilationCommitted || receipt.Replayed ||
		len(receipt.Statements) != 1 || receipt.Statements[0].Kind != "INSERT" ||
		len(receipt.Statements[0].ObjectIDs) != 1 || receipt.Statements[0].ObjectIDs[0] != "row-actual-1" ||
		receipt.Evidence == nil || receipt.Evidence.ReviewArtifactSHA256s[0] != commitSHA("d") {
		t.Fatalf("receipt = %#v, %v", receipt, err)
	}

	reopened := New(database, executor)
	replayed, err := reopened.SubmitAssimilation(ctx, plan)
	if err != nil || !replayed.Replayed || calls.Load() != 1 {
		t.Fatalf("replayed = %#v, %v, calls=%d", replayed, err, calls.Load())
	}
	loaded, err := reopened.AssimilationReceipt(ctx, "work", plan.PlanID)
	if err != nil || loaded.Replayed || loaded.PlanSHA256 != plan.SHA256 {
		t.Fatalf("loaded = %#v, %v", loaded, err)
	}
	for _, bucket := range []string{recordBucket, receiptBucket} {
		tx, err := database.Begin(ctx, store.ReadOnly)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := tx.Scan(ctx, bucket)
		_ = tx.Rollback()
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			for _, forbidden := range []string{"Private semantic body", `"parameters"`, `"msql"`} {
				if bytes.Contains(entry.Value, []byte(forbidden)) {
					t.Fatalf("bucket %s leaked %q: %s", bucket, forbidden, entry.Value)
				}
			}
		}
	}

	changed := sealedCommitPlan(t, "changed semantic body")
	if _, err := reopened.SubmitAssimilation(ctx, changed); commitCode(err) != result.CodeRevisionConflict {
		t.Fatalf("changed plan error = %v", err)
	}
	if _, err := reopened.AssimilationReceipt(ctx, "private", plan.PlanID); commitCode(err) != result.CodePermissionDenied {
		t.Fatalf("cross-database receipt error = %v", err)
	}
}

func TestProcessorMarksTransportUncertaintyInDoubtWithoutBlindReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := nativekvstore.Open(t.TempDir() + "/assimilation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var calls atomic.Int64
	processor := New(database, ExecutorFunc(func(context.Context, protocolmsql.Request) (protocolmsql.Envelope, error) {
		calls.Add(1)
		return protocolmsql.Envelope{}, errors.New("transport closed after dispatch")
	}))
	plan := sealedCommitPlan(t, "uncertain semantic body")
	first, err := processor.SubmitAssimilation(ctx, plan)
	if err != nil || first.Status != protocolmsql.AssimilationInDoubt || first.Replayed ||
		first.Evidence == nil || first.Evidence.ReviewArtifactSHA256s[0] != commitSHA("d") {
		t.Fatalf("first = %#v, %v", first, err)
	}
	second, err := processor.SubmitAssimilation(ctx, plan)
	if err != nil || second.Status != protocolmsql.AssimilationInDoubt || !second.Replayed || calls.Load() != 1 {
		t.Fatalf("second = %#v, %v, calls=%d", second, err, calls.Load())
	}
}

func TestProcessorKnownRollbackReleasesReservationForGuardedRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := nativekvstore.Open(t.TempDir() + "/assimilation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var calls atomic.Int64
	processor := New(database, ExecutorFunc(func(_ context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
		if calls.Add(1) == 1 {
			return failedEnvelope(request), nil
		}
		return committedEnvelope(request), nil
	}))
	plan := sealedCommitPlan(t, "retry semantic body")
	if _, err := processor.SubmitAssimilation(ctx, plan); commitCode(err) != result.CodeRevisionConflict {
		t.Fatalf("known rollback error = %v", err)
	}
	receipt, err := processor.SubmitAssimilation(ctx, plan)
	if err != nil || receipt.Status != protocolmsql.AssimilationCommitted || calls.Load() != 2 {
		t.Fatalf("retry receipt = %#v, %v, calls=%d", receipt, err, calls.Load())
	}
}

func TestProcessorConcurrentSubmissionHasOneDispatch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := nativekvstore.Open(t.TempDir() + "/assimilation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var calls atomic.Int64
	processor := New(database, ExecutorFunc(func(_ context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
		calls.Add(1)
		return committedEnvelope(request), nil
	}))
	plan := sealedCommitPlan(t, "concurrent semantic body")
	var wait sync.WaitGroup
	var failures atomic.Int64
	for index := 0; index < 24; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if receipt, err := processor.SubmitAssimilation(ctx, plan); err != nil || receipt.Status != protocolmsql.AssimilationCommitted {
				failures.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("failures/calls = %d/%d", failures.Load(), calls.Load())
	}
}

func TestProcessorRejectsCorruptPersistedReceipt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	database, err := nativekvstore.Open(t.TempDir() + "/assimilation.db")
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	processor := New(database, ExecutorFunc(func(_ context.Context, request protocolmsql.Request) (protocolmsql.Envelope, error) {
		return committedEnvelope(request), nil
	}))
	plan := sealedCommitPlan(t, "corruption semantic body")
	if _, err := processor.SubmitAssimilation(ctx, plan); err != nil {
		t.Fatal(err)
	}
	tx, err := database.Begin(ctx, store.ReadWrite)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Put(ctx, receiptBucket, plan.PlanID, []byte(`{"version":"corrupt"}`)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := processor.AssimilationReceipt(ctx, "work", plan.PlanID); commitCode(err) != result.CodeInternal {
		t.Fatalf("corrupt receipt error = %v", err)
	}
}

func sealedCommitPlan(t *testing.T, title string) protocolmsql.AssimilationPlan {
	t.Helper()
	proposal := protocolmsql.AssimilationProposal{
		Version: protocolmsql.AssimilationProposalVersion, ProposalID: "plan-1",
		Database: "work", Author: "agent:author", SourceID: "source-1",
		SourceLocator: "book.epub", SourceSHA256: commitSHA("a"), DocumentSHA256: commitSHA("b"),
		CoverageRevision: 3, CoveredNodes: 4, TotalNodes: 4,
		Evidence: &protocolmsql.AssimilationEvidence{
			Version: protocolmsql.AssimilationEvidenceVersion, Author: "agent:author",
			Reviewers: []string{"agent:reviewer"}, ReviewArtifactSHA256s: []string{commitSHA("d")},
			CoverageRevision: 3, CoveredNodes: 4, TotalNodes: 4,
		},
		Statements: []protocolmsql.AssimilationStatement{{
			MSQL:       "INSERT INTO work.notes (title) VALUES (:title)",
			Parameters: protocolmsql.Parameters{Named: map[string]any{"title": title}},
			Mutation: protocolmsql.MutationOptions{
				ExpectedSchemaVersion: 1, MaxAffectedRows: 1, Actor: "agent:author",
				Source: "source-1", Reason: "assimilate semantic module",
				SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: "book.epub",
				SourceContentHash: commitSHA("a"),
			},
		}},
	}
	plan, err := protocolmsql.SealAssimilationPlan(proposal)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func committedEnvelope(request protocolmsql.Request) protocolmsql.Envelope {
	revision, sequence := uint64(1), uint64(9)
	envelope := protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "nested-1", OK: true,
		Results: []protocolmsql.StatementResult{
			commitStatement(0, "BEGIN", 0, nil, nil),
			commitStatement(1, "INSERT", 1, &revision, &sequence),
			commitStatement(2, "COMMIT", 0, nil, nil),
		}, Warnings: []protocolmsql.Notice{},
	}
	envelope.Results[1].Rows = []protocolmsql.Row{{"row_id": "row-actual-1"}}
	return envelope
}

func failedEnvelope(request protocolmsql.Request) protocolmsql.Envelope {
	index := 1
	return protocolmsql.Envelope{
		Version: protocolmsql.EnvelopeVersion, RequestID: "nested-failed", OK: false,
		Results: []protocolmsql.StatementResult{
			commitStatement(0, "BEGIN", 0, nil, nil),
			{
				Index: 1, Statement: "INSERT", Source: request.Source, Status: "failed",
				Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, Warnings: []protocolmsql.Notice{},
				Error: &protocolmsql.ResultError{Code: "revision_conflict", Message: "guard changed", StatementIndex: &index},
			},
			commitStatement(2, "COMMIT", 0, nil, nil),
		}, Warnings: []protocolmsql.Notice{},
	}
}

func commitStatement(index int, kind string, affected uint64, revision, sequence *uint64) protocolmsql.StatementResult {
	return protocolmsql.StatementResult{
		Index: index, Statement: kind, Source: kind, Status: "succeeded",
		Columns: []protocolmsql.Column{}, Rows: []protocolmsql.Row{}, AffectedRows: affected,
		Revision: revision, CommitSequence: sequence, Warnings: []protocolmsql.Notice{},
	}
}

func commitSHA(character string) string {
	return "sha256:" + strings.Repeat(character, 64)
}

func commitCode(err error) result.Code {
	var stable interface{ StableCode() string }
	if errors.As(err, &stable) {
		return result.Code(stable.StableCode())
	}
	return ""
}
