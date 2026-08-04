package agent

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const (
	AssimilationReviewArtifactVersion = "memora.assimilation-review-artifact/v1"
	AssimilationReviewCheckVersion    = "memora.assimilation-review-check/v1"
	AssimilationReviewPromptVersion   = "memora.assimilation-review-prompt/v1"
	AssimilationReviewContextVersion  = "memora.assimilation-review-context/v1"
	AssimilationReviewToolName        = "review_assimilation_claims"
	AssimilationReviewSystemPrompt    = "Memora independent assimilation reviewer v1. Start from this fresh request only. Compare every candidate claim with its cited ReadExtent nodes and bounded peer claims. Use the required review_assimilation_claims tool, echo the challenge exactly, and return exactly one check per target claim. Verify every supplied numeric evidence item, exact source node inventory, internal conflicts, and that the candidate is a complete editable semantic module rather than a mechanical source-window copy. Do not submit MSQL, rewrite claims, inherit author conclusions, or answer directly."
	AssimilationReviewAccepted        = "accepted"
	AssimilationReviewRejected        = "rejected"
	AssimilationReviewDecisionAccept  = "accept"
	AssimilationReviewDecisionReject  = "reject"
	AssimilationReviewNeedsUser       = "needs_user"
)

var (
	ErrInvalidAssimilationReviewer = errors.New("Assimilation Reviewer dependencies are invalid")
	ErrInvalidAssimilationReview   = errors.New("Assimilation Review input or result is invalid")
	ErrAssimilationReviewNotFound  = errors.New("Assimilation Review artifact was not found")
	ErrAssimilationReviewConflict  = errors.New("Assimilation Review input conflicts with the stored artifact")
	ErrAssimilationReviewCorrupt   = errors.New("Assimilation Review artifact is corrupt")
)

type AssimilationReviewChallengeSource interface {
	NextAssimilationReviewChallenge(context.Context) (string, error)
}

type CryptoAssimilationReviewChallenges struct{}

func (CryptoAssimilationReviewChallenges) NextAssimilationReviewChallenge(ctx context.Context) (string, error) {
	if ctx == nil {
		return "", ErrInvalidAssimilationReview
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("create Assimilation Review challenge: %w", err)
	}
	return hex.EncodeToString(value), nil
}

type AssimilationReviewRequest struct {
	Batch      DraftClaimBatch `json:"batch"`
	Extent     ReadExtent      `json:"extent"`
	PeerClaims []DraftClaim    `json:"peer_claims"`
}

type AssimilationReviewCheck struct {
	Version            string   `json:"version"`
	ClaimID            string   `json:"claim_id"`
	ClaimSHA256        string   `json:"claim_sha256"`
	SourceNodeIDs      []string `json:"source_node_ids"`
	NumericEvidenceIDs []string `json:"numeric_evidence_ids"`
	AnchorVerified     bool     `json:"anchor_verified"`
	NumbersVerified    bool     `json:"numbers_verified"`
	ConflictFree       bool     `json:"conflict_free"`
	NotRawCopy         bool     `json:"not_raw_copy"`
	Decision           string   `json:"decision"`
	FindingCodes       []string `json:"finding_codes"`
}

type AssimilationReviewArtifact struct {
	Version          string                    `json:"version"`
	ReviewID         string                    `json:"review_id"`
	JobID            string                    `json:"job_id"`
	Database         string                    `json:"database"`
	DocumentSHA256   string                    `json:"document_sha256"`
	ExtentSequence   uint64                    `json:"extent_sequence"`
	ExtentSHA256     string                    `json:"extent_sha256"`
	BatchSHA256      string                    `json:"batch_sha256"`
	PeerClaimSHA256s []string                  `json:"peer_claim_sha256s"`
	Author           string                    `json:"author"`
	Reviewer         string                    `json:"reviewer"`
	ProviderID       string                    `json:"provider_id"`
	Model            string                    `json:"model"`
	PromptVersion    string                    `json:"prompt_version"`
	PromptSHA256     string                    `json:"prompt_sha256"`
	ChallengeSHA256  string                    `json:"challenge_sha256"`
	InputSHA256      string                    `json:"input_sha256"`
	Status           string                    `json:"status"`
	Usage            ProviderUsage             `json:"usage"`
	Checks           []AssimilationReviewCheck `json:"checks"`
	SHA256           string                    `json:"sha256"`
	Replayed         bool                      `json:"-"`
}

type assimilationNumericEvidence struct {
	ID    string `json:"id"`
	Path  string `json:"path"`
	Value string `json:"value"`
}

type AssimilationReviewerDependencies struct {
	Provider   Provider
	Store      *AssimilationReviewStore
	Challenges AssimilationReviewChallengeSource
}

type AssimilationReviewerConfig struct {
	Reviewer        string
	ProviderID      string
	Model           string
	MaxOutputTokens uint64
	MaxPeerClaims   uint64
}

type AssimilationReviewer struct {
	mu              sync.Mutex
	provider        *ProviderGateway
	store           *AssimilationReviewStore
	challenges      AssimilationReviewChallengeSource
	reviewer        string
	providerID      string
	model           string
	maxOutputTokens uint64
	maxPeerClaims   uint64
	promptSHA256    string
}

func NewAssimilationReviewer(
	dependencies AssimilationReviewerDependencies,
	config AssimilationReviewerConfig,
) (*AssimilationReviewer, error) {
	if dependencies.Provider == nil || dependencies.Store == nil || dependencies.Challenges == nil ||
		!validWriteText(config.Reviewer, 160) || !validWriteText(config.ProviderID, 160) ||
		invalidProviderText(config.Model, 200, true) || config.MaxOutputTokens == 0 ||
		config.MaxOutputTokens > 16384 || config.MaxPeerClaims > 64 {
		return nil, ErrInvalidAssimilationReviewer
	}
	provider, err := NewProviderGateway(dependencies.Provider)
	if err != nil {
		return nil, ErrInvalidAssimilationReviewer
	}
	prompt := sha256.Sum256([]byte(AssimilationReviewSystemPrompt))
	return &AssimilationReviewer{
		provider: provider, store: dependencies.Store, challenges: dependencies.Challenges,
		reviewer: config.Reviewer, providerID: config.ProviderID, model: config.Model,
		maxOutputTokens: config.MaxOutputTokens, maxPeerClaims: config.MaxPeerClaims,
		promptSHA256: "sha256:" + hex.EncodeToString(prompt[:]),
	}, nil
}

func (reviewer *AssimilationReviewer) Review(
	ctx context.Context,
	request AssimilationReviewRequest,
) (AssimilationReviewArtifact, error) {
	if reviewer == nil || ctx == nil {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	normalized, author, inventories, inputSHA256, err := reviewer.normalizeRequest(request)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: normalize request", err)
	}
	if author == reviewer.reviewer {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: reviewer must differ from author", ErrInvalidAssimilationReview)
	}
	reviewer.mu.Lock()
	defer reviewer.mu.Unlock()
	if existing, readErr := reviewer.store.Read(normalized.Batch.JobID, normalized.Batch.ExtentSequence); readErr == nil {
		if existing.InputSHA256 != inputSHA256 {
			return AssimilationReviewArtifact{}, ErrAssimilationReviewConflict
		}
		existing.Replayed = true
		return existing, nil
	} else if !errors.Is(readErr, ErrAssimilationReviewNotFound) {
		return AssimilationReviewArtifact{}, readErr
	}
	challenge, err := reviewer.challenges.NextAssimilationReviewChallenge(ctx)
	if err != nil || !validAssimilationReviewChallenge(challenge) {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: challenge", ErrInvalidAssimilationReview)
	}
	providerRequest, err := reviewer.providerRequest(normalized, inventories, challenge)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: build provider request", err)
	}
	response, err := reviewer.provider.Complete(ctx, providerRequest)
	if err != nil {
		return AssimilationReviewArtifact{}, err
	}
	checks, err := reviewer.decodeReview(response, normalized.Batch, inventories, challenge)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: decode reviewer output", err)
	}
	challengeDigest := sha256.Sum256([]byte(challenge))
	peerSHA256s := make([]string, len(normalized.PeerClaims))
	for index, peer := range normalized.PeerClaims {
		peerSHA256s[index] = peer.SHA256
	}
	artifact := AssimilationReviewArtifact{
		Version:  AssimilationReviewArtifactVersion,
		ReviewID: assimilationReviewID(normalized.Batch.JobID, normalized.Batch.ExtentSequence),
		JobID:    normalized.Batch.JobID, Database: normalized.Batch.Database,
		DocumentSHA256: normalized.Batch.DocumentSHA256,
		ExtentSequence: normalized.Batch.ExtentSequence, ExtentSHA256: normalized.Batch.ExtentSHA256,
		BatchSHA256: normalized.Batch.SHA256, PeerClaimSHA256s: peerSHA256s,
		Author: author, Reviewer: reviewer.reviewer, ProviderID: reviewer.providerID, Model: reviewer.model,
		PromptVersion: AssimilationReviewPromptVersion, PromptSHA256: reviewer.promptSHA256,
		ChallengeSHA256: "sha256:" + hex.EncodeToString(challengeDigest[:]), InputSHA256: inputSHA256,
		Status: assimilationReviewStatus(checks), Usage: response.Usage, Checks: checks,
	}
	artifact, err = sealAssimilationReviewArtifact(artifact)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: seal artifact", err)
	}
	recorded, err := reviewer.store.record(artifact)
	if err != nil {
		return AssimilationReviewArtifact{}, fmt.Errorf("%w: persist artifact", err)
	}
	return recorded, nil
}

func (reviewer *AssimilationReviewer) normalizeRequest(
	request AssimilationReviewRequest,
) (AssimilationReviewRequest, string, map[string][]assimilationNumericEvidence, string, error) {
	if request.Batch.Validate() != nil || len(request.Batch.Claims) == 0 || request.Extent.Version != ReadExtentVersion ||
		request.Extent.SHA256 != request.Batch.ExtentSHA256 ||
		request.Extent.DocumentSHA256 != request.Batch.DocumentSHA256 ||
		request.Extent.Sequence != request.Batch.ExtentSequence ||
		uint64(len(request.PeerClaims)) > reviewer.maxPeerClaims {
		return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
	}
	extentDigest, err := readExtentDigest(request.Extent)
	if err != nil || extentDigest != request.Extent.SHA256 {
		return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
	}
	nodes := make(map[string]ReadExtentNode, len(request.Extent.Nodes))
	for _, node := range request.Extent.Nodes {
		nodes[node.NodeID] = node
	}
	author := ""
	claimIDs := make(map[string]struct{}, len(request.Batch.Claims)+len(request.PeerClaims))
	inventories := make(map[string][]assimilationNumericEvidence, len(request.Batch.Claims))
	for _, claim := range request.Batch.Claims {
		if author == "" {
			author = claim.Candidate.Mutation.Actor
		}
		if claim.Candidate.Mutation.Actor != author || !reviewClaimAnchorsMatch(claim, nodes) {
			return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
		}
		claimIDs[claim.ClaimID] = struct{}{}
		inventories[claim.ClaimID] = assimilationClaimNumericEvidence(claim)
	}
	peers := cloneDraftClaims(request.PeerClaims)
	for _, peer := range peers {
		if peer.Validate() != nil || peer.JobID != request.Batch.JobID || peer.Database != request.Batch.Database ||
			peer.DocumentSHA256 != request.Batch.DocumentSHA256 || peer.Candidate.Mutation.Actor != author {
			return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
		}
		if _, duplicate := claimIDs[peer.ClaimID]; duplicate {
			return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
		}
		claimIDs[peer.ClaimID] = struct{}{}
	}
	sort.Slice(peers, func(left, right int) bool { return peers[left].ClaimID < peers[right].ClaimID })
	normalized := AssimilationReviewRequest{
		Batch: cloneDraftClaimBatch(request.Batch), Extent: cloneReadExtent(request.Extent), PeerClaims: peers,
	}
	inputSHA256, err := reviewer.inputDigest(normalized)
	if err != nil {
		return AssimilationReviewRequest{}, "", nil, "", ErrInvalidAssimilationReview
	}
	return normalized, author, inventories, inputSHA256, nil
}

func (reviewer *AssimilationReviewer) inputDigest(request AssimilationReviewRequest) (string, error) {
	peerSHA256s := make([]string, len(request.PeerClaims))
	for index, peer := range request.PeerClaims {
		peerSHA256s[index] = peer.SHA256
	}
	payload := struct {
		BatchSHA256  string   `json:"batch_sha256"`
		ExtentSHA256 string   `json:"extent_sha256"`
		PeerSHA256s  []string `json:"peer_sha256s"`
		Reviewer     string   `json:"reviewer"`
		ProviderID   string   `json:"provider_id"`
		Model        string   `json:"model"`
		PromptSHA256 string   `json:"prompt_sha256"`
	}{
		BatchSHA256: request.Batch.SHA256, ExtentSHA256: request.Extent.SHA256,
		PeerSHA256s: peerSHA256s, Reviewer: reviewer.reviewer,
		ProviderID: reviewer.providerID, Model: reviewer.model, PromptSHA256: reviewer.promptSHA256,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (artifact AssimilationReviewArtifact) Validate() error {
	if artifact.Version != AssimilationReviewArtifactVersion ||
		!validSourceIntakeID(artifact.ReviewID, 200) || !validSourceIntakeID(artifact.JobID, 128) ||
		!validWriteText(artifact.Database, 200) || !validSourceSHA256(artifact.DocumentSHA256) ||
		artifact.ExtentSequence == 0 || !validSourceSHA256(artifact.ExtentSHA256) ||
		!validSourceSHA256(artifact.BatchSHA256) || artifact.PeerClaimSHA256s == nil ||
		!validWriteText(artifact.Author, 160) || !validWriteText(artifact.Reviewer, 160) ||
		artifact.Author == artifact.Reviewer || !validWriteText(artifact.ProviderID, 160) ||
		invalidProviderText(artifact.Model, 200, true) || artifact.PromptVersion != AssimilationReviewPromptVersion ||
		!validSourceSHA256(artifact.PromptSHA256) || !validSourceSHA256(artifact.ChallengeSHA256) ||
		!validSourceSHA256(artifact.InputSHA256) || !validProviderUsage(artifact.Usage) ||
		artifact.Checks == nil || len(artifact.Checks) == 0 || len(artifact.Checks) > 32 ||
		(artifact.Status != AssimilationReviewAccepted && artifact.Status != AssimilationReviewRejected) ||
		!validSourceSHA256(artifact.SHA256) {
		return ErrInvalidAssimilationReview
	}
	for _, digest := range artifact.PeerClaimSHA256s {
		if !validSourceSHA256(digest) {
			return ErrInvalidAssimilationReview
		}
	}
	for _, check := range artifact.Checks {
		if check.Validate() != nil {
			return ErrInvalidAssimilationReview
		}
	}
	if artifact.Status != assimilationReviewStatus(artifact.Checks) {
		return ErrInvalidAssimilationReview
	}
	digest, err := assimilationReviewArtifactDigest(artifact)
	if err != nil || digest != artifact.SHA256 {
		return ErrInvalidAssimilationReview
	}
	return nil
}

func (check AssimilationReviewCheck) Validate() error {
	if check.Version != AssimilationReviewCheckVersion || !validSourceIntakeID(check.ClaimID, 160) ||
		!validSourceSHA256(check.ClaimSHA256) || check.SourceNodeIDs == nil || len(check.SourceNodeIDs) == 0 ||
		len(check.SourceNodeIDs) > 32 || check.NumericEvidenceIDs == nil || check.FindingCodes == nil ||
		len(check.NumericEvidenceIDs) > 256 || len(check.FindingCodes) > 16 || !validAssimilationReviewDecision(check.Decision) {
		return ErrInvalidAssimilationReview
	}
	for _, value := range check.SourceNodeIDs {
		if !validSourceIntakeID(value, 160) {
			return ErrInvalidAssimilationReview
		}
	}
	for _, value := range check.NumericEvidenceIDs {
		if !validSourceSHA256(value) {
			return ErrInvalidAssimilationReview
		}
	}
	for _, value := range check.FindingCodes {
		if !validSourceIntakeID(value, 80) {
			return ErrInvalidAssimilationReview
		}
	}
	return nil
}

func sealAssimilationReviewArtifact(artifact AssimilationReviewArtifact) (AssimilationReviewArtifact, error) {
	artifact.SHA256 = ""
	digest, err := assimilationReviewArtifactDigest(artifact)
	if err != nil {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	artifact.SHA256 = digest
	if artifact.Validate() != nil {
		return AssimilationReviewArtifact{}, ErrInvalidAssimilationReview
	}
	return cloneAssimilationReviewArtifact(artifact), nil
}

func assimilationReviewArtifactDigest(artifact AssimilationReviewArtifact) (string, error) {
	artifact.SHA256 = ""
	artifact.Replayed = false
	encoded, err := json.Marshal(artifact)
	if err != nil || len(encoded) > maxAssimilationReviewArtifactBytes {
		return "", ErrInvalidAssimilationReview
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func assimilationReviewStatus(checks []AssimilationReviewCheck) string {
	for _, check := range checks {
		if check.Decision != AssimilationReviewDecisionAccept || !check.AnchorVerified ||
			!check.NumbersVerified || !check.ConflictFree || !check.NotRawCopy || len(check.FindingCodes) != 0 {
			return AssimilationReviewRejected
		}
	}
	return AssimilationReviewAccepted
}

func reviewClaimAnchorsMatch(claim DraftClaim, nodes map[string]ReadExtentNode) bool {
	for _, source := range claim.Sources {
		node, found := nodes[source.NodeID]
		if !found || !reflect.DeepEqual(node.Anchor, source.Anchor) {
			return false
		}
	}
	return true
}

var assimilationNumberPattern = regexp.MustCompile(`[-+]?[0-9]+(?:\.[0-9]+)?%?`)

func assimilationClaimNumericEvidence(claim DraftClaim) []assimilationNumericEvidence {
	var evidence []assimilationNumericEvidence
	keys := make([]string, 0, len(claim.Candidate.Parameters.Named))
	for key := range claim.Candidate.Parameters.Named {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		assimilationNumericValues(claim.ClaimID, "$named."+key, claim.Candidate.Parameters.Named[key], &evidence)
	}
	for index, value := range claim.Candidate.Parameters.Positional {
		assimilationNumericValues(claim.ClaimID, "$positional."+strconv.Itoa(index), value, &evidence)
	}
	return evidence
}

func assimilationNumericValues(claimID, path string, value any, target *[]assimilationNumericEvidence) {
	switch typed := value.(type) {
	case json.Number:
		appendAssimilationNumericEvidence(claimID, path, string(typed), target)
	case int:
		appendAssimilationNumericEvidence(claimID, path, strconv.Itoa(typed), target)
	case int8, int16, int32, int64:
		appendAssimilationNumericEvidence(claimID, path, fmt.Sprint(typed), target)
	case uint, uint8, uint16, uint32, uint64:
		appendAssimilationNumericEvidence(claimID, path, fmt.Sprint(typed), target)
	case float32:
		appendAssimilationNumericEvidence(claimID, path, strconv.FormatFloat(float64(typed), 'g', -1, 32), target)
	case float64:
		appendAssimilationNumericEvidence(claimID, path, strconv.FormatFloat(typed, 'g', -1, 64), target)
	case string:
		for index, token := range assimilationNumberPattern.FindAllString(typed, -1) {
			appendAssimilationNumericEvidence(claimID, path+"#"+strconv.Itoa(index), token, target)
		}
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			assimilationNumericValues(claimID, path+"."+key, typed[key], target)
		}
	case []any:
		for index, item := range typed {
			assimilationNumericValues(claimID, path+"."+strconv.Itoa(index), item, target)
		}
	}
}

func appendAssimilationNumericEvidence(claimID, path, value string, target *[]assimilationNumericEvidence) {
	digest := sha256.Sum256([]byte(claimID + "\x00" + path + "\x00" + value))
	*target = append(*target, assimilationNumericEvidence{
		ID: "sha256:" + hex.EncodeToString(digest[:]), Path: path, Value: value,
	})
}

func assimilationReviewID(jobID string, sequence uint64) string {
	digest := sha256.Sum256([]byte(jobID + "\x00" + strconv.FormatUint(sequence, 10)))
	return "review:sha256:" + hex.EncodeToString(digest[:])
}

func validAssimilationReviewChallenge(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func validAssimilationReviewDecision(value string) bool {
	return value == AssimilationReviewDecisionAccept || value == AssimilationReviewDecisionReject ||
		value == AssimilationReviewNeedsUser
}

func cloneAssimilationReviewArtifact(artifact AssimilationReviewArtifact) AssimilationReviewArtifact {
	artifact.PeerClaimSHA256s = cloneAssimilationReviewStrings(artifact.PeerClaimSHA256s)
	checks := artifact.Checks
	artifact.Checks = make([]AssimilationReviewCheck, len(checks))
	for index, check := range checks {
		artifact.Checks[index] = check
		artifact.Checks[index].SourceNodeIDs = cloneAssimilationReviewStrings(check.SourceNodeIDs)
		artifact.Checks[index].NumericEvidenceIDs = cloneAssimilationReviewStrings(check.NumericEvidenceIDs)
		artifact.Checks[index].FindingCodes = cloneAssimilationReviewStrings(check.FindingCodes)
	}
	return artifact
}

func cloneAssimilationReviewStrings(values []string) []string {
	if values == nil {
		return nil
	}
	return append(make([]string, 0, len(values)), values...)
}
