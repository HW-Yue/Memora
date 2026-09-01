package nativerow

import (
	"encoding/binary"
	"path/filepath"
	"testing"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

// chainFixture writes one Row and then revisions it `revisions-1` times, so the
// Row ends at revision `revisions` with its full history behind it.
func chainFixture(t *testing.T, revisions int) (*nativestore.File, *Repository, row.Row) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "database.memora")
	file, err := nativestore.Create(path, nativestore.FileKindDatabase)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	catalogValue, value := rowFixture()
	if err := nativecatalog.New(file).Write(nil, catalogValue); err != nil {
		t.Fatal(err)
	}
	repository := New(file)
	if err := repository.Write(value); err != nil {
		t.Fatal(err)
	}
	if err := repository.AppendHistory(value, history.OperationInsert, row.WriteMetadata{}, value.CreatedAt); err != nil {
		t.Fatal(err)
	}
	for revision := 2; revision <= revisions; revision++ {
		value.Revision = uint64(revision)
		value.UpdatedAt = value.CreatedAt.Add(time.Duration(revision) * time.Minute)
		value.Values["col_integer"] = int64(revision)
		if err := repository.WriteRevision(value); err != nil {
			t.Fatalf("WriteRevision(%d) error = %v", revision, err)
		}
		if err := repository.AppendHistory(value, history.OperationUpdate, row.WriteMetadata{}, value.UpdatedAt); err != nil {
			t.Fatal(err)
		}
	}
	return file, repository, value
}

// TestRecordsNoLongerCarryAChainPointer pins that the physical version chain is
// gone from new writes. The clustered Row version tree walks revisions now, and
// a second, independently maintained way to reach the previous revision is a
// second source of truth that can only drift.
func TestRecordsNoLongerCarryAChainPointer(t *testing.T) {
	t.Parallel()

	file, _, current := chainFixture(t, 4)

	for revision := uint64(1); revision <= current.Revision; revision++ {
		payload, err := file.Get(nativestore.ObjectKindRow, revisionRecordID(current.ID, revision))
		if err != nil {
			t.Fatal(err)
		}
		if flags := binary.LittleEndian.Uint16(payload[2:4]); flags != 0 {
			t.Fatalf("revision %d was written with flags %d, want 0", revision, flags)
		}
	}
}

// TestRecordsWrittenWithAChainPointerStillDecode pins backward compatibility.
// Databases written while revisions were chained hold records with a trailing
// pointer and the flag set; dropping the writer must not make them unreadable.
func TestRecordsWrittenWithAChainPointerStillDecode(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	payload, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	// Rebuild the historical layout: flag set, fixed-width pointer appended.
	chained := append([]byte(nil), payload...)
	binary.LittleEndian.PutUint16(chained[2:4], flagPreviousLocation)
	chained = binary.LittleEndian.AppendUint64(chained, 4096)
	chained = binary.LittleEndian.AppendUint32(chained, 128)
	chained = binary.LittleEndian.AppendUint32(chained, 99)
	if len(chained) != len(payload)+chainFooterSize {
		t.Fatalf("chained record is %d bytes, want %d", len(chained), len(payload)+chainFooterSize)
	}

	decoded, err := decode(chained)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != value.ID || decoded.Revision != value.Revision {
		t.Fatalf("decoded identity = %#v", decoded)
	}
	if decoded.Values["col_text"] != value.Values["col_text"] {
		t.Fatalf("decoded values = %#v, want %#v", decoded.Values, value.Values)
	}
}

// TestHistoryStillReadsEveryRevisionAfterTheChainIsGone pins that removing the
// chain changed nothing a caller sees.
func TestHistoryStillReadsEveryRevisionAfterTheChainIsGone(t *testing.T) {
	t.Parallel()

	_, repository, current := chainFixture(t, 5)

	records, err := repository.HistoryAll(current.DatabaseID, current.TableID, current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("HistoryAll() returned %d records, want 5", len(records))
	}
	for position, record := range records {
		if record.Revision != uint64(5-position) {
			t.Fatalf("position %d = revision %d, want %d", position, record.Revision, 5-position)
		}
	}
}
