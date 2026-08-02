package agenttest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/agent/agenttest"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestScriptedMSQLChecksOrderRecordsCallsAndVerifiesExhaustion(t *testing.T) {
	t.Parallel()

	firstRequest := request("SELECT 1")
	secondRequest := request("SELECT 2")
	firstEnvelope := envelope("first")
	secondEnvelope := envelope("second")
	fake := agenttest.NewScriptedMSQL(
		agenttest.MSQLStep{Request: firstRequest, Envelope: firstEnvelope},
		agenttest.MSQLStep{Request: secondRequest, Envelope: secondEnvelope},
	)
	var port agent.MSQLExecutor = fake

	got, err := port.ExecuteMSQL(context.Background(), firstRequest)
	if err != nil || got.RequestID != firstEnvelope.RequestID {
		t.Fatalf("first ExecuteMSQL() = %#v, %v", got, err)
	}
	if !errors.Is(fake.Verify(), agenttest.ErrPendingCalls) {
		t.Fatalf("Verify() before exhaustion = %v", fake.Verify())
	}
	got, err = port.ExecuteMSQL(context.Background(), secondRequest)
	if err != nil || got.RequestID != secondEnvelope.RequestID {
		t.Fatalf("second ExecuteMSQL() = %#v, %v", got, err)
	}
	if err := fake.Verify(); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if calls := fake.Calls(); len(calls) != 2 || calls[0].Source != "SELECT 1" || calls[1].Source != "SELECT 2" {
		t.Fatalf("Calls() = %#v", calls)
	}
}

func TestScriptedMSQLReportsMismatchUnexpectedCallAndScriptedError(t *testing.T) {
	t.Parallel()

	fake := agenttest.NewScriptedMSQL(agenttest.MSQLStep{Request: request("SELECT expected")})
	if _, err := fake.ExecuteMSQL(context.Background(), request("SELECT other")); !errors.Is(err, agenttest.ErrUnexpectedRequest) {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := fake.ExecuteMSQL(context.Background(), request("SELECT extra")); !errors.Is(err, agenttest.ErrUnexpectedCall) {
		t.Fatalf("extra call error = %v", err)
	}

	wantErr := errors.New("scripted failure")
	failing := agenttest.NewScriptedMSQL(agenttest.MSQLStep{
		Request: request("SELECT fail"), Error: wantErr,
	})
	if _, err := failing.ExecuteMSQL(context.Background(), request("SELECT fail")); !errors.Is(err, wantErr) {
		t.Fatalf("scripted error = %v", err)
	}
}

func TestScriptedMSQLIsSafeForConcurrentIdenticalCalls(t *testing.T) {
	t.Parallel()

	const count = 32
	steps := make([]agenttest.MSQLStep, count)
	for index := range steps {
		steps[index] = agenttest.MSQLStep{Request: request("SELECT concurrent"), Envelope: envelope("concurrent")}
	}
	fake := agenttest.NewScriptedMSQL(steps...)
	var wait sync.WaitGroup
	wait.Add(count)
	for range count {
		go func() {
			defer wait.Done()
			if _, err := fake.ExecuteMSQL(context.Background(), request("SELECT concurrent")); err != nil {
				t.Errorf("ExecuteMSQL() error = %v", err)
			}
		}()
	}
	wait.Wait()
	if err := fake.Verify(); err != nil {
		t.Fatal(err)
	}
	if calls := fake.Calls(); len(calls) != count {
		t.Fatalf("call count = %d, want %d", len(calls), count)
	}
}

func request(source string) protocolmsql.Request {
	return protocolmsql.Request{Version: protocolmsql.RequestVersion, Source: source}
}

func envelope(requestID string) protocolmsql.Envelope {
	return protocolmsql.Envelope{Version: protocolmsql.EnvelopeVersion, RequestID: requestID}
}
