package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	eventBucket      = "conversation.events.v1"
	checkpointBucket = "conversation.checkpoints.v1"
)

type eventStatus string

const (
	eventProcessing eventStatus = "processing"
	eventCompleted  eventStatus = "completed"
)

type eventRecord struct {
	Digest  string          `json:"digest"`
	Status  eventStatus     `json:"status"`
	Receipt json.RawMessage `json:"receipt,omitempty"`
}

type StartResult struct {
	Existing   bool
	Processing bool
	Receipt    Receipt
}

type Journal struct{ store store.Store }

func NewJournal(database store.Store) *Journal { return &Journal{store: database} }

func (journal *Journal) Start(ctx context.Context, eventID, digest string) (StartResult, error) {
	if journal == nil || journal.store == nil {
		return StartResult{}, conversationError(result.CodeInternal, "event journal is not configured")
	}
	tx, err := journal.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return StartResult{}, conversationError(result.CodeInternal, "begin event journal: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, eventBucket, eventID)
	if err == nil {
		var record eventRecord
		if decodeErr := json.Unmarshal(encoded, &record); decodeErr != nil {
			return StartResult{}, conversationError(result.CodeInternal, "decode event record: %v", decodeErr)
		}
		if record.Digest != digest {
			return StartResult{}, conversationError(result.CodeRevisionConflict, "event_id %q was already used by different content", eventID)
		}
		if record.Status == eventProcessing {
			return StartResult{Existing: true, Processing: true}, nil
		}
		var receipt Receipt
		if decodeErr := json.Unmarshal(record.Receipt, &receipt); decodeErr != nil {
			return StartResult{}, conversationError(result.CodeInternal, "decode event receipt: %v", decodeErr)
		}
		return StartResult{Existing: true, Receipt: receipt}, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return StartResult{}, conversationError(result.CodeInternal, "read event journal: %v", err)
	}
	record := eventRecord{Digest: digest, Status: eventProcessing}
	encoded, _ = json.Marshal(record)
	if err := tx.Put(ctx, eventBucket, eventID, encoded); err != nil {
		return StartResult{}, conversationError(result.CodeInternal, "reserve event: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return StartResult{}, conversationError(result.CodeInternal, "commit event reservation: %v", err)
	}
	return StartResult{}, nil
}

func (journal *Journal) Complete(ctx context.Context, eventID, digest string, receipt Receipt) error {
	receipt.Replayed = false
	receiptBytes, err := json.Marshal(receipt)
	if err != nil {
		return conversationError(result.CodeInternal, "encode event receipt: %v", err)
	}
	recordBytes, _ := json.Marshal(eventRecord{Digest: digest, Status: eventCompleted, Receipt: receiptBytes})
	return journal.write(ctx, eventBucket, eventID, recordBytes, "complete event")
}

func (journal *Journal) Forget(ctx context.Context, eventID string) error {
	if journal == nil || journal.store == nil {
		return nil
	}
	tx, err := journal.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Delete(ctx, eventBucket, eventID); err != nil {
		return err
	}
	return tx.Commit()
}

func (journal *Journal) SaveCheckpoint(ctx context.Context, checkpoint StoredCheckpoint) error {
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return conversationError(result.CodeInternal, "encode checkpoint: %v", err)
	}
	return journal.write(ctx, checkpointBucket, checkpoint.SessionID, encoded, "save checkpoint")
}

func (journal *Journal) LoadCheckpoint(ctx context.Context, sessionID string) (StoredCheckpoint, bool, error) {
	if journal == nil || journal.store == nil {
		return StoredCheckpoint{}, false, conversationError(result.CodeInternal, "event journal is not configured")
	}
	tx, err := journal.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return StoredCheckpoint{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, checkpointBucket, sessionID)
	if errors.Is(err, store.ErrNotFound) {
		return StoredCheckpoint{}, false, nil
	}
	if err != nil {
		return StoredCheckpoint{}, false, err
	}
	var checkpoint StoredCheckpoint
	if err := json.Unmarshal(encoded, &checkpoint); err != nil {
		return StoredCheckpoint{}, false, fmt.Errorf("decode conversation checkpoint: %w", err)
	}
	return checkpoint, true, nil
}

func (journal *Journal) ClearCheckpoint(ctx context.Context, sessionID string) error {
	if journal == nil || journal.store == nil {
		return conversationError(result.CodeInternal, "event journal is not configured")
	}
	tx, err := journal.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Delete(ctx, checkpointBucket, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

func (journal *Journal) write(ctx context.Context, bucket, key string, value []byte, operation string) error {
	if journal == nil || journal.store == nil {
		return conversationError(result.CodeInternal, "event journal is not configured")
	}
	tx, err := journal.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return conversationError(result.CodeInternal, "%s: %v", operation, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := tx.Put(ctx, bucket, key, value); err != nil {
		return conversationError(result.CodeInternal, "%s: %v", operation, err)
	}
	if err := tx.Commit(); err != nil {
		return conversationError(result.CodeInternal, "%s: %v", operation, err)
	}
	return nil
}
