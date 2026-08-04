package agent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

func TestAssimilationReviewerAcceptsIndependentChallengeBoundChecks(t *testing.T) {
	batch, extent := reviewDraftBatch(t)
	root := t.TempDir()
	store, err := agent.OpenAssimilationReviewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	challenge := strings.Repeat("a", 64)
	challenges := &reviewChallengeSource{values: []string{challenge}}
	provider := &shortTextProvider{response: assimilationReviewResponse(
		t, "review-model", reviewToolArguments(batch, challenge, true),
	)}
	reviewer := newAssimilationReviewer(t, provider, store, challenges, "review-model")

	artifact, err := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Validate() != nil || artifact.Replayed || artifact.Status != agent.AssimilationReviewAccepted ||
		artifact.Author != "agent:assimilation-author" || artifact.Reviewer != "agent:assimilation-reviewer" ||
		artifact.BatchSHA256 != batch.SHA256 || artifact.ExtentSHA256 != extent.SHA256 ||
		len(artifact.Checks) != len(batch.Claims) || !validTestSHA256(artifact.ChallengeSHA256) ||
		!validTestSHA256(artifact.InputSHA256) || !validTestSHA256(artifact.SHA256) {
		t.Fatalf("artifact = %#v", artifact)
	}
	if provider.Calls() != 1 || challenges.Calls() != 1 {
		t.Fatalf("calls = Provider %d Challenge %d", provider.Calls(), challenges.Calls())
	}
	request := provider.Requests()[0]
	if request.ToolChoice != agent.ProviderToolChoiceRequired || len(request.Tools) != 1 ||
		request.Tools[0].Name != agent.AssimilationReviewToolName || len(request.Messages) != 2 ||
		request.Messages[0].Role != agent.ProviderRoleSystem || request.Messages[1].Role != agent.ProviderRoleUser ||
		strings.Contains(request.Messages[1].Content, `"role":"assistant"`) ||
		!strings.Contains(request.Messages[1].Content, challenge) {
		t.Fatalf("fresh review request = %#v", request)
	}
	wantNumeric := reviewNumericEvidenceID(batch.Claims[0].ClaimID, "$named.year", "2026")
	if len(artifact.Checks[0].NumericEvidenceIDs) != 1 ||
		artifact.Checks[0].NumericEvidenceIDs[0] != wantNumeric ||
		!artifact.Checks[0].AnchorVerified || !artifact.Checks[0].NumbersVerified ||
		!artifact.Checks[0].ConflictFree || !artifact.Checks[0].NotRawCopy {
		t.Fatalf("numeric review check = %#v", artifact.Checks[0])
	}

	encoded := onlyReviewArtifactFile(t, root)
	for _, forbidden := range []string{
		extent.Nodes[1].Text, "INSERT INTO work.notes", "Chapter summary",
		agent.AssimilationReviewSystemPrompt, challenge, `"bootstrap"`,
	} {
		if bytes.Contains(encoded, []byte(forbidden)) {
			t.Fatalf("artifact store leaked %q: %s", forbidden, encoded)
		}
	}
	info, err := os.Stat(onlyReviewArtifactPath(t, root))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("artifact mode = %v, %v", info.Mode().Perm(), err)
	}

	reopened, err := agent.OpenAssimilationReviewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	noProvider := &shortTextProvider{}
	noChallenges := &reviewChallengeSource{}
	restarted := newAssimilationReviewer(t, noProvider, reopened, noChallenges, "review-model")
	replayed, err := restarted.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	})
	if err != nil || !replayed.Replayed || replayed.SHA256 != artifact.SHA256 {
		t.Fatalf("review replay = %#v, %v", replayed, err)
	}
	if noProvider.Calls() != 0 || noChallenges.Calls() != 0 {
		t.Fatalf("replay calls = Provider %d Challenge %d", noProvider.Calls(), noChallenges.Calls())
	}
}

func TestAssimilationReviewerPersistsRejectedSemanticResult(t *testing.T) {
	batch, extent := reviewDraftBatch(t)
	store, err := agent.OpenAssimilationReviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	challenge := strings.Repeat("b", 64)
	arguments := reviewToolArgumentMap(batch, challenge, true)
	first := arguments["checks"].([]any)[0].(map[string]any)
	first["decision"] = "needs_user"
	first["conflict_free"] = false
	first["finding_codes"] = []string{"DRAFT_CONFLICT"}
	reviewer := newAssimilationReviewer(t,
		&shortTextProvider{response: assimilationReviewResponse(t, "review-model", mustJSON(t, arguments))},
		store, &reviewChallengeSource{values: []string{challenge}}, "review-model",
	)
	artifact, err := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	})
	if err != nil || artifact.Status != agent.AssimilationReviewRejected ||
		artifact.Checks[0].Decision != agent.AssimilationReviewNeedsUser ||
		artifact.Checks[0].ConflictFree || len(artifact.Checks[0].FindingCodes) != 1 {
		t.Fatalf("rejected artifact = %#v, %v", artifact, err)
	}
	stored, err := store.Read(batch.JobID, batch.ExtentSequence)
	if err != nil || stored.Status != agent.AssimilationReviewRejected || stored.SHA256 != artifact.SHA256 {
		t.Fatalf("stored rejection = %#v, %v", stored, err)
	}
}

func TestAssimilationReviewerRejectsInvalidIsolationAndToolOutputWithoutArtifact(t *testing.T) {
	batch, extent := reviewDraftBatch(t)
	tests := []struct {
		name      string
		reviewer  string
		challenge string
		arguments func(string) json.RawMessage
	}{
		{
			name: "same actor", reviewer: "agent:assimilation-author", challenge: strings.Repeat("c", 64),
			arguments: func(challenge string) json.RawMessage {
				return reviewToolArguments(batch, challenge, true)
			},
		},
		{
			name: "wrong challenge", reviewer: "agent:assimilation-reviewer", challenge: strings.Repeat("d", 64),
			arguments: func(string) json.RawMessage {
				return reviewToolArguments(batch, strings.Repeat("e", 64), true)
			},
		},
		{
			name: "missing claim check", reviewer: "agent:assimilation-reviewer", challenge: strings.Repeat("f", 64),
			arguments: func(challenge string) json.RawMessage {
				return reviewToolArguments(batch, challenge, false)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := agent.OpenAssimilationReviewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			provider := &shortTextProvider{response: assimilationReviewResponse(
				t, "review-model", test.arguments(test.challenge),
			)}
			reviewer := newAssimilationReviewerWithActor(
				t, provider, store, &reviewChallengeSource{values: []string{test.challenge}},
				"review-model", test.reviewer,
			)
			_, reviewErr := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
				Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
			})
			if !errors.Is(reviewErr, agent.ErrInvalidAssimilationReview) {
				t.Fatalf("Review() error = %v", reviewErr)
			}
			if _, err := store.Read(batch.JobID, batch.ExtentSequence); !errors.Is(err, agent.ErrAssimilationReviewNotFound) {
				t.Fatalf("Read() error = %v", err)
			}
		})
	}
}

func TestAssimilationReviewStoreRejectsInputConflictAndCorruption(t *testing.T) {
	batch, extent := reviewDraftBatch(t)
	root := t.TempDir()
	store, err := agent.OpenAssimilationReviewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	challenge := strings.Repeat("1", 64)
	reviewer := newAssimilationReviewer(t,
		&shortTextProvider{response: assimilationReviewResponse(
			t, "review-model", reviewToolArguments(batch, challenge, true),
		)}, store, &reviewChallengeSource{values: []string{challenge}}, "review-model",
	)
	if _, err := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	}); err != nil {
		t.Fatal(err)
	}
	conflicting := newAssimilationReviewer(t, &shortTextProvider{}, store, &reviewChallengeSource{}, "review-model-v2")
	if _, err := conflicting.Review(context.Background(), agent.AssimilationReviewRequest{
		Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
	}); !errors.Is(err, agent.ErrAssimilationReviewConflict) {
		t.Fatalf("conflicting Review() error = %v", err)
	}

	path := onlyReviewArtifactPath(t, root)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Replace(encoded, []byte("agent:assimilation-reviewer"), []byte("agent:assimilation-reviewes"), 1)
	if bytes.Equal(corrupt, encoded) {
		t.Fatal("corruption target absent")
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := agent.OpenAssimilationReviewStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := broken.Read(batch.JobID, batch.ExtentSequence); !errors.Is(err, agent.ErrAssimilationReviewCorrupt) {
		t.Fatalf("corrupt Read() error = %v", err)
	}
}

func TestAssimilationReviewerCoalescesConcurrentReview(t *testing.T) {
	batch, extent := reviewDraftBatch(t)
	store, err := agent.OpenAssimilationReviewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	challenge := strings.Repeat("2", 64)
	provider := &shortTextProvider{response: assimilationReviewResponse(
		t, "review-model", reviewToolArguments(batch, challenge, true),
	)}
	challenges := &reviewChallengeSource{values: []string{challenge}}
	reviewer := newAssimilationReviewer(t, provider, store, challenges, "review-model")

	const callers = 16
	artifacts := make(chan agent.AssimilationReviewArtifact, callers)
	errorsFound := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			artifact, reviewErr := reviewer.Review(context.Background(), agent.AssimilationReviewRequest{
				Batch: batch, Extent: extent, PeerClaims: []agent.DraftClaim{},
			})
			if reviewErr != nil {
				errorsFound <- reviewErr
				return
			}
			artifacts <- artifact
		}()
	}
	wait.Wait()
	close(artifacts)
	close(errorsFound)
	for reviewErr := range errorsFound {
		t.Fatalf("concurrent Review() error = %v", reviewErr)
	}
	wantSHA256 := ""
	replayed := 0
	for artifact := range artifacts {
		if wantSHA256 == "" {
			wantSHA256 = artifact.SHA256
		}
		if artifact.SHA256 != wantSHA256 || artifact.Validate() != nil {
			t.Fatalf("concurrent artifact = %#v", artifact)
		}
		if artifact.Replayed {
			replayed++
		}
	}
	if provider.Calls() != 1 || challenges.Calls() != 1 || replayed != callers-1 {
		t.Fatalf("calls = Provider %d Challenge %d Replayed %d", provider.Calls(), challenges.Calls(), replayed)
	}
}

func reviewDraftBatch(t *testing.T) (agent.DraftClaimBatch, agent.ReadExtent) {
	t.Helper()
	document, extent := assimilationDraftExtent(t)
	arguments := validAssimilationDraftArgumentMap(extent)
	first := arguments["claims"].([]any)[0].(map[string]any)
	first["named"].(map[string]any)["year"] = 2026
	ledger, err := agent.OpenDraftClaimLedger(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	drafter := newAssimilationDrafter(t,
		&shortTextMSQL{responses: []protocolmsql.Envelope{shortTextBootstrapEnvelope()}},
		&shortTextProvider{response: assimilationDraftToolResponse(mustJSON(t, arguments))}, ledger,
	)
	if _, err := drafter.Draft(context.Background(), assimilationDraftRequest(document, extent)); err != nil {
		t.Fatal(err)
	}
	batch, err := ledger.ReadExtent("job-draft", extent.Sequence)
	if err != nil {
		t.Fatal(err)
	}
	return batch, extent
}

func newAssimilationReviewer(
	t *testing.T,
	provider agent.Provider,
	store *agent.AssimilationReviewStore,
	challenges agent.AssimilationReviewChallengeSource,
	model string,
) *agent.AssimilationReviewer {
	t.Helper()
	return newAssimilationReviewerWithActor(
		t, provider, store, challenges, model, "agent:assimilation-reviewer",
	)
}

func newAssimilationReviewerWithActor(
	t *testing.T,
	provider agent.Provider,
	store *agent.AssimilationReviewStore,
	challenges agent.AssimilationReviewChallengeSource,
	model, reviewer string,
) *agent.AssimilationReviewer {
	t.Helper()
	subject, err := agent.NewAssimilationReviewer(agent.AssimilationReviewerDependencies{
		Provider: provider, Store: store, Challenges: challenges,
	}, agent.AssimilationReviewerConfig{
		Reviewer: reviewer, ProviderID: "openai-compatible-review", Model: model,
		MaxOutputTokens: 4096, MaxPeerClaims: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	return subject
}

func reviewToolArguments(batch agent.DraftClaimBatch, challenge string, all bool) json.RawMessage {
	checks := reviewToolArgumentMap(batch, challenge, all)
	encoded, _ := json.Marshal(checks)
	return encoded
}

func reviewToolArgumentMap(batch agent.DraftClaimBatch, challenge string, all bool) map[string]any {
	limit := len(batch.Claims)
	if !all {
		limit--
	}
	checks := make([]any, 0, limit)
	for index, claim := range batch.Claims[:limit] {
		numeric := []string{}
		if index == 0 {
			numeric = append(numeric, reviewNumericEvidenceID(claim.ClaimID, "$named.year", "2026"))
		}
		nodes := make([]string, len(claim.Sources))
		for sourceIndex, source := range claim.Sources {
			nodes[sourceIndex] = source.NodeID
		}
		checks = append(checks, map[string]any{
			"claim_id": claim.ClaimID, "source_node_ids": nodes,
			"numeric_evidence_ids": numeric, "anchor_verified": true,
			"numbers_verified": true, "conflict_free": true, "not_raw_copy": true,
			"decision": "accept", "finding_codes": []string{},
		})
	}
	return map[string]any{"challenge": challenge, "checks": checks}
}

func assimilationReviewResponse(
	t *testing.T,
	model string,
	arguments json.RawMessage,
) agent.ProviderResponse {
	t.Helper()
	response := queryProviderResponse(agent.ProviderFinishToolCalls, agent.ProviderMessage{
		Role: agent.ProviderRoleAssistant, ToolCalls: []agent.ProviderToolCall{{
			ID: "call-assimilation-review", Name: agent.AssimilationReviewToolName, Arguments: arguments,
		}},
	})
	response.Model = model
	return response
}

func reviewNumericEvidenceID(claimID, path, value string) string {
	digest := sha256.Sum256([]byte(claimID + "\x00" + path + "\x00" + value))
	return "sha256:" + hex.EncodeToString(digest[:])
}

type reviewChallengeSource struct {
	mu     sync.Mutex
	values []string
	calls  int
}

func (source *reviewChallengeSource) NextAssimilationReviewChallenge(context.Context) (string, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	index := source.calls
	source.calls++
	if index >= len(source.values) {
		return "", errors.New("unexpected challenge call")
	}
	return source.values[index], nil
}

func (source *reviewChallengeSource) Calls() int {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.calls
}

func onlyReviewArtifactPath(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].IsDir() || filepath.Ext(entries[0].Name()) != ".review" {
		t.Fatalf("review entries = %#v", entries)
	}
	return filepath.Join(root, entries[0].Name())
}

func onlyReviewArtifactFile(t *testing.T, root string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(onlyReviewArtifactPath(t, root))
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
