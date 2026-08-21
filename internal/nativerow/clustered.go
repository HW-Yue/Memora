package nativerow

import (
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

// EncodeBody produces the bytes a clustered leaf stores for one Row revision.
// The Row codec lives here, so writers ask this package for the body rather than
// the store packages growing a dependency on Row semantics.
// The Row reaching here has already been validated on its way through the write
// path, and a revision being republished from the record log was valid when it
// was written. So this uses the tolerant normaliser, the same one decoding uses:
// re-applying write-time strictness would reject Rows that are already durable —
// a Row whose Table has since gained a column, or an archived column's value.
func EncodeBody(value row.Row, table catalog.Table) ([]byte, error) {
	normalized, err := normalizeStored(value, table)
	if err != nil {
		return nil, err
	}
	return encode(normalized, table)
}

// HistoryRecordID is the record ID a revision's history metadata is stored
// under. It is derived from the Row's identity, so a caller holding a Row can
// find its history without being told where it is.
func HistoryRecordID(rowID string, revision uint64) string {
	return revisionRecordID(rowID, revision)
}

// DecodeHistoryMetadata turns the bytes a clustered leaf carries back into the
// history fields of a Record. The Row itself supplies everything else, so this
// fills in only what the metadata knows: who wrote the revision, why, and when.
func DecodeHistoryMetadata(encoded []byte, value row.Row) (history.Record, error) {
	item, err := decodeHistory(encoded)
	if err != nil {
		return history.Record{}, err
	}
	if item.rowID != value.ID || item.revision != value.Revision {
		return history.Record{}, fmt.Errorf("%w: history metadata identifies another revision", ErrCorrupt)
	}
	return historyRecord(item, value), nil
}

// RowFromLocator decodes the Row a clustered leaf carries and checks it against
// the identity its key promised. A leaf whose bytes describe a different Row is
// corruption: returning it would let a mis-keyed entry masquerade as a Row.
//
// A locator with no body is not an empty Row — it is a revision written before
// leaves carried bodies. Callers get an error and fall back to the record log.
func RowFromLocator(locator rowversionindex.Locator, table catalog.Table) (row.Row, error) {
	if locator.Body == "" {
		return row.Row{}, fmt.Errorf("%w: clustered leaf carries no Row body", ErrNoBody)
	}
	decoded, err := decode([]byte(locator.Body))
	if err != nil {
		return row.Row{}, err
	}
	if decoded.ID != locator.RowID ||
		decoded.DatabaseID != locator.DatabaseID ||
		decoded.TableID != locator.TableID ||
		decoded.Revision != locator.Revision ||
		decoded.SchemaVersion != locator.SchemaRevision ||
		decoded.CommitSequence != locator.CommitSequence ||
		decoded.State != locator.State {
		return row.Row{}, fmt.Errorf("%w: clustered leaf body disagrees with its key", ErrCorrupt)
	}
	return normalizeDecodedRecord(decoded, table)
}
