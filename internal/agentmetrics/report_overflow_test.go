package agentmetrics

import (
	"math"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestMetricsAdditionFailsClosedOnCounterOverflow(t *testing.T) {
	if checked := checkedAdd(new(uint64), math.MaxUint64); !checked {
		t.Fatal("max counter should be representable")
	}
	value := Metrics{Events: math.MaxUint64, StatusCounts: map[string]uint64{}, FailureCounts: map[string]uint64{}, CostMicros: map[string]uint64{}}
	event := agent.TraceEvent{Status: agent.TraceStatusSucceeded}
	if err := value.add(event); err != ErrMetricsOverflow {
		t.Fatalf("overflow error = %v", err)
	}
}
