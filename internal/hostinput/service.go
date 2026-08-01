package hostinput

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

const inputBucket = "host_input.pending.v1"

type Clock interface{ Now() time.Time }

type Options struct {
	Clock          Clock
	MaximumPending int
}

type Service struct {
	store          store.Store
	clock          Clock
	maximumPending int
}

var captureMu sync.Mutex

type record struct {
	Digest    string  `json:"digest"`
	Candidate Input   `json:"candidate"`
	Receipt   Receipt `json:"receipt"`
}

type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

func New(database store.Store, options Options) *Service {
	if options.Clock == nil {
		options.Clock = systemClock{}
	}
	if options.MaximumPending < 1 || options.MaximumPending > MaximumPendingInputs {
		options.MaximumPending = MaximumPendingInputs
	}
	return &Service{store: database, clock: options.Clock, maximumPending: options.MaximumPending}
}

func (service *Service) Capture(ctx context.Context, input Input) (Receipt, error) {
	if service == nil || service.store == nil || service.clock == nil {
		return Receipt{}, inputError(result.CodeInternal, "capture service is not configured")
	}
	input = normalize(input)
	if err := validate(input); err != nil {
		return Receipt{}, err
	}
	digest, err := hashJSON(input)
	if err != nil {
		return Receipt{}, inputError(result.CodeInternal, "candidate could not be hashed")
	}
	captureMu.Lock()
	defer captureMu.Unlock()
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return Receipt{}, inputError(result.CodeInternal, "begin capture: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	if encoded, getErr := tx.Get(ctx, inputBucket, input.InputID); getErr == nil {
		current, decodeErr := decodeRecord(encoded)
		if decodeErr != nil {
			return Receipt{}, decodeErr
		}
		if current.Digest != digest {
			return Receipt{}, inputError(result.CodeRevisionConflict, "input_id %q was already used by different content", input.InputID)
		}
		receipt := current.Receipt
		receipt.Replayed = true
		return receipt, nil
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return Receipt{}, inputError(result.CodeInternal, "read existing capture: %v", getErr)
	}
	entries, err := tx.Scan(ctx, inputBucket)
	if err != nil {
		return Receipt{}, inputError(result.CodeInternal, "scan pending input inbox: %v", err)
	}
	if len(entries) >= service.maximumPending {
		return Receipt{}, inputError(result.CodeConstraint, "pending input inbox reached %d entries", service.maximumPending)
	}
	capturedAt := service.clock.Now().UTC()
	if capturedAt.IsZero() {
		return Receipt{}, inputError(result.CodeInternal, "capture clock returned zero time")
	}
	contentHash := hashBytes([]byte(input.CandidateText))
	scopeHash, err := hashJSON(input.AuthorizedDatabases)
	if err != nil {
		return Receipt{}, inputError(result.CodeInternal, "Database scope could not be hashed")
	}
	receipt := Receipt{Version: ReceiptVersion, InputID: input.InputID, Workspace: input.Workspace,
		Status: StatusPending, InputSHA256: digest, ContentSHA256: contentHash, ScopeSHA256: scopeHash,
		ContentBytes: len(input.CandidateText), AuthorizedDatabases: append([]string{}, input.AuthorizedDatabases...),
		CapturedAt: capturedAt, Replayed: false}
	encoded, err := json.Marshal(record{Digest: digest, Candidate: input, Receipt: receipt})
	if err != nil {
		return Receipt{}, inputError(result.CodeInternal, "encode capture: %v", err)
	}
	if err := tx.Put(ctx, inputBucket, input.InputID, encoded); err != nil {
		return Receipt{}, inputError(result.CodeInternal, "write capture: %v", err)
	}
	if err := tx.Commit(); err != nil {
		return Receipt{}, inputError(result.CodeInternal, "commit capture: %v", err)
	}
	return receipt, nil
}

func (service *Service) Get(ctx context.Context, inputID, workspace string) (Input, Receipt, error) {
	if service == nil || service.store == nil {
		return Input{}, Receipt{}, inputError(result.CodeInternal, "capture service is not configured")
	}
	inputID, workspace = strings.TrimSpace(inputID), strings.TrimSpace(workspace)
	if !validText(inputID, 200) || !validText(workspace, 500) {
		return Input{}, Receipt{}, inputError(result.CodeValidation, "bounded input ID and workspace are required")
	}
	tx, err := service.store.Begin(ctx, store.ReadOnly)
	if err != nil {
		return Input{}, Receipt{}, inputError(result.CodeInternal, "begin capture read: %v", err)
	}
	defer func() { _ = tx.Rollback() }()
	encoded, err := tx.Get(ctx, inputBucket, inputID)
	if errors.Is(err, store.ErrNotFound) {
		return Input{}, Receipt{}, inputError(result.CodeNotFound, "input %q was not found", inputID)
	}
	if err != nil {
		return Input{}, Receipt{}, inputError(result.CodeInternal, "read capture: %v", err)
	}
	current, err := decodeRecord(encoded)
	if err != nil {
		return Input{}, Receipt{}, err
	}
	if current.Candidate.Workspace != workspace {
		return Input{}, Receipt{}, inputError(result.CodePermissionDenied, "input workspace does not match the request")
	}
	return current.Candidate, current.Receipt, nil
}

func normalize(input Input) Input {
	input.InputID, input.Workspace, input.Actor = strings.TrimSpace(input.InputID), strings.TrimSpace(input.Workspace), strings.TrimSpace(input.Actor)
	input.CandidateText = strings.TrimSpace(input.CandidateText)
	input.Source.Title, input.Source.Locator = strings.TrimSpace(input.Source.Title), strings.TrimSpace(input.Source.Locator)
	input.Source.ContentSHA256 = strings.ToLower(strings.TrimSpace(input.Source.ContentSHA256))
	input.AuthorizedDatabases = append([]string{}, input.AuthorizedDatabases...)
	for index := range input.AuthorizedDatabases {
		input.AuthorizedDatabases[index] = strings.TrimSpace(input.AuthorizedDatabases[index])
	}
	sort.Slice(input.AuthorizedDatabases, func(i, j int) bool {
		return strings.ToLower(input.AuthorizedDatabases[i]) < strings.ToLower(input.AuthorizedDatabases[j])
	})
	return input
}

func validate(input Input) error {
	if input.Version != InputVersion || !validText(input.InputID, 200) || !validText(input.Workspace, 500) ||
		!validText(input.Actor, 160) || !validText(input.Source.Title, 200) {
		return inputError(result.CodeValidation, "input identity, workspace, actor, and source title are required and bounded")
	}
	if input.CandidateText == "" || !validCandidate(input.CandidateText) {
		if len(input.CandidateText) > MaximumCandidateBytes {
			return inputError(result.CodeOutputTruncated, "candidate text exceeds %d UTF-8 bytes", MaximumCandidateBytes)
		}
		return inputError(result.CodeValidation, "candidate text is invalid")
	}
	if len(input.AuthorizedDatabases) == 0 || len(input.AuthorizedDatabases) > 32 {
		return inputError(result.CodePermissionDenied, "input requires 1 to 32 authorized Database selectors")
	}
	seen := map[string]bool{}
	for _, database := range input.AuthorizedDatabases {
		key := strings.ToLower(database)
		if !validText(database, 200) || seen[key] {
			return inputError(result.CodeValidation, "authorized Database selectors are invalid or duplicated")
		}
		seen[key] = true
	}
	switch input.Source.Kind {
	case history.SourceConversationAssertion:
		if input.Source.Locator != "" || input.Source.ContentSHA256 != "" {
			return inputError(result.CodeValidation, "conversation assertion cannot claim an anchored source")
		}
	case history.SourceDocumentAnchor, history.SourceRepositoryAnchor:
		if !validText(input.Source.Locator, 1000) || !validSHA256(input.Source.ContentSHA256) {
			return inputError(result.CodeValidation, "anchored source requires a bounded locator and SHA-256")
		}
	default:
		return inputError(result.CodeValidation, "source kind cannot be captured")
	}
	return nil
}

func validCandidate(value string) bool {
	if !utf8.ValidString(value) || len(value) > MaximumCandidateBytes {
		return false
	}
	for _, character := range value {
		if (character < 0x20 && character != '\n' && character != '\r' && character != '\t') || character == 0x7f {
			return false
		}
	}
	return true
}

func validText(value string, maximumRunes int) bool {
	if value == "" || !utf8.ValidString(value) || utf8.RuneCountInString(value) > maximumRunes {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validSHA256(value string) bool {
	value = strings.TrimPrefix(value, "sha256:")
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func hashJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return hashBytes(encoded), nil
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func decodeRecord(encoded []byte) (record, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value record
	if err := decoder.Decode(&value); err != nil || decoder.Decode(&struct{}{}) != io.EOF || validate(value.Candidate) != nil {
		return record{}, inputError(result.CodeInternal, "stored input record is invalid")
	}
	want, err := hashJSON(value.Candidate)
	wantContent := hashBytes([]byte(value.Candidate.CandidateText))
	wantScope, scopeErr := hashJSON(value.Candidate.AuthorizedDatabases)
	if err != nil || want != value.Digest || value.Receipt.InputSHA256 != value.Digest ||
		scopeErr != nil || value.Receipt.ContentSHA256 != wantContent || value.Receipt.ScopeSHA256 != wantScope ||
		value.Receipt.Version != ReceiptVersion || value.Receipt.Status != StatusPending || value.Receipt.Replayed ||
		value.Receipt.InputID != value.Candidate.InputID || value.Receipt.Workspace != value.Candidate.Workspace ||
		value.Receipt.ContentBytes != len(value.Candidate.CandidateText) || value.Receipt.CapturedAt.IsZero() ||
		!sameStrings(value.Receipt.AuthorizedDatabases, value.Candidate.AuthorizedDatabases) {
		return record{}, inputError(result.CodeInternal, "stored input record integrity check failed")
	}
	return value, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
