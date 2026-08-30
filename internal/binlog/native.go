package binlog

import (
	"fmt"

	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// Sink adapts a Log to the record store's commit point.
//
// The store defines the interface rather than importing this package: it owns
// the moment a transaction becomes committed, and the log is only what that
// moment is reported to.
type Sink struct{ log *Log }

func NewSink(log *Log) *Sink { return &Sink{log: log} }

func (sink *Sink) Append(transactionID string, records []nativestore.BinlogRecord) error {
	if sink == nil || sink.log == nil {
		return fmt.Errorf("%w: binlog sink", ErrInvalid)
	}
	converted := make([]Record, 0, len(records))
	for _, record := range records {
		converted = append(converted, Record{
			Kind: record.Kind, SchemaVersion: record.SchemaVersion,
			ID: record.ID, Payload: record.Payload,
		})
	}
	return sink.log.Append(Entry{TransactionID: transactionID, Records: converted})
}

// ReplayInto rebuilds a Database from the log alone.
//
// Each frame is replayed as the transaction it was, in the order it committed,
// so the target ends up holding the same records the source does. This is what
// "the binlog is the only thing recovery consults" means in practice: the
// target starts empty and nothing else is read.
//
// A record the target already holds stops the replay rather than being skipped.
// Replaying into a non-empty Database is a caller mistake, and quietly
// tolerating it would produce a Database that is neither the source nor the
// target.
func ReplayInto(log *Log, target *nativestore.File) error {
	if log == nil || target == nil {
		return fmt.Errorf("%w: binlog replay target", ErrInvalid)
	}
	return log.Replay(func(entry Entry) error {
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
