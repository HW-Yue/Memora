package agent_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestTraceRecorderReplaysRedactedProviderEvent(t *testing.T) {
	t.Parallel()

	recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run-1", SessionID: "session-1"})
	if err != nil {
		t.Fatal(err)
	}
	secretInput := []byte("prompt with sk-secret and private row body")
	secretOutput := []byte("hidden reasoning and answer")
	started := time.Date(2026, 8, 3, 9, 30, 0, 123000000, time.UTC)
	wantDraft := agent.TraceDraft{
		Turn:       1,
		Kind:       agent.TraceKindProvider,
		Operation:  "provider.complete",
		StartedAt:  started,
		FinishedAt: started.Add(1750 * time.Microsecond),
		Status:     agent.TraceStatusSucceeded,
		Input:      agent.DigestTracePayload(secretInput),
		Output:     agent.DigestTracePayload(secretOutput),
		Model:      "moonshot-v1-test",
		Usage: &agent.ProviderUsage{
			InputTokens: 120, CachedInputTokens: 20, OutputTokens: 30, TotalTokens: 150,
		},
		Cost: &agent.TraceCost{Currency: "CNY", Micros: 42, PriceSnapshotID: "moonshot-2026-08-03"},
	}
	event, err := recorder.Append(wantDraft)
	if err != nil {
		t.Fatal(err)
	}
	if event.Version != agent.TraceEventVersion || event.RunID != "run-1" || event.SessionID != "session-1" ||
		event.Sequence != 1 || event.DurationMicros != 1750 {
		t.Fatalf("event identity/timing = %#v", event)
	}

	envelope := recorder.Snapshot()
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{string(secretInput), string(secretOutput), "sk-secret", "private row body", "hidden reasoning"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("trace leaked %q: %s", secret, encoded)
		}
	}
	var replayed agent.TraceEnvelope
	if err := json.Unmarshal(encoded, &replayed); err != nil {
		t.Fatal(err)
	}
	if err := replayed.Validate(); err != nil {
		t.Fatalf("replayed Validate() error = %v\n%s", err, encoded)
	}
	if !reflect.DeepEqual(replayed, envelope) {
		t.Fatalf("round trip mismatch\n got: %#v\nwant: %#v", replayed, envelope)
	}
}

func TestTraceRecorderSummarizesEventsAndReturnsDefensiveSnapshots(t *testing.T) {
	t.Parallel()

	recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run-summary", SessionID: "session-summary"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	provider := validTraceDraft(base)
	if _, err := recorder.Append(provider); err != nil {
		t.Fatal(err)
	}
	msql := validTraceDraft(base.Add(time.Second))
	msql.Kind = agent.TraceKindMSQL
	msql.Operation = "msql.execute"
	msql.FinishedAt = msql.StartedAt.Add(250 * time.Microsecond)
	msql.Status = agent.TraceStatusFailed
	msql.ErrorCode = "MSQL_EXECUTION_FAILED"
	msql.Model = ""
	msql.Usage = nil
	msql.Cost = nil
	if _, err := recorder.Append(msql); err != nil {
		t.Fatal(err)
	}

	snapshot := recorder.Snapshot()
	if snapshot.Version != agent.TraceEnvelopeVersion || snapshot.RunID != "run-summary" || snapshot.SessionID != "session-summary" {
		t.Fatalf("envelope identity = %#v", snapshot)
	}
	if snapshot.Summary.Events != 2 || snapshot.Summary.KindCounts[agent.TraceKindProvider] != 1 ||
		snapshot.Summary.KindCounts[agent.TraceKindMSQL] != 1 ||
		snapshot.Summary.StatusCounts[agent.TraceStatusSucceeded] != 1 ||
		snapshot.Summary.StatusCounts[agent.TraceStatusFailed] != 1 ||
		snapshot.Summary.DurationMicros[agent.TraceKindProvider] != 1750 ||
		snapshot.Summary.DurationMicros[agent.TraceKindMSQL] != 250 {
		t.Fatalf("summary counts/durations = %#v", snapshot.Summary)
	}
	if snapshot.Summary.InputTokens != 120 || snapshot.Summary.CachedInputTokens != 20 ||
		snapshot.Summary.OutputTokens != 30 || snapshot.Summary.TotalTokens != 150 ||
		snapshot.Summary.CostMicrosByCurrency["CNY"] != 42 {
		t.Fatalf("summary usage/cost = %#v", snapshot.Summary)
	}

	snapshot.Events[0].Operation = "tampered"
	snapshot.Events[0].Usage.InputTokens = 999
	snapshot.Events[0].Cost.Micros = 999
	snapshot.Summary.KindCounts[agent.TraceKindProvider] = 999
	snapshot.Summary.CostMicrosByCurrency["CNY"] = 999
	fresh := recorder.Snapshot()
	if fresh.Events[0].Operation != "provider.complete" || fresh.Events[0].Usage.InputTokens != 120 ||
		fresh.Events[0].Cost.Micros != 42 || fresh.Summary.KindCounts[agent.TraceKindProvider] != 1 ||
		fresh.Summary.CostMicrosByCurrency["CNY"] != 42 {
		t.Fatalf("Snapshot() exposed recorder state: %#v", fresh)
	}
}

func TestTraceRecorderRejectsMalformedIdentityAndDrafts(t *testing.T) {
	t.Parallel()

	for _, identity := range []agent.TraceIdentity{
		{},
		{RunID: "run", SessionID: " "},
		{RunID: strings.Repeat("r", 161), SessionID: "session"},
		{RunID: "run\x00secret", SessionID: "session"},
	} {
		if _, err := agent.NewTraceRecorder(identity); !errors.Is(err, agent.ErrInvalidTraceIdentity) {
			t.Fatalf("NewTraceRecorder(%#v) error = %v", identity, err)
		}
	}

	base := time.Date(2026, 8, 3, 11, 0, 0, 0, time.UTC)
	valid := validTraceDraft(base)
	tests := []struct {
		name   string
		mutate func(*agent.TraceDraft)
	}{
		{"zero turn", func(value *agent.TraceDraft) { value.Turn = 0 }},
		{"unsupported kind", func(value *agent.TraceDraft) { value.Kind = "network" }},
		{"unsafe operation", func(value *agent.TraceDraft) { value.Operation = "provider complete secret" }},
		{"non UTC", func(value *agent.TraceDraft) { value.StartedAt = value.StartedAt.In(time.FixedZone("CST", 8*60*60)) }},
		{"negative duration", func(value *agent.TraceDraft) { value.FinishedAt = value.StartedAt.Add(-time.Nanosecond) }},
		{"bad input digest", func(value *agent.TraceDraft) { value.Input.SHA256 = "abc" }},
		{"failed without code", func(value *agent.TraceDraft) { value.Status = agent.TraceStatusFailed }},
		{"succeeded with code", func(value *agent.TraceDraft) { value.ErrorCode = "SHOULD_NOT_EXIST" }},
		{"bad usage total", func(value *agent.TraceDraft) { value.Usage.TotalTokens++ }},
		{"cost without snapshot", func(value *agent.TraceDraft) { value.Cost.PriceSnapshotID = "" }},
		{"provider without model", func(value *agent.TraceDraft) { value.Model = "" }},
		{"provider without usage", func(value *agent.TraceDraft) { value.Usage = nil }},
		{"non provider metadata", func(value *agent.TraceDraft) { value.Kind = agent.TraceKindTool; value.Operation = "tool.execute" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			draft := cloneTraceDraft(valid)
			test.mutate(&draft)
			recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run-invalid", SessionID: "session-invalid"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := recorder.Append(draft); !errors.Is(err, agent.ErrInvalidTraceEvent) {
				t.Fatalf("Append() error = %v", err)
			}
			if got := recorder.Snapshot().Summary.Events; got != 0 {
				t.Fatalf("failed append changed recorder, events = %d", got)
			}
		})
	}
}

func TestTraceEnvelopeValidationDetectsReplayTampering(t *testing.T) {
	t.Parallel()

	base := time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)
	recorder, _ := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run-replay", SessionID: "session-replay"})
	if _, err := recorder.Append(validTraceDraft(base)); err != nil {
		t.Fatal(err)
	}
	want := recorder.Snapshot()
	tests := []struct {
		name   string
		mutate func(*agent.TraceEnvelope)
	}{
		{"version", func(value *agent.TraceEnvelope) { value.Version = "v2" }},
		{"sequence", func(value *agent.TraceEnvelope) { value.Events[0].Sequence = 2 }},
		{"identity", func(value *agent.TraceEnvelope) { value.Events[0].RunID = "other" }},
		{"duration", func(value *agent.TraceEnvelope) { value.Events[0].DurationMicros++ }},
		{"summary", func(value *agent.TraceEnvelope) { value.Summary.TotalTokens++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded, _ := json.Marshal(want)
			var tampered agent.TraceEnvelope
			_ = json.Unmarshal(encoded, &tampered)
			test.mutate(&tampered)
			if err := tampered.Validate(); !errors.Is(err, agent.ErrInvalidTraceEnvelope) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestTraceRecorderConcurrentAppendHasContinuousSequence(t *testing.T) {
	t.Parallel()

	const writers = 128
	recorder, err := agent.NewTraceRecorder(agent.TraceIdentity{RunID: "run-concurrent", SessionID: "session-concurrent"})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 8, 3, 13, 0, 0, 0, time.UTC)
	errorsOut := make(chan error, writers)
	done := make(chan struct{})
	readerErrors := make(chan error, 1)
	go func() {
		for {
			select {
			case <-done:
				readerErrors <- nil
				return
			default:
				snapshot := recorder.Snapshot()
				if validationErr := snapshot.Validate(); validationErr != nil {
					readerErrors <- validationErr
					return
				}
			}
		}
	}()
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			draft := validTraceDraft(base.Add(time.Duration(index) * time.Second))
			draft.Turn = uint64(index + 1)
			draft.Kind = agent.TraceKindMSQL
			draft.Operation = "msql.execute"
			draft.Model = ""
			draft.Usage = nil
			draft.Cost = nil
			_, appendErr := recorder.Append(draft)
			errorsOut <- appendErr
		}(index)
	}
	group.Wait()
	close(done)
	close(errorsOut)
	for appendErr := range errorsOut {
		if appendErr != nil {
			t.Fatal(appendErr)
		}
	}
	if readerErr := <-readerErrors; readerErr != nil {
		t.Fatalf("concurrent Snapshot().Validate() error = %v", readerErr)
	}
	snapshot := recorder.Snapshot()
	if len(snapshot.Events) != writers || snapshot.Summary.Events != writers {
		t.Fatalf("events = %d, summary = %d", len(snapshot.Events), snapshot.Summary.Events)
	}
	sequences := make([]int, 0, writers)
	for _, event := range snapshot.Events {
		sequences = append(sequences, int(event.Sequence))
	}
	sort.Ints(sequences)
	for index, sequence := range sequences {
		if sequence != index+1 {
			t.Fatalf("sequences = %v", sequences)
		}
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("Snapshot().Validate() error = %v", err)
	}
}

func TestTraceContractHasNoRawContentFields(t *testing.T) {
	t.Parallel()

	forbidden := map[string]bool{
		"content": true, "prompt": true, "row": true, "source": true, "parameters": true,
		"arguments": true, "api_key": true, "reasoning": true, "endpoint": true, "headers": true,
		"error_message": true,
	}
	seen := make(map[reflect.Type]bool)
	var visit func(reflect.Type)
	visit = func(value reflect.Type) {
		for value.Kind() == reflect.Pointer || value.Kind() == reflect.Slice || value.Kind() == reflect.Array || value.Kind() == reflect.Map {
			value = value.Elem()
		}
		if value.Kind() != reflect.Struct || value.PkgPath() != "github.com/HW-Yue/Memora/internal/agent" || seen[value] {
			return
		}
		seen[value] = true
		for index := 0; index < value.NumField(); index++ {
			field := value.Field(index)
			name := strings.Split(field.Tag.Get("json"), ",")[0]
			if forbidden[name] {
				t.Errorf("%s.%s exposes forbidden JSON field %q", value.Name(), field.Name, name)
			}
			visit(field.Type)
		}
	}
	visit(reflect.TypeOf(agent.TraceEnvelope{}))
	visit(reflect.TypeOf(agent.TraceDraft{}))
	if t.Failed() {
		t.FailNow()
	}
	for _, required := range []reflect.Type{
		reflect.TypeOf(agent.TraceEnvelope{}), reflect.TypeOf(agent.TraceEvent{}), reflect.TypeOf(agent.TraceSummary{}),
		reflect.TypeOf(agent.TraceDigest{}), reflect.TypeOf(agent.TraceCost{}),
	} {
		if !seen[required] {
			t.Fatalf("contract traversal missed %s", required)
		}
	}
}

func validTraceDraft(started time.Time) agent.TraceDraft {
	return agent.TraceDraft{
		Turn:       1,
		Kind:       agent.TraceKindProvider,
		Operation:  "provider.complete",
		StartedAt:  started,
		FinishedAt: started.Add(1750 * time.Microsecond),
		Status:     agent.TraceStatusSucceeded,
		Input:      agent.DigestTracePayload([]byte("input")),
		Output:     agent.DigestTracePayload([]byte("output")),
		Model:      "model-test",
		Usage: &agent.ProviderUsage{
			InputTokens: 120, CachedInputTokens: 20, OutputTokens: 30, TotalTokens: 150,
		},
		Cost: &agent.TraceCost{Currency: "CNY", Micros: 42, PriceSnapshotID: "price-v1"},
	}
}

func cloneTraceDraft(value agent.TraceDraft) agent.TraceDraft {
	cloned := value
	if value.Usage != nil {
		usage := *value.Usage
		cloned.Usage = &usage
	}
	if value.Cost != nil {
		cost := *value.Cost
		cloned.Cost = &cost
	}
	return cloned
}

func ExampleDigestTracePayload() {
	digest := agent.DigestTracePayload([]byte("private body"))
	fmt.Println(digest.Bytes)
	fmt.Println(strings.HasPrefix(digest.SHA256, "sha256:"))
	// Output:
	// 12
	// true
}
