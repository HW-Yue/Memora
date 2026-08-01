package executor

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestChangeCursorCodecRejectsTamperUnknownTrailingAndNoncanonical(t *testing.T) {
	t.Parallel()
	core := changeCursorCore{
		Version: changeCursorVersion, Kind: changeTimelineKind,
		Scope: changeScope(changeTimelineKind, "db_work"), Snapshot: 9, Position: 4,
	}
	encoded, err := encodeChangeCursor(core)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeChangeCursor(encoded)
	if err != nil || decoded.Snapshot != 9 || decoded.Position != 4 || decoded.Scope != core.Scope {
		t.Fatalf("decodeChangeCursor() = %#v, %v", decoded, err)
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(payload[:len(payload)-1], []byte(`,"unknown":true}`)...)
	trailing := append(append([]byte(nil), payload...), []byte(`{}`)...)
	noncanonical := append([]byte(" "), payload...)
	tampered := append([]byte(nil), payload...)
	tampered[len(tampered)/2] ^= 1
	for number, candidate := range [][]byte{unknown, trailing, noncanonical, tampered} {
		if _, err := decodeChangeCursor(base64.RawURLEncoding.EncodeToString(candidate)); err == nil {
			t.Fatalf("corrupt cursor corpus[%d] succeeded", number)
		}
	}
	if _, err := decodeChangeCursor(encoded + "="); err == nil {
		t.Fatal("padded cursor encoding succeeded")
	}
}

func TestChangeEntryCursorRequiresTransactionIdentity(t *testing.T) {
	t.Parallel()
	core := changeCursorCore{
		Version: changeCursorVersion, Kind: changeEntriesKind,
		Scope: changeScope(changeEntriesKind, "db_work"), Snapshot: 7,
		TransactionID:    "txn_" + strings.Repeat("a", 32),
		EnvelopeChecksum: strings.Repeat("b", 64), Offset: 2,
	}
	encoded, err := encodeChangeCursor(core)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeChangeCursor(encoded)
	if err != nil || decoded.TransactionID != core.TransactionID || decoded.Offset != 2 {
		t.Fatalf("entry cursor = %#v, %v", decoded, err)
	}
}
