package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const (
	DraftClaimVersion           = "memora.assimilation-draft-claim/v1"
	DraftClaimBatchVersion      = "memora.assimilation-draft-batch/v1"
	DraftClaimReceiptVersion    = "memora.assimilation-draft-receipt/v1"
	DraftClaimReviewRequired    = "review_required"
	draftClaimRecordVersion     = "memora.assimilation-draft-record/v1"
	draftClaimJournalSuffix     = ".claims"
	maxDraftClaimJournalBytes   = 128 << 20
	maxDraftClaimRecordBytes    = 4 << 20
	maxDraftClaimCandidateBytes = 512 << 10
)

var (
	ErrDraftClaimInvalid       = errors.New("draft claim is invalid")
	ErrDraftClaimNotFound      = errors.New("draft claim extent was not found")
	ErrDraftClaimConflict      = errors.New("draft claim extent conflicts with the ledger")
	ErrDraftClaimLedgerCorrupt = errors.New("draft claim ledger is corrupt")
)

type DraftClaimSource struct {
	NodeID string         `json:"node_id"`
	Anchor DocumentAnchor `json:"anchor"`
}

type DraftClaim struct {
	Version        string                             `json:"version"`
	ClaimID        string                             `json:"claim_id"`
	Status         string                             `json:"status"`
	JobID          string                             `json:"job_id"`
	Database       string                             `json:"database"`
	DocumentSHA256 string                             `json:"document_sha256"`
	ExtentSequence uint64                             `json:"extent_sequence"`
	ExtentSHA256   string                             `json:"extent_sha256"`
	Sources        []DraftClaimSource                 `json:"sources"`
	ProviderID     string                             `json:"provider_id"`
	Model          string                             `json:"model"`
	PromptVersion  string                             `json:"prompt_version"`
	PromptSHA256   string                             `json:"prompt_sha256"`
	Candidate      protocolmsql.AssimilationStatement `json:"candidate"`
	SHA256         string                             `json:"sha256"`
}

type DraftClaimBatch struct {
	Version        string        `json:"version"`
	JobID          string        `json:"job_id"`
	Database       string        `json:"database"`
	DocumentSHA256 string        `json:"document_sha256"`
	ExtentSequence uint64        `json:"extent_sequence"`
	ExtentSHA256   string        `json:"extent_sha256"`
	ProviderID     string        `json:"provider_id"`
	Model          string        `json:"model"`
	PromptVersion  string        `json:"prompt_version"`
	PromptSHA256   string        `json:"prompt_sha256"`
	Usage          ProviderUsage `json:"usage"`
	Claims         []DraftClaim  `json:"claims"`
	SHA256         string        `json:"sha256"`
}

type DraftClaimReceipt struct {
	Version        string        `json:"version"`
	JobID          string        `json:"job_id"`
	Database       string        `json:"database"`
	DocumentSHA256 string        `json:"document_sha256"`
	ExtentSequence uint64        `json:"extent_sequence"`
	ExtentSHA256   string        `json:"extent_sha256"`
	ProviderID     string        `json:"provider_id"`
	Model          string        `json:"model"`
	PromptVersion  string        `json:"prompt_version"`
	PromptSHA256   string        `json:"prompt_sha256"`
	Usage          ProviderUsage `json:"usage"`
	ClaimIDs       []string      `json:"claim_ids"`
	BatchSHA256    string        `json:"batch_sha256"`
	Replayed       bool          `json:"replayed"`
}

type draftClaimRecord struct {
	Version        string          `json:"version"`
	Revision       uint64          `json:"revision"`
	PreviousSHA256 string          `json:"previous_sha256,omitempty"`
	Batch          DraftClaimBatch `json:"batch"`
	RecordSHA256   string          `json:"record_sha256"`
}

type loadedDraftClaimLedger struct {
	revision   uint64
	lastRecord string
	byExtent   map[uint64]DraftClaimBatch
	claims     []DraftClaim
	claimIDs   map[string]struct{}
}

type DraftClaimLedger struct {
	mu   sync.Mutex
	root string
}

func OpenDraftClaimLedger(root string) (*DraftClaimLedger, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrDraftClaimInvalid
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("open Draft Claim Ledger: %w", err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("open Draft Claim Ledger: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrDraftClaimInvalid
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("secure Draft Claim Ledger: %w", err)
	}
	return &DraftClaimLedger{root: absolute}, nil
}

func (claim DraftClaim) Validate() error {
	if claim.Version != DraftClaimVersion || !validSourceIntakeID(claim.ClaimID, 160) ||
		claim.Status != DraftClaimReviewRequired ||
		!validSourceIntakeID(claim.JobID, 128) || !validWriteText(claim.Database, 200) ||
		!validSourceSHA256(claim.DocumentSHA256) || claim.ExtentSequence == 0 ||
		!validSourceSHA256(claim.ExtentSHA256) || len(claim.Sources) == 0 || len(claim.Sources) > 32 ||
		!validWriteText(claim.ProviderID, 160) || invalidProviderText(claim.Model, 200, true) ||
		!validSourceIntakeID(claim.PromptVersion, 160) || !validSourceSHA256(claim.PromptSHA256) ||
		!validSourceSHA256(claim.SHA256) || !validDraftCandidate(claim.Candidate) {
		return ErrDraftClaimInvalid
	}
	seen := make(map[string]struct{}, len(claim.Sources))
	for _, source := range claim.Sources {
		if !validSourceIntakeID(source.NodeID, 160) || !validDraftClaimAnchor(source.Anchor) {
			return ErrDraftClaimInvalid
		}
		if _, duplicate := seen[source.NodeID]; duplicate {
			return ErrDraftClaimInvalid
		}
		seen[source.NodeID] = struct{}{}
	}
	digest, err := draftClaimDigest(claim)
	if err != nil || digest != claim.SHA256 {
		return ErrDraftClaimInvalid
	}
	return nil
}

func (batch DraftClaimBatch) Validate() error {
	if batch.Version != DraftClaimBatchVersion || !validSourceIntakeID(batch.JobID, 128) ||
		!validWriteText(batch.Database, 200) || !validSourceSHA256(batch.DocumentSHA256) ||
		batch.ExtentSequence == 0 || !validSourceSHA256(batch.ExtentSHA256) ||
		!validWriteText(batch.ProviderID, 160) || invalidProviderText(batch.Model, 200, true) ||
		!validSourceIntakeID(batch.PromptVersion, 160) || !validSourceSHA256(batch.PromptSHA256) ||
		batch.Claims == nil || len(batch.Claims) > 32 || !validProviderUsage(batch.Usage) ||
		!validSourceSHA256(batch.SHA256) {
		return ErrDraftClaimInvalid
	}
	seen := make(map[string]struct{}, len(batch.Claims))
	for _, claim := range batch.Claims {
		if claim.Validate() != nil || claim.JobID != batch.JobID || claim.Database != batch.Database ||
			claim.DocumentSHA256 != batch.DocumentSHA256 || claim.ExtentSequence != batch.ExtentSequence ||
			claim.ExtentSHA256 != batch.ExtentSHA256 || claim.ProviderID != batch.ProviderID ||
			claim.Model != batch.Model || claim.PromptVersion != batch.PromptVersion ||
			claim.PromptSHA256 != batch.PromptSHA256 {
			return ErrDraftClaimInvalid
		}
		if _, duplicate := seen[claim.ClaimID]; duplicate {
			return ErrDraftClaimInvalid
		}
		seen[claim.ClaimID] = struct{}{}
	}
	digest, err := draftClaimBatchDigest(batch)
	if err != nil || digest != batch.SHA256 {
		return ErrDraftClaimInvalid
	}
	return nil
}

func (receipt DraftClaimReceipt) Validate() error {
	if receipt.Version != DraftClaimReceiptVersion || !validSourceIntakeID(receipt.JobID, 128) ||
		!validWriteText(receipt.Database, 200) || !validSourceSHA256(receipt.DocumentSHA256) ||
		receipt.ExtentSequence == 0 || !validSourceSHA256(receipt.ExtentSHA256) ||
		!validWriteText(receipt.ProviderID, 160) || invalidProviderText(receipt.Model, 200, true) ||
		!validSourceIntakeID(receipt.PromptVersion, 160) || !validSourceSHA256(receipt.PromptSHA256) ||
		receipt.ClaimIDs == nil || len(receipt.ClaimIDs) > 32 || !validProviderUsage(receipt.Usage) ||
		!validSourceSHA256(receipt.BatchSHA256) {
		return ErrDraftClaimInvalid
	}
	seen := make(map[string]struct{}, len(receipt.ClaimIDs))
	for _, claimID := range receipt.ClaimIDs {
		if !validSourceIntakeID(claimID, 160) {
			return ErrDraftClaimInvalid
		}
		if _, duplicate := seen[claimID]; duplicate {
			return ErrDraftClaimInvalid
		}
		seen[claimID] = struct{}{}
	}
	return nil
}

func (ledger *DraftClaimLedger) ReadExtent(jobID string, sequence uint64) (DraftClaimBatch, error) {
	if ledger == nil || !validSourceIntakeID(jobID, 128) || sequence == 0 {
		return DraftClaimBatch{}, ErrDraftClaimInvalid
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	loaded, err := ledger.load(jobID, true)
	if err != nil {
		return DraftClaimBatch{}, err
	}
	batch, found := loaded.byExtent[sequence]
	if !found {
		return DraftClaimBatch{}, ErrDraftClaimNotFound
	}
	return cloneDraftClaimBatch(batch), nil
}

func (ledger *DraftClaimLedger) Claims(jobID string) ([]DraftClaim, error) {
	if ledger == nil || !validSourceIntakeID(jobID, 128) {
		return nil, ErrDraftClaimInvalid
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	loaded, err := ledger.load(jobID, true)
	if err != nil {
		return nil, err
	}
	return cloneDraftClaims(loaded.claims), nil
}

func (ledger *DraftClaimLedger) record(batch DraftClaimBatch) (DraftClaimReceipt, error) {
	if ledger == nil || batch.Validate() != nil {
		return DraftClaimReceipt{}, ErrDraftClaimInvalid
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	loaded, err := ledger.load(batch.JobID, true)
	if err != nil && !errors.Is(err, ErrDraftClaimNotFound) {
		return DraftClaimReceipt{}, err
	}
	if loaded == nil {
		loaded = newLoadedDraftClaimLedger()
	}
	if existing, found := loaded.byExtent[batch.ExtentSequence]; found {
		if existing.SHA256 != batch.SHA256 {
			return DraftClaimReceipt{}, ErrDraftClaimConflict
		}
		return draftClaimReceipt(existing, true), nil
	}
	for _, claim := range batch.Claims {
		if _, duplicate := loaded.claimIDs[claim.ClaimID]; duplicate {
			return DraftClaimReceipt{}, ErrDraftClaimConflict
		}
	}
	record := draftClaimRecord{
		Version: draftClaimRecordVersion, Revision: loaded.revision + 1,
		PreviousSHA256: loaded.lastRecord, Batch: cloneDraftClaimBatch(batch),
	}
	record.RecordSHA256, err = draftClaimRecordDigest(record)
	if err != nil {
		return DraftClaimReceipt{}, ErrDraftClaimInvalid
	}
	if err := ledger.append(batch.JobID, record, loaded.revision == 0); err != nil {
		return DraftClaimReceipt{}, err
	}
	return draftClaimReceipt(batch, false), nil
}

func (ledger *DraftClaimLedger) load(jobID string, repairTail bool) (*loadedDraftClaimLedger, error) {
	path := ledger.path(jobID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrDraftClaimNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Draft Claim Ledger: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxDraftClaimJournalBytes {
		return nil, ErrDraftClaimLedgerCorrupt
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Draft Claim Ledger: %w", err)
	}
	if len(data) == 0 {
		return nil, ledger.recoverEmpty(path, repairTail)
	}
	complete := data
	torn := false
	if data[len(data)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(data, '\n')
		if lastNewline < 0 {
			return nil, ledger.recoverEmpty(path, repairTail)
		}
		complete = data[:lastNewline+1]
		torn = true
	}
	loaded := newLoadedDraftClaimLedger()
	for _, line := range bytes.Split(bytes.TrimSuffix(complete, []byte{'\n'}), []byte{'\n'}) {
		if len(line) == 0 || len(line) > maxDraftClaimRecordBytes {
			return nil, ErrDraftClaimLedgerCorrupt
		}
		var record draftClaimRecord
		if err := decodeDraftClaimRecord(line, &record); err != nil ||
			ledger.applyLoadedRecord(jobID, loaded, record) != nil {
			return nil, ErrDraftClaimLedgerCorrupt
		}
	}
	if torn && repairTail {
		if err := truncateDraftClaimJournal(path, int64(len(complete))); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func (ledger *DraftClaimLedger) applyLoadedRecord(
	jobID string,
	loaded *loadedDraftClaimLedger,
	record draftClaimRecord,
) error {
	if record.Version != draftClaimRecordVersion || record.Revision != loaded.revision+1 ||
		record.PreviousSHA256 != loaded.lastRecord || record.Batch.JobID != jobID ||
		record.Batch.Validate() != nil || !validSourceSHA256(record.RecordSHA256) {
		return ErrDraftClaimLedgerCorrupt
	}
	want, err := draftClaimRecordDigest(record)
	if err != nil || want != record.RecordSHA256 {
		return ErrDraftClaimLedgerCorrupt
	}
	if _, duplicate := loaded.byExtent[record.Batch.ExtentSequence]; duplicate {
		return ErrDraftClaimLedgerCorrupt
	}
	for _, claim := range record.Batch.Claims {
		if _, duplicate := loaded.claimIDs[claim.ClaimID]; duplicate {
			return ErrDraftClaimLedgerCorrupt
		}
		loaded.claimIDs[claim.ClaimID] = struct{}{}
	}
	batch := cloneDraftClaimBatch(record.Batch)
	loaded.byExtent[batch.ExtentSequence] = batch
	loaded.claims = append(loaded.claims, cloneDraftClaims(batch.Claims)...)
	loaded.revision = record.Revision
	loaded.lastRecord = record.RecordSHA256
	return nil
}

func (ledger *DraftClaimLedger) append(jobID string, record draftClaimRecord, create bool) error {
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > maxDraftClaimRecordBytes {
		return ErrDraftClaimInvalid
	}
	encoded = append(encoded, '\n')
	flags := os.O_WRONLY | os.O_APPEND
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(ledger.path(jobID), flags, 0o600)
	if err != nil {
		return fmt.Errorf("append Draft Claim Ledger: %w", err)
	}
	writeErr := writeDraftClaimBytes(file, encoded)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append Draft Claim Ledger: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Draft Claim Ledger: %w", closeErr)
	}
	if create {
		return syncDraftClaimDirectory(ledger.root)
	}
	return nil
}

func (ledger *DraftClaimLedger) recoverEmpty(path string, repair bool) error {
	if !repair {
		return ErrDraftClaimLedgerCorrupt
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("recover Draft Claim Ledger: %w", err)
	}
	if err := syncDraftClaimDirectory(ledger.root); err != nil {
		return err
	}
	return ErrDraftClaimNotFound
}

func (ledger *DraftClaimLedger) path(jobID string) string {
	digest := sha256.Sum256([]byte(jobID))
	return filepath.Join(ledger.root, hex.EncodeToString(digest[:])+draftClaimJournalSuffix)
}

func newLoadedDraftClaimLedger() *loadedDraftClaimLedger {
	return &loadedDraftClaimLedger{
		byExtent: make(map[uint64]DraftClaimBatch), claimIDs: make(map[string]struct{}),
	}
}

func sealDraftClaim(claim DraftClaim) (DraftClaim, error) {
	claim.SHA256 = ""
	digest, err := draftClaimDigest(claim)
	if err != nil {
		return DraftClaim{}, ErrDraftClaimInvalid
	}
	claim.SHA256 = digest
	if claim.Validate() != nil {
		return DraftClaim{}, ErrDraftClaimInvalid
	}
	return cloneDraftClaim(claim), nil
}

func sealDraftClaimBatch(batch DraftClaimBatch) (DraftClaimBatch, error) {
	batch.SHA256 = ""
	digest, err := draftClaimBatchDigest(batch)
	if err != nil {
		return DraftClaimBatch{}, ErrDraftClaimInvalid
	}
	batch.SHA256 = digest
	if batch.Validate() != nil {
		return DraftClaimBatch{}, ErrDraftClaimInvalid
	}
	return cloneDraftClaimBatch(batch), nil
}

func draftClaimDigest(claim DraftClaim) (string, error) {
	claim.SHA256 = ""
	encoded, err := json.Marshal(claim)
	if err != nil || len(encoded) > maxDraftClaimCandidateBytes {
		return "", ErrDraftClaimInvalid
	}
	return draftClaimSHA256(encoded), nil
}

func draftClaimBatchDigest(batch DraftClaimBatch) (string, error) {
	batch.SHA256 = ""
	encoded, err := json.Marshal(batch)
	if err != nil || len(encoded) > maxDraftClaimRecordBytes {
		return "", ErrDraftClaimInvalid
	}
	return draftClaimSHA256(encoded), nil
}

func draftClaimRecordDigest(record draftClaimRecord) (string, error) {
	record.RecordSHA256 = ""
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > maxDraftClaimRecordBytes {
		return "", ErrDraftClaimInvalid
	}
	return draftClaimSHA256(encoded), nil
}

func draftClaimSHA256(encoded []byte) string {
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func draftClaimReceipt(batch DraftClaimBatch, replayed bool) DraftClaimReceipt {
	claimIDs := make([]string, len(batch.Claims))
	for index, claim := range batch.Claims {
		claimIDs[index] = claim.ClaimID
	}
	return DraftClaimReceipt{
		Version: DraftClaimReceiptVersion, JobID: batch.JobID, Database: batch.Database,
		DocumentSHA256: batch.DocumentSHA256, ExtentSequence: batch.ExtentSequence,
		ExtentSHA256: batch.ExtentSHA256, ProviderID: batch.ProviderID, Model: batch.Model,
		PromptVersion: batch.PromptVersion, PromptSHA256: batch.PromptSHA256,
		Usage: batch.Usage, ClaimIDs: claimIDs, BatchSHA256: batch.SHA256, Replayed: replayed,
	}
}

func validDraftCandidate(candidate protocolmsql.AssimilationStatement) bool {
	mutation := candidate.Mutation
	if strings.TrimSpace(candidate.MSQL) == "" || !utf8.ValidString(candidate.MSQL) ||
		len(candidate.MSQL) > 64<<10 || strings.ContainsRune(candidate.MSQL, '\x00') ||
		mutation.ExpectedSchemaVersion == 0 || mutation.MaxAffectedRows == 0 || mutation.MaxAffectedRows > 1024 ||
		!validWriteText(mutation.Actor, 160) || !validWriteText(mutation.Source, 1000) ||
		!validWriteText(mutation.Reason, 1000) || mutation.SourceKind != protocolmsql.SourceDocumentAnchor ||
		mutation.SourceReceiptID != "" || !validWriteText(mutation.SourceLocator, 2048) ||
		!validSourceSHA256(mutation.SourceContentHash) {
		return false
	}
	encoded, err := json.Marshal(candidate)
	return err == nil && len(encoded) <= maxDraftClaimCandidateBytes
}

func validDraftClaimAnchor(anchor DocumentAnchor) bool {
	return validSourceIntakeID(anchor.ResourceID, 160) && anchor.End > anchor.Start &&
		len(anchor.Selector) <= 2048 && utf8.ValidString(anchor.Selector) && !strings.ContainsRune(anchor.Selector, '\x00')
}

func cloneDraftClaimBatch(batch DraftClaimBatch) DraftClaimBatch {
	batch.Claims = cloneDraftClaims(batch.Claims)
	return batch
}

func cloneDraftClaims(claims []DraftClaim) []DraftClaim {
	if claims == nil {
		return nil
	}
	cloned := make([]DraftClaim, len(claims))
	for index, claim := range claims {
		cloned[index] = cloneDraftClaim(claim)
	}
	return cloned
}

func cloneDraftClaim(claim DraftClaim) DraftClaim {
	claim.Sources = append([]DraftClaimSource(nil), claim.Sources...)
	claim.Candidate = protocolmsql.AssimilationStatement{
		MSQL:       claim.Candidate.MSQL,
		Parameters: cloneWriteParameters(claim.Candidate.Parameters),
		Mutation:   cloneWriteMutation(claim.Candidate.Mutation),
	}
	return claim
}

func decodeDraftClaimRecord(encoded []byte, target *draftClaimRecord) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

func writeDraftClaimBytes(file *os.File, data []byte) error {
	for len(data) > 0 {
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

func truncateDraftClaimJournal(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("recover Draft Claim Ledger: %w", err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return fmt.Errorf("recover Draft Claim Ledger: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("recover Draft Claim Ledger: %w", err)
	}
	return file.Close()
}

func syncDraftClaimDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync Draft Claim Ledger directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Draft Claim Ledger directory: %w", syncErr)
	}
	return closeErr
}
