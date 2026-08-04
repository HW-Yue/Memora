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
	"sync"
)

const (
	AssimilationJournalRecordVersion = "memora.assimilation-journal-record/v1"
	assimilationJournalSuffix        = ".ajournal"
	maxAssimilationJournalBytes      = 64 << 20
	maxAssimilationJournalRecord     = 1 << 20
	maxAssimilationEventPage         = 1000
)

type AssimilationJobStore struct {
	mu   sync.Mutex
	root string
}

type assimilationJournalRecord struct {
	Version        string                 `json:"version"`
	Revision       uint64                 `json:"revision"`
	PreviousSHA256 string                 `json:"previous_sha256,omitempty"`
	CommandSHA256  string                 `json:"command_sha256"`
	Command        AssimilationJobCommand `json:"command"`
	Event          AssimilationJobEvent   `json:"event"`
	RecordSHA256   string                 `json:"record_sha256"`
}

type assimilationAppliedCommand struct {
	digest   string
	event    AssimilationJobEvent
	snapshot AssimilationJobSnapshot
}

type loadedAssimilationJob struct {
	snapshot   AssimilationJobSnapshot
	events     []AssimilationJobEvent
	commands   map[string]assimilationAppliedCommand
	lastRecord string
}

func OpenAssimilationJobStore(root string) (*AssimilationJobStore, error) {
	if root == "" {
		return nil, ErrAssimilationJobInvalid
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("%w: resolve root: %v", ErrAssimilationJobInvalid, err)
	}
	if err := os.MkdirAll(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("open Assimilation Job Store: %w", err)
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("open Assimilation Job Store: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrAssimilationJobInvalid
	}
	if err := os.Chmod(absolute, 0o700); err != nil {
		return nil, fmt.Errorf("secure Assimilation Job Store: %w", err)
	}
	return &AssimilationJobStore{root: absolute}, nil
}

func (store *AssimilationJobStore) Apply(command AssimilationJobCommand) (AssimilationJobReceipt, error) {
	if store == nil || !validAssimilationJobCommand(command) {
		return AssimilationJobReceipt{}, ErrAssimilationJobInvalid
	}
	command = cloneAssimilationJobCommand(command)
	commandDigest, err := assimilationCommandDigest(command)
	if err != nil {
		return AssimilationJobReceipt{}, ErrAssimilationJobInvalid
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	loaded, err := store.load(command.JobID, true)
	if err != nil && !errors.Is(err, ErrAssimilationJobNotFound) {
		return AssimilationJobReceipt{}, err
	}
	if loaded == nil {
		loaded = &loadedAssimilationJob{commands: make(map[string]assimilationAppliedCommand)}
	}
	if applied, exists := loaded.commands[command.CommandID]; exists {
		if applied.digest != commandDigest {
			return AssimilationJobReceipt{}, ErrAssimilationJobCommandConflict
		}
		return AssimilationJobReceipt{
			Event:    cloneAssimilationJobEvent(applied.event),
			Snapshot: cloneAssimilationJobSnapshot(applied.snapshot), Replayed: true,
		}, nil
	}
	if errors.Is(err, ErrAssimilationJobNotFound) && command.Kind != AssimilationJobCommandStart {
		return AssimilationJobReceipt{}, ErrAssimilationJobNotFound
	}
	event, next, err := deriveAssimilationJobEvent(loaded.snapshot, command, commandDigest)
	if err != nil {
		return AssimilationJobReceipt{}, err
	}
	record := assimilationJournalRecord{
		Version: AssimilationJournalRecordVersion, Revision: event.Revision,
		PreviousSHA256: loaded.lastRecord, CommandSHA256: commandDigest,
		Command: command, Event: event,
	}
	record.RecordSHA256, err = assimilationRecordDigest(record)
	if err != nil {
		return AssimilationJobReceipt{}, ErrAssimilationJobInvalid
	}
	if err := store.appendRecord(command.JobID, record, loaded.snapshot.Revision == 0); err != nil {
		return AssimilationJobReceipt{}, err
	}
	return AssimilationJobReceipt{
		Event: cloneAssimilationJobEvent(event), Snapshot: cloneAssimilationJobSnapshot(next),
	}, nil
}

func (store *AssimilationJobStore) Read(jobID string) (AssimilationJobSnapshot, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) {
		return AssimilationJobSnapshot{}, ErrAssimilationJobInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	loaded, err := store.load(jobID, true)
	if err != nil {
		return AssimilationJobSnapshot{}, err
	}
	return cloneAssimilationJobSnapshot(loaded.snapshot), nil
}

func (store *AssimilationJobStore) Events(
	jobID string,
	afterRevision uint64,
	limit uint64,
) ([]AssimilationJobEvent, error) {
	if store == nil || !validSourceIntakeID(jobID, 128) || limit == 0 ||
		limit > maxAssimilationEventPage {
		return nil, ErrAssimilationJobInvalid
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	loaded, err := store.load(jobID, true)
	if err != nil {
		return nil, err
	}
	events := make([]AssimilationJobEvent, 0, min(int(limit), len(loaded.events)))
	for _, event := range loaded.events {
		if event.Revision <= afterRevision {
			continue
		}
		events = append(events, cloneAssimilationJobEvent(event))
		if uint64(len(events)) == limit {
			break
		}
	}
	return events, nil
}

func (store *AssimilationJobStore) load(jobID string, repairTail bool) (*loadedAssimilationJob, error) {
	path := store.journalPath(jobID)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrAssimilationJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("inspect Assimilation Job journal: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrAssimilationJobCorrupt
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read Assimilation Job journal: %w", err)
	}
	if len(data) > maxAssimilationJournalBytes {
		return nil, ErrAssimilationJobCorrupt
	}
	if len(data) == 0 {
		return nil, store.recoverEmptyJournal(path, repairTail)
	}
	complete := data
	torn := false
	if data[len(data)-1] != '\n' {
		lastNewline := bytes.LastIndexByte(data, '\n')
		if lastNewline < 0 {
			return nil, store.recoverEmptyJournal(path, repairTail)
		}
		complete = data[:lastNewline+1]
		torn = true
	}
	trimmed := bytes.TrimSuffix(complete, []byte{'\n'})
	lines := bytes.Split(trimmed, []byte{'\n'})
	loaded := &loadedAssimilationJob{commands: make(map[string]assimilationAppliedCommand)}
	for _, line := range lines {
		if len(line) == 0 || len(line) > maxAssimilationJournalRecord {
			return nil, ErrAssimilationJobCorrupt
		}
		var record assimilationJournalRecord
		if err := decodeAssimilationRecord(line, &record); err != nil {
			return nil, ErrAssimilationJobCorrupt
		}
		if err := validateAssimilationRecord(jobID, loaded, record); err != nil {
			return nil, err
		}
		commandDigest := record.CommandSHA256
		event, next, err := deriveAssimilationJobEvent(loaded.snapshot, record.Command, commandDigest)
		if err != nil || !equalAssimilationJobEvent(event, record.Event) {
			return nil, ErrAssimilationJobCorrupt
		}
		if _, duplicate := loaded.commands[record.Command.CommandID]; duplicate {
			return nil, ErrAssimilationJobCorrupt
		}
		loaded.snapshot = next
		loaded.events = append(loaded.events, cloneAssimilationJobEvent(record.Event))
		loaded.commands[record.Command.CommandID] = assimilationAppliedCommand{
			digest: commandDigest, event: cloneAssimilationJobEvent(record.Event),
			snapshot: cloneAssimilationJobSnapshot(next),
		}
		loaded.lastRecord = record.RecordSHA256
	}
	if torn && repairTail {
		if err := truncateAssimilationJournal(path, int64(len(complete))); err != nil {
			return nil, err
		}
	}
	return loaded, nil
}

func (store *AssimilationJobStore) recoverEmptyJournal(path string, repair bool) error {
	if !repair {
		return ErrAssimilationJobCorrupt
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("recover empty Assimilation Job journal: %w", err)
	}
	if err := syncAssimilationDirectory(store.root); err != nil {
		return err
	}
	return ErrAssimilationJobNotFound
}

func validateAssimilationRecord(
	jobID string,
	loaded *loadedAssimilationJob,
	record assimilationJournalRecord,
) error {
	if record.Version != AssimilationJournalRecordVersion || record.Revision != loaded.snapshot.Revision+1 ||
		record.PreviousSHA256 != loaded.lastRecord || record.Command.JobID != jobID ||
		record.Event.JobID != jobID || !validAssimilationJobCommand(record.Command) ||
		record.Event.Validate() != nil || record.CommandSHA256 != record.Event.CommandSHA256 ||
		!validSourceSHA256(record.CommandSHA256) || !validSourceSHA256(record.RecordSHA256) {
		return ErrAssimilationJobCorrupt
	}
	commandDigest, err := assimilationCommandDigest(record.Command)
	if err != nil || commandDigest != record.CommandSHA256 {
		return ErrAssimilationJobCorrupt
	}
	recordDigest, err := assimilationRecordDigest(record)
	if err != nil || recordDigest != record.RecordSHA256 {
		return ErrAssimilationJobCorrupt
	}
	return nil
}

func (store *AssimilationJobStore) appendRecord(
	jobID string,
	record assimilationJournalRecord,
	create bool,
) error {
	encoded, err := json.Marshal(record)
	if err != nil || len(encoded) > maxAssimilationJournalRecord {
		return ErrAssimilationJobInvalid
	}
	encoded = append(encoded, '\n')
	path := store.journalPath(jobID)
	flags := os.O_WRONLY | os.O_APPEND
	if create {
		flags |= os.O_CREATE | os.O_EXCL
	}
	file, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return fmt.Errorf("append Assimilation Job journal: %w", err)
	}
	writeErr := writeAssimilationRecord(file, encoded)
	if writeErr == nil {
		writeErr = file.Sync()
	}
	closeErr := file.Close()
	if writeErr != nil {
		return fmt.Errorf("append Assimilation Job journal: %w", writeErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Assimilation Job journal: %w", closeErr)
	}
	if create {
		if err := syncAssimilationDirectory(store.root); err != nil {
			return err
		}
	}
	return nil
}

func (store *AssimilationJobStore) journalPath(jobID string) string {
	digest := sha256.Sum256([]byte(jobID))
	return filepath.Join(store.root, hex.EncodeToString(digest[:])+assimilationJournalSuffix)
}

func assimilationCommandDigest(command AssimilationJobCommand) (string, error) {
	encoded, err := json.Marshal(cloneAssimilationJobCommand(command))
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func assimilationRecordDigest(record assimilationJournalRecord) (string, error) {
	payload := struct {
		Version        string                 `json:"version"`
		Revision       uint64                 `json:"revision"`
		PreviousSHA256 string                 `json:"previous_sha256,omitempty"`
		CommandSHA256  string                 `json:"command_sha256"`
		Command        AssimilationJobCommand `json:"command"`
		Event          AssimilationJobEvent   `json:"event"`
	}{
		Version: record.Version, Revision: record.Revision,
		PreviousSHA256: record.PreviousSHA256, CommandSHA256: record.CommandSHA256,
		Command: cloneAssimilationJobCommand(record.Command),
		Event:   cloneAssimilationJobEvent(record.Event),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func decodeAssimilationRecord(encoded []byte, target *assimilationJournalRecord) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrAssimilationJobCorrupt
	}
	return nil
}

func writeAssimilationRecord(file *os.File, data []byte) error {
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

func truncateAssimilationJournal(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("recover Assimilation Job journal: %w", err)
	}
	if err := file.Truncate(size); err != nil {
		_ = file.Close()
		return fmt.Errorf("recover Assimilation Job journal: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("recover Assimilation Job journal: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("recover Assimilation Job journal: %w", err)
	}
	return nil
}

func syncAssimilationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("sync Assimilation Job directory: %w", err)
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil {
		return fmt.Errorf("sync Assimilation Job directory: %w", syncErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close Assimilation Job directory: %w", closeErr)
	}
	return nil
}
