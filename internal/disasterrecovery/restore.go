// Package disasterrecovery rebuilds a Database from its backups alone.
//
// The chain has two halves. The full half is a base copy of the record log
// taken at a commit boundary. The incremental half is the binlog
// (internal/binlog): every transaction that committed, in order, kept for a
// retention window. Neither is enough alone — the base stops at the moment it
// was taken, and the log only reaches back as far as retention. What joins them
// is a binlog position recorded with the base, so a restore replays exactly the
// transactions the base does not already hold.
//
// The page files are not backed up and not restored. They are derived: opening
// the instance afterwards builds them from the record log. That is what makes
// this chain the bottom rather than a second copy of the same thing.
//
// # Why the base is a byte copy and not a logical snapshot
//
// internal/nativesnapshot exports a logical snapshot — what the Database means,
// in a form that outlives the storage format. It is the right thing for
// migration and for comparing two engines, and it is the wrong thing to roll a
// binlog forward from. A logical import replays history through the ordinary
// write path, so it allocates its own change sequences and keeps only the
// current revision of each catalog object. The binlog, by contrast, carries
// record IDs and payloads exactly as they were written. Land one on the other
// and the numbering no longer lines up: the restored change log is dense from
// one, the log's frames continue from six, and the join is a gap the change
// index refuses (internal/store/changeindex Bootstrap). TestALogicalSnapshotIsNotARollforwardBase
// pins that, so the reason survives the next person who tries it.
//
// A byte copy has the opposite property and the one that matters here: the
// record log is append-only, so the bytes below a commit boundary never change
// again. The base can therefore be taken from a running Database with nothing
// quiesced, and what it holds is exactly a prefix of what the log describes.
package disasterrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/binlog"
	"github.com/HW-Yue/Memora/internal/nativemigration"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	// BaseFilename is the record log prefix a backup copies out.
	BaseFilename = "base.memora"
	// ReceiptFilename records where that prefix stops, in both the record log
	// and the binlog. A base without it cannot be rolled forward, so the two
	// are written together.
	ReceiptFilename = "base.json"
)

var (
	// ErrNotEmpty is a restore aimed at a data directory that already holds a
	// Database. Restoring over one would interleave two histories.
	ErrNotEmpty = errors.New("restore target already holds a Database")
	// ErrDiverged is a binlog frame that contradicts the base: the same record
	// ID carrying different bytes. The log and the base came from different
	// Databases, and replaying further would build a third one.
	ErrDiverged = errors.New("binlog frame contradicts the base backup")
)

// Receipt is what a backup writes beside its base, and what a restore reads to
// know where to resume.
type Receipt struct {
	// RecordBytes is how much of the record log the base holds. It is a commit
	// boundary: everything below it is whole transactions.
	RecordBytes int64 `json:"record_bytes"`
	// Binlog is where the log stood when the base was taken. It is read
	// *before* the record log's length on purpose — see Backup.
	Binlog binlog.Position `json:"binlog"`
}

// Backup copies the record log up to a commit boundary and records where the
// binlog stood.
//
// Order matters and only one order is safe. The binlog position is read first
// and the record log's length second, so anything that commits in between lands
// in both: it is below the length the base copies, and it is after the position
// the tail replays from. An overlap is recoverable — the restore recognises a
// transaction it already holds — while the other order leaves a gap, and a gap
// is a Database that never existed.
//
// Nothing is quiesced. The record log only ever appends, so the bytes below a
// boundary are already final, and a concurrent writer is simply writing past
// the end of what is being copied.
func Backup(
	ctx context.Context, file *nativestore.File, log *binlog.Log, destination string,
) (Receipt, error) {
	if file == nil || log == nil {
		return Receipt{}, fmt.Errorf("backup needs a record log and a binlog")
	}
	if !filepath.IsAbs(destination) {
		return Receipt{}, fmt.Errorf("backup destination must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	position, err := log.Position()
	if err != nil {
		return Receipt{}, err
	}
	size, err := file.Size()
	if err != nil {
		return Receipt{}, err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return Receipt{}, err
	}
	if err := copyPrefix(file.Path(), filepath.Join(destination, BaseFilename), size); err != nil {
		return Receipt{}, err
	}
	receipt := Receipt{RecordBytes: size, Binlog: position}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	if err := os.WriteFile(filepath.Join(destination, ReceiptFilename), encoded, 0o600); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

// Restore rebuilds an instance's record log from a base and the binlog after it.
//
// dataDir must not already hold a record file; binlogDirectory is the archived
// log, which in a real disaster is wherever it was shipped to rather than
// beside the Database it describes. The instance is left with a record log and
// no page files, and the next open builds those.
//
// The restored Database is not written back to the binlog. Replaying into the
// log being replayed would append the history a second time. A restored
// instance therefore starts a new backup lineage: take a fresh base before
// relying on it.
func Restore(ctx context.Context, dataDir, backupRoot, binlogDirectory string) error {
	if !filepath.IsAbs(dataDir) || !filepath.IsAbs(backupRoot) {
		return fmt.Errorf("restore paths must be absolute")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := os.ReadFile(filepath.Join(backupRoot, ReceiptFilename))
	if err != nil {
		return fmt.Errorf("read backup receipt: %w", err)
	}
	var receipt Receipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		return fmt.Errorf("backup receipt is corrupt: %w", err)
	}
	directory := filepath.Join(dataDir, "databases")
	recordPath := filepath.Join(directory, nativemigration.NativeFilename)
	if _, err := os.Stat(recordPath); err == nil {
		return fmt.Errorf("%w: %s", ErrNotEmpty, recordPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := copyPrefix(
		filepath.Join(backupRoot, BaseFilename), recordPath, receipt.RecordBytes,
	); err != nil {
		return err
	}
	// A restore that stops partway removes what it wrote. Leaving the base
	// behind would leave an operator a record log that opens, serves reads, and
	// is missing everything after the backup — the one failure worth more than
	// no restore at all.
	restored := false
	defer func() {
		if !restored {
			_ = os.Remove(recordPath)
		}
	}()
	file, err := nativestore.Open(recordPath)
	if err != nil {
		return fmt.Errorf("open restored record log: %w", err)
	}
	// No binlog sink is attached on purpose: see the doc comment.
	defer func() { _ = file.Close() }()
	// Retention is suspended for the read: dropping a segment while restoring
	// from it would turn a recoverable backup into a gap.
	log, err := binlog.OpenWithOptions(binlogDirectory, binlog.Options{Retention: -1})
	if err != nil {
		return fmt.Errorf("open binlog: %w", err)
	}
	defer func() { _ = log.Close() }()
	if err := replayTail(ctx, log, file, receipt.Binlog); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	restored = true
	return nil
}

// replayTail applies every transaction the log holds at or after a position,
// skipping the ones the base already carries.
//
// The skip is what makes Backup's ordering safe: a transaction that committed
// between the position and the length is in both halves, and applying it twice
// would fail on the duplicate record ID. A frame is only skipped when every
// record in it is already present *with the same bytes* — same ID with
// different content is two different Databases, and that is reported.
func replayTail(
	ctx context.Context, log *binlog.Log, target *nativestore.File, from binlog.Position,
) error {
	return log.ReplayFrom(from, func(entry binlog.Entry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		held, err := alreadyHeld(target, entry)
		if err != nil || held {
			return err
		}
		transaction, err := target.Begin()
		if err != nil {
			return err
		}
		for _, record := range entry.Records {
			if err := transaction.Put(
				nativestore.ObjectKind(record.Kind), record.SchemaVersion, record.ID, record.Payload,
			); err != nil {
				_ = transaction.Rollback()
				return fmt.Errorf("replay record %q: %w", record.ID, err)
			}
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("replay transaction %q: %w", entry.TransactionID, err)
		}
		return nil
	})
}

// alreadyHeld reports whether the base already carries this whole transaction.
//
// All of it or none of it: the record log publishes a transaction's records
// together, so a base holding some but not all of one is not an overlap, it is
// corruption, and it is reported rather than patched up.
func alreadyHeld(target *nativestore.File, entry binlog.Entry) (bool, error) {
	present := 0
	for _, record := range entry.Records {
		payload, err := target.FindRecord(nativestore.ObjectKind(record.Kind), record.ID)
		if errors.Is(err, nativestore.ErrNotFound) {
			continue
		}
		if err != nil {
			return false, err
		}
		if string(payload) != string(record.Payload) {
			return false, fmt.Errorf("%w: record %q", ErrDiverged, record.ID)
		}
		present++
	}
	if present != 0 && present != len(entry.Records) {
		return false, fmt.Errorf(
			"%w: transaction %q is %d of %d records into the base",
			ErrDiverged, entry.TransactionID, present, len(entry.Records),
		)
	}
	return present != 0, nil
}

// copyPrefix writes the first length bytes of source to destination.
func copyPrefix(source, destination string, length int64) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { _ = input.Close() }()
	output, err := os.OpenFile(destination, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.CopyN(output, input, length); err != nil {
		_ = output.Close()
		_ = os.Remove(destination)
		return fmt.Errorf("copy %s: %w", filepath.Base(source), err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		return err
	}
	return output.Close()
}
