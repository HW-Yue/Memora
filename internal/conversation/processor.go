package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/skillwrite"
)

type Processor struct {
	journal *Journal
	tool    skillwrite.Tool
}

func New(journal *Journal, tool skillwrite.Tool) *Processor {
	return &Processor{journal: journal, tool: tool}
}

func (processor *Processor) Process(ctx context.Context, event Event) (Receipt, error) {
	if err := validateEvent(event); err != nil {
		return Receipt{}, err
	}
	if processor == nil || processor.journal == nil {
		return Receipt{}, conversationError(result.CodeInternal, "conversation processor is not configured")
	}
	digest := eventDigest(event)
	started, err := processor.journal.Start(ctx, event.EventID, digest)
	if err != nil {
		return Receipt{}, err
	}
	if started.Existing {
		if started.Processing {
			return Receipt{}, conversationError(result.CodeRevisionConflict, "event %q is in doubt after an interrupted attempt", event.EventID)
		}
		started.Receipt.Replayed = true
		return started.Receipt, nil
	}

	receipt := Receipt{
		Version: ReceiptVersion, EventID: event.EventID, SessionID: event.SessionID,
		Mutations: []skillwrite.Receipt{}, Missing: []string{},
	}
	switch event.Kind {
	case KindCheckpoint:
		checkpoint := StoredCheckpoint{
			Checkpoint: *event.Checkpoint, SessionID: event.SessionID,
			Workspace: event.Workspace, UpdatedEventID: event.EventID,
		}
		if err := processor.journal.SaveCheckpoint(ctx, checkpoint); err != nil {
			_ = processor.journal.Forget(ctx, event.EventID)
			return Receipt{}, err
		}
		receipt.Status = StatusCheckpointed
	case KindSessionEnd:
		if err := processor.journal.ClearCheckpoint(ctx, event.SessionID); err != nil {
			_ = processor.journal.Forget(ctx, event.EventID)
			return Receipt{}, err
		}
		receipt.Status = StatusEnded
	case KindDelta:
		for _, delta := range event.Deltas {
			if delta.Disposition == DispositionIgnore {
				receipt.Ignored++
				continue
			}
			if delta.Database == "" {
				receipt.Missing = append(receipt.Missing, "delta "+delta.ID+": database")
			}
			if delta.Plan == nil {
				receipt.Missing = append(receipt.Missing, "delta "+delta.ID+": mutation_plan")
			}
			if len(receipt.Missing) > 0 {
				continue
			}
			if processor.tool == nil {
				_ = processor.journal.Forget(ctx, event.EventID)
				return Receipt{}, conversationError(result.CodeInternal, "mutation tool is not configured")
			}
			report, runErr := skillwrite.New(processor.tool).Run(ctx, *delta.Plan)
			if runErr != nil {
				_ = processor.journal.Forget(ctx, event.EventID)
				return Receipt{}, runErr
			}
			receipt.Mutations = append(receipt.Mutations, report.Receipt)
		}
		if len(receipt.Missing) > 0 {
			receipt.Status = StatusNeedsContext
		} else {
			receipt.Status = StatusProcessed
		}
	}
	if err := processor.journal.Complete(ctx, event.EventID, digest, receipt); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func validateEvent(event Event) error {
	if event.Version != EventVersion {
		return conversationError(result.CodeValidation, "event version = %q, want %q", event.Version, EventVersion)
	}
	if blank(event.EventID) || blank(event.SessionID) || blank(event.Workspace) || len(event.AuthorizedDatabases) == 0 {
		return conversationError(result.CodeValidation, "event identity, workspace, and authorized databases are required")
	}
	allowed := map[string]bool{}
	for _, database := range event.AuthorizedDatabases {
		if blank(database) {
			return conversationError(result.CodeValidation, "authorized database cannot be empty")
		}
		allowed[canonical(database)] = true
	}
	switch event.Kind {
	case KindDelta:
		if len(event.Deltas) == 0 || event.Checkpoint != nil {
			return conversationError(result.CodeValidation, "delta event requires deltas and no checkpoint")
		}
		seen := map[string]bool{}
		persisted := 0
		for _, delta := range event.Deltas {
			if blank(delta.ID) || seen[delta.ID] {
				return conversationError(result.CodeValidation, "delta ids must be non-empty and unique")
			}
			seen[delta.ID] = true
			switch delta.Disposition {
			case DispositionIgnore:
				if blank(delta.Reason) || delta.Plan != nil {
					return conversationError(result.CodeValidation, "ignored delta %q requires a reason and no plan", delta.ID)
				}
			case DispositionPersist:
				persisted++
				if delta.Database != "" && !allowed[canonical(delta.Database)] {
					return conversationError(result.CodePermissionDenied, "delta %q targets unauthorized database %q", delta.ID, delta.Database)
				}
				if delta.Plan != nil {
					if delta.Plan.SourceEventID != event.EventID ||
						(delta.Database != "" && !strings.EqualFold(delta.Plan.Database, delta.Database)) {
						return conversationError(result.CodeValidation, "delta %q plan provenance or database does not match the event", delta.ID)
					}
					for _, database := range delta.Plan.AuthorizedDatabases {
						if !allowed[canonical(database)] {
							return conversationError(result.CodePermissionDenied, "delta %q plan expands database authorization", delta.ID)
						}
					}
				}
			default:
				return conversationError(result.CodeValidation, "delta %q has unsupported disposition %q", delta.ID, delta.Disposition)
			}
		}
		if persisted > 1 {
			return conversationError(result.CodeValidation, "one conversation event may persist at most one atomic semantic delta")
		}
	case KindCheckpoint:
		if len(event.Deltas) != 0 || event.Checkpoint == nil || blank(event.Checkpoint.ActiveDatabase) || blank(event.Checkpoint.LastEventID) {
			return conversationError(result.CodeValidation, "checkpoint event requires compact checkpoint context only")
		}
		if !allowed[canonical(event.Checkpoint.ActiveDatabase)] {
			return conversationError(result.CodePermissionDenied, "checkpoint database is outside authorized scope")
		}
	case KindSessionEnd:
		if len(event.Deltas) != 0 || event.Checkpoint != nil {
			return conversationError(result.CodeValidation, "session_end cannot carry deltas or checkpoint")
		}
	default:
		return conversationError(result.CodeValidation, "unsupported event kind %q", event.Kind)
	}
	return nil
}

func eventDigest(event Event) string {
	encoded, _ := json.Marshal(event)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func blank(value string) bool       { return strings.TrimSpace(value) == "" }
func canonical(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
