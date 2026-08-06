package agent_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestQueryAgentPromptReservesFinalToolCallForSelect(t *testing.T) {
	for _, fragment := range []string{
		"prompt v4",
		"Bootstrap root_prefetches[].routes already contains authorized root children",
		"If kind is leaf",
		"OPEN ROUTE :leaf LIMIT 1",
		"If kind is branch",
		"SHOW ROUTES UNDER :branch LIMIT 12",
		"SELECT * FROM `projects`.`decisions` WHERE row_id = :row LIMIT 1",
		"Never emit a bare database/path",
		"unqualified table",
		"LIKE",
		"CONTAINS",
		"batch multiple OPEN statements",
		"batch the matching qualified SELECT statements",
	} {
		if !strings.Contains(agent.QueryAgentSystemPrompt, fragment) {
			t.Fatalf("Query Agent prompt missing %q", fragment)
		}
	}
}
