package agent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestReadCoverageDeterministicallyCoversEveryRequiredNode(t *testing.T) {
	t.Parallel()
	document := sealedReadCoverageDocument(t)
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	for !coverage.Receipt().Complete {
		extent, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 256})
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 99, MaxUTF8Bytes: 4096})
		if err != nil || !reflect.DeepEqual(replayed, extent) {
			t.Fatalf("pending replay = %#v, %v; want %#v", replayed, err, extent)
		}
		if extent.Version != agent.ReadExtentVersion || extent.DocumentSHA256 != document.SHA256 ||
			extent.StartPosition != uint64(len(seen)) || extent.EndPosition <= extent.StartPosition ||
			!validTestSHA256(extent.SHA256) || len(extent.Nodes) > 2 {
			t.Fatalf("extent = %#v", extent)
		}
		for _, node := range extent.Nodes {
			seen = append(seen, node.NodeID)
			if len(node.Context) == 0 || node.Context[0].Kind != agent.DocumentNodeDocument {
				t.Fatalf("node context = %#v", node.Context)
			}
		}
		if _, err := coverage.Acknowledge(extent.Sequence+1, extent.SHA256); !errors.Is(err, agent.ErrReadCoverageConflict) {
			t.Fatalf("wrong sequence error = %v", err)
		}
		if _, err := coverage.Acknowledge(extent.Sequence, "sha256:"+strings.Repeat("0", 64)); !errors.Is(err, agent.ErrReadCoverageConflict) {
			t.Fatalf("wrong digest error = %v", err)
		}
		receipt, err := coverage.Acknowledge(extent.Sequence, extent.SHA256)
		if err != nil || receipt.Replay || receipt.CoveredNodes != extent.EndPosition {
			t.Fatalf("ack receipt = %#v, %v", receipt, err)
		}
		replayReceipt, err := coverage.Acknowledge(extent.Sequence, extent.SHA256)
		if err != nil || !replayReceipt.Replay || replayReceipt.CoveredNodes != receipt.CoveredNodes {
			t.Fatalf("ack replay = %#v, %v", replayReceipt, err)
		}
	}
	if !reflect.DeepEqual(seen, document.ReadingOrder) {
		t.Fatalf("seen = %#v, want %#v", seen, document.ReadingOrder)
	}
	if _, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 1, MaxUTF8Bytes: 256}); !errors.Is(err, agent.ErrReadCoverageComplete) {
		t.Fatalf("complete Next error = %v", err)
	}
}

func TestReadCoverageCheckpointResumesPendingExtentWithoutSourceText(t *testing.T) {
	t.Parallel()
	document := sealedReadCoverageDocument(t)
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}
	first, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 1, MaxUTF8Bytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coverage.Acknowledge(first.Sequence, first.SHA256); err != nil {
		t.Fatal(err)
	}
	pending, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := coverage.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}
	lower := bytes.ToLower(checkpoint)
	for _, forbidden := range []string{"chapter one", "paragraph", "footnote detail", `"text"`, `"content"`, `"body"`, `"chunk"`, `"label"`} {
		if bytes.Contains(lower, []byte(forbidden)) {
			t.Fatalf("checkpoint leaked %q: %s", forbidden, checkpoint)
		}
	}
	restored, err := agent.RestoreReadCoverage(document, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := restored.Next(agent.ReadExtentLimits{MaxNodes: 1, MaxUTF8Bytes: 1})
	if err != nil || !reflect.DeepEqual(rebuilt, pending) {
		t.Fatalf("rebuilt pending = %#v, %v; want %#v", rebuilt, err, pending)
	}
	checkpointAgain, err := restored.Checkpoint()
	if err != nil || !bytes.Equal(checkpointAgain, checkpoint) {
		t.Fatalf("restored checkpoint = %s, %v; want %s", checkpointAgain, err, checkpoint)
	}
	if _, err := restored.Acknowledge(pending.Sequence, pending.SHA256); err != nil {
		t.Fatal(err)
	}
	for !restored.Receipt().Complete {
		extent, err := restored.Next(agent.ReadExtentLimits{MaxNodes: 3, MaxUTF8Bytes: 1024})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := restored.Acknowledge(extent.Sequence, extent.SHA256); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadCoverageRejectsBudgetViolationsWithoutSplittingNodes(t *testing.T) {
	t.Parallel()
	document := sealedReadCoverageDocument(t)
	for _, limits := range []agent.ReadExtentLimits{
		{}, {MaxNodes: -1, MaxUTF8Bytes: 10}, {MaxNodes: 1, MaxUTF8Bytes: -1},
	} {
		coverage, err := agent.NewReadCoverage(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := coverage.Next(limits); !errors.Is(err, agent.ErrReadCoverageBudget) {
			t.Fatalf("limits %#v error = %v", limits, err)
		}
	}
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 1, MaxUTF8Bytes: 1}); !errors.Is(err, agent.ErrReadCoverageBudget) {
		t.Fatalf("oversized semantic node error = %v", err)
	}
	if coverage.Receipt().CoveredNodes != 0 {
		t.Fatalf("budget failure advanced coverage: %#v", coverage.Receipt())
	}
	extent, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 1, MaxUTF8Bytes: 256})
	if err != nil || len(extent.Nodes) != 1 || extent.EndPosition-extent.StartPosition != 1 {
		t.Fatalf("whole semantic node extent = %#v, %v", extent, err)
	}
}

func TestReadCoverageRejectsCheckpointCorruptionAndDifferentDocument(t *testing.T) {
	t.Parallel()
	document := sealedReadCoverageDocument(t)
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 256}); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := coverage.Checkpoint()
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(checkpoint, &raw); err != nil {
		t.Fatal(err)
	}
	mutations := []func(map[string]any){
		func(value map[string]any) { value["revision"] = float64(99) },
		func(value map[string]any) { value["unknown"] = true },
		func(value map[string]any) { value["sha256"] = "sha256:" + strings.Repeat("0", 64) },
		func(value map[string]any) { value["covered_nodes"] = float64(999) },
	}
	for index, mutate := range mutations {
		clone := make(map[string]any, len(raw))
		for key, value := range raw {
			clone[key] = value
		}
		mutate(clone)
		encoded, err := json.Marshal(clone)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := agent.RestoreReadCoverage(document, encoded); !errors.Is(err, agent.ErrReadCoverageInvalid) {
			t.Fatalf("mutation %d error = %v", index, err)
		}
	}
	if _, err := agent.RestoreReadCoverage(document, checkpoint[:len(checkpoint)/2]); !errors.Is(err, agent.ErrReadCoverageInvalid) {
		t.Fatalf("truncated checkpoint error = %v", err)
	}

	changedDraft := validDocumentIR(t)
	changedDraft.Nodes[3].Text = "changed source interpretation"
	changed, err := agent.SealDocumentIR(changedDraft)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := agent.RestoreReadCoverage(changed, checkpoint); !errors.Is(err, agent.ErrReadCoverageInvalid) {
		t.Fatalf("different document error = %v", err)
	}
}

func TestReadCoverageConcurrentAcknowledgementHasOneCommit(t *testing.T) {
	t.Parallel()
	document := sealedReadCoverageDocument(t)
	coverage, err := agent.NewReadCoverage(document)
	if err != nil {
		t.Fatal(err)
	}
	extent, err := coverage.Next(agent.ReadExtentLimits{MaxNodes: 2, MaxUTF8Bytes: 256})
	if err != nil {
		t.Fatal(err)
	}
	var commits atomic.Int64
	var failures atomic.Int64
	var wait sync.WaitGroup
	for index := 0; index < 32; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			receipt, err := coverage.Acknowledge(extent.Sequence, extent.SHA256)
			if err != nil {
				failures.Add(1)
				return
			}
			if !receipt.Replay {
				commits.Add(1)
			}
		}()
	}
	wait.Wait()
	if failures.Load() != 0 || commits.Load() != 1 {
		t.Fatalf("failures/commits = %d/%d", failures.Load(), commits.Load())
	}
}

func sealedReadCoverageDocument(t *testing.T) agent.DocumentIR {
	t.Helper()
	document, err := agent.SealDocumentIR(validDocumentIR(t))
	if err != nil {
		t.Fatal(err)
	}
	return document
}
