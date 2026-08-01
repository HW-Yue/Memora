package executor

import (
	"testing"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/routemutationplan"
)

func TestRouteProposalDecoderRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	value := map[string]any{
		"version": routemutationplan.ProposalVersion, "proposal_id": "proposal_1", "operation": "MOVE",
		"actor": "agent:test", "source_event_id": "event_1", "reason": "move", "surprise": true,
	}
	if _, err := decodeRouteProposal(value); !hasStableCode(err, result.CodeValidation) {
		t.Fatalf("unknown proposal field error = %v", err)
	}
}
