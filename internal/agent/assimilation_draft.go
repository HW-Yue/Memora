package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	AssimilationDraftPromptVersion  = "memora.assimilation-draft-prompt/v1"
	AssimilationDraftContextVersion = "memora.assimilation-draft-context/v1"
	AssimilationDraftToolName       = "propose_assimilation_claims"
	AssimilationDraftSystemPrompt   = "Memora assimilation drafter v1. Read the complete bounded ReadExtent and the navigation-only Bootstrap Frame. Return every independently useful semantic claim through the required propose_assimilation_claims tool. Bind every claim to one or more supplied source_node_ids and propose one parameterized, same-Database L1 MSQL mutation with explicit schema, revision, affected-row and Route guards. Do not invent anchors, create Schema, submit writes, copy arbitrary chunks, or answer directly. An empty claims array is valid when the extent has no durable semantic claim."
	maxAssimilationDraftToolBytes   = 1 << 20
)

var (
	ErrInvalidAssimilationDrafterDependencies = errors.New("Assimilation Drafter dependencies are invalid")
	ErrInvalidAssimilationDraftRequest        = errors.New("Assimilation Draft request is invalid")
	ErrInvalidAssimilationDraftToolCall       = errors.New("Assimilation Draft tool call is invalid")
)

type AssimilationDrafterDependencies struct {
	MSQL     MSQLExecutor
	Provider Provider
	Ledger   *DraftClaimLedger
}

type AssimilationDrafterConfig struct {
	ProviderID         string
	Model              string
	WriteProfile       WriteProfile
	BootstrapBudget    BootstrapBudget
	BootstrapProfile   BootstrapProfile
	MaxExtentUTF8Bytes uint64
	MaxOutputTokens    uint64
	MaxClaims          uint64
}

type AssimilationDraftRequest struct {
	JobID         string     `json:"job_id"`
	Database      string     `json:"database"`
	SourceID      string     `json:"source_id"`
	SourceLocator string     `json:"source_locator"`
	SourceSHA256  string     `json:"source_sha256"`
	Extent        ReadExtent `json:"extent"`
}

type AssimilationDrafter struct {
	mu               sync.Mutex
	bootstrap        *BootstrapAssembler
	provider         *ProviderGateway
	ledger           *DraftClaimLedger
	providerID       string
	model            string
	profile          WriteProfile
	bootstrapBudget  BootstrapBudget
	bootstrapProfile BootstrapProfile
	maxExtentBytes   uint64
	maxOutputTokens  uint64
	maxClaims        uint64
	promptSHA256     string
}

func NewAssimilationDrafter(
	dependencies AssimilationDrafterDependencies,
	config AssimilationDrafterConfig,
) (*AssimilationDrafter, error) {
	if dependencies.MSQL == nil || dependencies.Provider == nil || dependencies.Ledger == nil ||
		!validWriteText(config.ProviderID, 160) || invalidProviderText(config.Model, 200, true) ||
		!validWriteProfile(config.WriteProfile) || config.MaxClaims == 0 || config.MaxClaims > 32 ||
		config.MaxClaims > config.WriteProfile.MaxStatements || config.MaxExtentUTF8Bytes < 1024 ||
		config.MaxExtentUTF8Bytes > MaxReadExtentUTF8Bytes || config.MaxOutputTokens == 0 ||
		config.MaxOutputTokens > 16384 {
		return nil, ErrInvalidAssimilationDrafterDependencies
	}
	runtime, err := New(Dependencies{MSQL: dependencies.MSQL})
	if err != nil {
		return nil, ErrInvalidAssimilationDrafterDependencies
	}
	bootstrap, err := NewBootstrapAssembler(runtime)
	if err != nil {
		return nil, ErrInvalidAssimilationDrafterDependencies
	}
	provider, err := NewProviderGateway(dependencies.Provider)
	if err != nil {
		return nil, ErrInvalidAssimilationDrafterDependencies
	}
	profile, valid := ResolveBootstrapProfile(config.BootstrapProfile)
	if !valid || validateBootstrapRequest(context.Background(), BootstrapRequest{
		Query:         "assimilation-draft-construction-probe",
		Authorization: shortTextReadAuthorization(config.WriteProfile),
		Budget:        config.BootstrapBudget, Profile: profile,
	}) != nil {
		return nil, ErrInvalidAssimilationDrafterDependencies
	}
	promptDigest := sha256.Sum256([]byte(AssimilationDraftSystemPrompt))
	return &AssimilationDrafter{
		bootstrap: bootstrap, provider: provider, ledger: dependencies.Ledger,
		providerID: config.ProviderID, model: config.Model, profile: cloneWriteProfile(config.WriteProfile),
		bootstrapBudget: config.BootstrapBudget, bootstrapProfile: profile,
		maxExtentBytes: config.MaxExtentUTF8Bytes, maxOutputTokens: config.MaxOutputTokens,
		maxClaims:    config.MaxClaims,
		promptSHA256: "sha256:" + hex.EncodeToString(promptDigest[:]),
	}, nil
}

func (drafter *AssimilationDrafter) Draft(
	ctx context.Context,
	request AssimilationDraftRequest,
) (DraftClaimReceipt, error) {
	if drafter == nil || ctx == nil || !drafter.validRequest(request) {
		return DraftClaimReceipt{}, ErrInvalidAssimilationDraftRequest
	}
	if err := ctx.Err(); err != nil {
		return DraftClaimReceipt{}, err
	}
	drafter.mu.Lock()
	defer drafter.mu.Unlock()

	if existing, err := drafter.ledger.ReadExtent(request.JobID, request.Extent.Sequence); err == nil {
		if existing.ExtentSHA256 != request.Extent.SHA256 || existing.DocumentSHA256 != request.Extent.DocumentSHA256 ||
			!strings.EqualFold(existing.Database, request.Database) {
			return DraftClaimReceipt{}, ErrDraftClaimConflict
		}
		return draftClaimReceipt(existing, true), nil
	} else if !errors.Is(err, ErrDraftClaimNotFound) {
		return DraftClaimReceipt{}, err
	}

	frame, err := drafter.bootstrap.Assemble(ctx, BootstrapRequest{
		Query:         assimilationDraftNavigationQuery(request.Extent),
		Authorization: shortTextReadAuthorization(drafter.profile),
		Budget:        drafter.bootstrapBudget, Profile: drafter.bootstrapProfile,
	})
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	providerRequest, err := drafter.providerRequest(request, frame)
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	response, err := drafter.provider.Complete(ctx, providerRequest)
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	arguments, err := drafter.decodeToolResponse(response, request.Extent)
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	batch, err := drafter.buildBatch(request, response.Usage, arguments)
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	receipt, err := drafter.ledger.record(batch)
	if err != nil {
		return DraftClaimReceipt{}, err
	}
	if receipt.Validate() != nil {
		return DraftClaimReceipt{}, ErrDraftClaimInvalid
	}
	return receipt, nil
}

func (drafter *AssimilationDrafter) validRequest(request AssimilationDraftRequest) bool {
	if !validSourceIntakeID(request.JobID, 128) || !validShortIdentifier(request.Database) ||
		!validWriteText(request.SourceID, 1000) || !validWriteText(request.SourceLocator, 2048) ||
		!validSourceSHA256(request.SourceSHA256) || !drafter.databaseAllowed(request.Database) ||
		request.Extent.Version != ReadExtentVersion || request.Extent.Sequence == 0 ||
		!validSourceSHA256(request.Extent.DocumentSHA256) || len(request.Extent.Nodes) == 0 ||
		len(request.Extent.Nodes) > MaxReadExtentNodes || request.Extent.StartPosition >= request.Extent.EndPosition ||
		request.Extent.EndPosition-request.Extent.StartPosition != uint64(len(request.Extent.Nodes)) ||
		!validSourceSHA256(request.Extent.SHA256) {
		return false
	}
	digest, err := readExtentDigest(request.Extent)
	if err != nil || digest != request.Extent.SHA256 {
		return false
	}
	seen := make(map[string]struct{}, len(request.Extent.Nodes))
	var extentBytes uint64
	for _, node := range request.Extent.Nodes {
		if !validSourceIntakeID(node.NodeID, 160) || !validDocumentNodeKind(node.Kind) ||
			!validDocumentText(node.Label, 1000, false) || !utf8.ValidString(node.Text) ||
			!validDraftClaimAnchor(node.Anchor) {
			return false
		}
		if _, duplicate := seen[node.NodeID]; duplicate {
			return false
		}
		seen[node.NodeID] = struct{}{}
		extentBytes += uint64(readExtentNodeBytes(node))
	}
	return extentBytes <= drafter.maxExtentBytes
}

func (drafter *AssimilationDrafter) databaseAllowed(database string) bool {
	for _, allowed := range drafter.profile.AuthorizedDatabases {
		if strings.EqualFold(strings.TrimSpace(allowed), strings.TrimSpace(database)) {
			return true
		}
	}
	return false
}

func (drafter *AssimilationDrafter) buildBatch(
	request AssimilationDraftRequest,
	usage ProviderUsage,
	arguments assimilationDraftToolArguments,
) (DraftClaimBatch, error) {
	nodes := make(map[string]ReadExtentNode, len(request.Extent.Nodes))
	for _, node := range request.Extent.Nodes {
		nodes[node.NodeID] = node
	}
	claims := make([]DraftClaim, len(arguments.Claims))
	for index, argument := range arguments.Claims {
		sources := make([]DraftClaimSource, len(argument.SourceNodeIDs))
		for sourceIndex, nodeID := range argument.SourceNodeIDs {
			node := nodes[nodeID]
			sources[sourceIndex] = DraftClaimSource{NodeID: nodeID, Anchor: node.Anchor}
		}
		claim, err := sealDraftClaim(DraftClaim{
			Version: DraftClaimVersion, ClaimID: argument.ClaimID, Status: DraftClaimReviewRequired,
			JobID:    request.JobID,
			Database: request.Database, DocumentSHA256: request.Extent.DocumentSHA256,
			ExtentSequence: request.Extent.Sequence, ExtentSHA256: request.Extent.SHA256,
			Sources: sources, ProviderID: drafter.providerID, Model: drafter.model,
			PromptVersion: AssimilationDraftPromptVersion, PromptSHA256: drafter.promptSHA256,
			Candidate: protocolmsql.AssimilationStatement{
				MSQL: argument.MSQL,
				Parameters: protocolmsql.Parameters{
					Named: argument.Named, Positional: argument.Positional,
				},
				Mutation: protocolmsql.MutationOptions{
					ExpectedSchemaVersion: argument.ExpectedSchemaVersion,
					ExpectedRevision:      argument.ExpectedRevision, SourceRevisions: argument.SourceRevisions,
					MaxAffectedRows: argument.MaxAffectedRows, Actor: drafter.profile.Actor,
					Source: request.SourceID, Reason: argument.Reason,
					SourceKind: protocolmsql.SourceDocumentAnchor, SourceLocator: request.SourceLocator,
					SourceContentHash: request.SourceSHA256,
					RouteLeafIDs:      argument.RouteLeafIDs, TargetRouteLeafIDs: argument.TargetRouteLeafIDs,
					RelationTargetOrdinals: argument.RelationTargetOrdinals, RouteUpdates: argument.RouteUpdates,
				},
			},
		})
		if err != nil {
			return DraftClaimBatch{}, fmt.Errorf("%w: claim %d", ErrInvalidAssimilationDraftToolCall, index)
		}
		claims[index] = claim
	}
	batch, err := sealDraftClaimBatch(DraftClaimBatch{
		Version: DraftClaimBatchVersion, JobID: request.JobID, Database: request.Database,
		DocumentSHA256: request.Extent.DocumentSHA256, ExtentSequence: request.Extent.Sequence,
		ExtentSHA256: request.Extent.SHA256, ProviderID: drafter.providerID, Model: drafter.model,
		PromptVersion: AssimilationDraftPromptVersion, PromptSHA256: drafter.promptSHA256,
		Usage: usage, Claims: claims,
	})
	if err != nil {
		return DraftClaimBatch{}, ErrInvalidAssimilationDraftToolCall
	}
	return batch, nil
}

func assimilationDraftNavigationQuery(extent ReadExtent) string {
	var builder strings.Builder
	runes := 0
	for _, node := range extent.Nodes {
		for _, text := range []string{node.Label, node.Text} {
			text = strings.TrimSpace(text)
			if text == "" {
				continue
			}
			if builder.Len() > 0 {
				if runes == 256 {
					return builder.String()
				}
				builder.WriteByte(' ')
				runes++
			}
			for _, character := range text {
				if runes == 256 {
					return builder.String()
				}
				builder.WriteRune(character)
				runes++
			}
		}
	}
	if builder.Len() == 0 {
		return fmt.Sprintf("document extent %d", extent.Sequence)
	}
	return builder.String()
}
