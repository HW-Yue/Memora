package history

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	ReadCursorVersion = "memora.history-cursor/v1"
	maximumCursorSize = 4096
)

type ReadPage struct {
	Snapshot   string
	NextCursor string
}

type readCursor struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
	Checksum string `json:"checksum"`
}

type readCursorCore struct {
	Version  string `json:"version"`
	Scope    string `json:"scope"`
	Snapshot string `json:"snapshot"`
	Offset   uint64 `json:"offset"`
}

type pageRecord struct {
	Revision          uint64     `json:"revision"`
	CommitSequence    uint64     `json:"commit_sequence"`
	SchemaVersion     uint64     `json:"schema_version"`
	Operation         Operation  `json:"operation"`
	State             string     `json:"row_state"`
	Actor             string     `json:"actor"`
	Source            string     `json:"source"`
	SourceKind        SourceKind `json:"source_kind"`
	SourceReceiptID   string     `json:"source_receipt_id,omitempty"`
	SourceLocator     string     `json:"source_locator,omitempty"`
	SourceContentHash string     `json:"source_content_hash,omitempty"`
	Reason            string     `json:"reason"`
	UpdatedAt         string     `json:"updated_at"`
}

func Paginate(scope, cursor string, limit int, records []Record) ([]Record, ReadPage, error) {
	if strings.TrimSpace(scope) == "" || limit < 1 || limit > 1000 {
		return nil, ReadPage{}, pageError(result.CodeValidation, "History page scope and limit are invalid")
	}
	if records == nil {
		records = []Record{}
	}
	values := make([]pageRecord, 0, len(records))
	for _, record := range records {
		values = append(values, pageRecord{
			Revision: record.Revision, CommitSequence: record.CommitSequence,
			SchemaVersion: record.SchemaVersion, Operation: record.Operation, State: record.State,
			Actor: record.Actor, Source: record.Source, SourceKind: record.SourceKind,
			SourceReceiptID: record.SourceReceiptID, SourceLocator: record.SourceLocator,
			SourceContentHash: record.SourceContentHash, Reason: record.Reason,
			UpdatedAt: record.UpdatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	scopeDigest := digest(struct {
		Kind  string `json:"kind"`
		Scope string `json:"scope"`
	}{Kind: "row_history", Scope: scope})
	snapshot := digest(struct {
		Scope  string       `json:"scope"`
		Values []pageRecord `json:"values"`
	}{Scope: scopeDigest, Values: values})
	start := 0
	if cursor != "" {
		decoded, err := decodeCursor(cursor)
		if err != nil || decoded.Scope != scopeDigest {
			return nil, ReadPage{}, pageError(result.CodeValidation, "History cursor is invalid for this Row")
		}
		if decoded.Snapshot != snapshot {
			return nil, ReadPage{}, pageError(result.CodeRevisionConflict, "Row history changed after the cursor snapshot")
		}
		if decoded.Offset == 0 || decoded.Offset >= uint64(len(records)) || decoded.Offset > uint64(^uint(0)>>1) {
			return nil, ReadPage{}, pageError(result.CodeValidation, "History cursor offset is invalid")
		}
		start = int(decoded.Offset)
	}
	end := len(records)
	if limit < end-start {
		end = start + limit
	}
	page := ReadPage{Snapshot: snapshot}
	if end < len(records) {
		var err error
		page.NextCursor, err = encodeCursor(readCursorCore{
			Version: ReadCursorVersion, Scope: scopeDigest, Snapshot: snapshot, Offset: uint64(end),
		})
		if err != nil {
			return nil, ReadPage{}, pageError(result.CodeInternal, "History cursor could not be encoded")
		}
	}
	return records[start:end], page, nil
}

func encodeCursor(core readCursorCore) (string, error) {
	checksum, err := cursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(readCursor{
		Version: core.Version, Scope: core.Scope, Snapshot: core.Snapshot,
		Offset: core.Offset, Checksum: checksum,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeCursor(value string) (readCursor, error) {
	if value == "" || len(value) > maximumCursorSize {
		return readCursor{}, fmt.Errorf("invalid History cursor size")
	}
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > maximumCursorSize {
		return readCursor{}, fmt.Errorf("invalid History cursor encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded readCursor
	if err := decoder.Decode(&decoded); err != nil || decoded.Version != ReadCursorVersion ||
		decoded.Scope == "" || decoded.Snapshot == "" || decoded.Offset == 0 || decoded.Checksum == "" {
		return readCursor{}, fmt.Errorf("invalid History cursor payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return readCursor{}, fmt.Errorf("invalid History cursor trailing data")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(encoded, canonical) {
		return readCursor{}, fmt.Errorf("non-canonical History cursor")
	}
	want, err := cursorChecksum(readCursorCore{
		Version: decoded.Version, Scope: decoded.Scope, Snapshot: decoded.Snapshot, Offset: decoded.Offset,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return readCursor{}, fmt.Errorf("invalid History cursor checksum")
	}
	return decoded, nil
}

func cursorChecksum(core readCursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func digest(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func pageError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
