package executor

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
)

const (
	changeCursorVersion = "memora.change-cursor/v1"
	changeCursorMaximum = 4096
	changeTimelineKind  = "timeline"
	changeEntriesKind   = "entries"
)

type changeCursor struct {
	Version          string `json:"version"`
	Kind             string `json:"kind"`
	Scope            string `json:"scope"`
	Snapshot         uint64 `json:"snapshot"`
	Position         uint64 `json:"position"`
	TransactionID    string `json:"transaction_id"`
	EnvelopeChecksum string `json:"envelope_checksum"`
	Offset           uint64 `json:"offset"`
	Checksum         string `json:"checksum"`
}

type changeCursorCore struct {
	Version          string `json:"version"`
	Kind             string `json:"kind"`
	Scope            string `json:"scope"`
	Snapshot         uint64 `json:"snapshot"`
	Position         uint64 `json:"position"`
	TransactionID    string `json:"transaction_id"`
	EnvelopeChecksum string `json:"envelope_checksum"`
	Offset           uint64 `json:"offset"`
}

func encodeChangeCursor(core changeCursorCore) (string, error) {
	checksum, err := changeCursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(changeCursor{
		Version: core.Version, Kind: core.Kind, Scope: core.Scope,
		Snapshot: core.Snapshot, Position: core.Position,
		TransactionID: core.TransactionID, EnvelopeChecksum: core.EnvelopeChecksum,
		Offset: core.Offset, Checksum: checksum,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeChangeCursor(value string) (changeCursor, error) {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > changeCursorMaximum {
		return changeCursor{}, fmt.Errorf("invalid cursor encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded changeCursor
	if err := decoder.Decode(&decoded); err != nil {
		return changeCursor{}, fmt.Errorf("invalid cursor payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return changeCursor{}, fmt.Errorf("invalid cursor trailing data")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(canonical, encoded) || !validChangeCursor(decoded) {
		return changeCursor{}, fmt.Errorf("invalid or non-canonical cursor payload")
	}
	want, err := changeCursorChecksum(changeCursorCore{
		Version: decoded.Version, Kind: decoded.Kind, Scope: decoded.Scope,
		Snapshot: decoded.Snapshot, Position: decoded.Position,
		TransactionID: decoded.TransactionID, EnvelopeChecksum: decoded.EnvelopeChecksum,
		Offset: decoded.Offset,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return changeCursor{}, fmt.Errorf("invalid cursor checksum")
	}
	return decoded, nil
}

func validChangeCursor(value changeCursor) bool {
	if value.Version != changeCursorVersion || !strings.HasPrefix(value.Scope, "sha256:") ||
		len(value.Scope) != len("sha256:")+64 || value.Checksum == "" {
		return false
	}
	switch value.Kind {
	case changeTimelineKind:
		return value.Snapshot > 0 && value.Position > 0 && value.Position <= value.Snapshot &&
			value.TransactionID == "" && value.EnvelopeChecksum == "" && value.Offset == 0
	case changeEntriesKind:
		return value.Snapshot > 0 && value.Position == 0 && value.TransactionID != "" &&
			len(value.EnvelopeChecksum) == 64 && value.Offset > 0
	default:
		return false
	}
}

func changeCursorChecksum(core changeCursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func changeScope(kind, databaseID string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + databaseID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func changeSnapshot(scope string, sequence uint64, transactionChecksum string) string {
	encoded, _ := json.Marshal(struct {
		Scope               string `json:"scope"`
		Sequence            uint64 `json:"sequence"`
		TransactionChecksum string `json:"transaction_checksum"`
	}{scope, sequence, transactionChecksum})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
