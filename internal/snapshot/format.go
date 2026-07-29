package snapshot

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/HW-Yue/Memora/internal/result"
)

const (
	Version       = "memora.logical-snapshot/v1"
	legacyVersion = "memora.logical-snapshot/v0"
)

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

type document struct {
	Version        string
	Catalog        json.RawMessage
	Rows           []json.RawMessage
	History        []json.RawMessage
	RelationNow    []json.RawMessage
	RelationPast   []json.RawMessage
	CommitSequence uint64
	Unknown        map[string]json.RawMessage
}

func Migrate(encoded []byte) ([]byte, error) {
	value, err := decodeDocument(encoded)
	if err != nil {
		return nil, err
	}
	if _, err := prepare(value); err != nil {
		return nil, err
	}
	return encodeDocument(value)
}

func CanonicalHash(encoded []byte) (string, error) {
	migrated, err := Migrate(encoded)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(migrated))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return "", snapshotError(result.CodeValidation, "logical snapshot JSON is invalid")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", snapshotError(result.CodeInternal, "logical snapshot could not be canonicalized")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

func cloneRaw(value []byte) json.RawMessage { return append(json.RawMessage(nil), value...) }

func cleanID(value string) bool { return strings.TrimSpace(value) != "" }

func snapshotError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
