package nativerow

import (
	"encoding/binary"
	"testing"
)

// TestRecordCarriesTheChangeSequence pins the foreign key that joins a Row
// revision to the transaction that wrote it. Attribution lives once per
// transaction in the Change Log; the version stores the key, not a copy of the
// attribution text.
func TestRecordCarriesTheChangeSequence(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	value.ChangeSequence = 42

	payload, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	if flags := binary.LittleEndian.Uint16(payload[2:4]); flags&flagChangeSequence == 0 {
		t.Fatalf("a record with a change sequence must set its flag, got flags %d", flags)
	}
	decoded, err := decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ChangeSequence != 42 {
		t.Fatalf("decoded change sequence = %d, want 42", decoded.ChangeSequence)
	}
	if decoded.ID != value.ID || decoded.Revision != value.Revision {
		t.Fatalf("decoded identity = %#v", decoded)
	}
}

// TestRecordWithoutAChangeSequenceSetsNoFlag pins that a Row with no change
// sequence encodes exactly as it did before the field existed, so the addition
// costs nothing for revisions that have no transaction to point at.
func TestRecordWithoutAChangeSequenceSetsNoFlag(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	value.ChangeSequence = 0

	payload, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	if flags := binary.LittleEndian.Uint16(payload[2:4]); flags != 0 {
		t.Fatalf("a record with no change sequence must set no flags, got %d", flags)
	}
	decoded, err := decode(payload)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ChangeSequence != 0 {
		t.Fatalf("decoded change sequence = %d, want 0", decoded.ChangeSequence)
	}
}

// TestChainedRecordStillDecodesAlongsideTheNewField pins that the two optional
// trailers have a fixed order. A Database written while revisions were chained
// carries the old pointer; nothing writes it now, but the decoder must keep
// reading those records correctly.
func TestChainedRecordStillDecodesAlongsideTheNewField(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	value.ChangeSequence = 0
	payload, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	chained := append([]byte(nil), payload...)
	binary.LittleEndian.PutUint16(chained[2:4], flagPreviousLocation)
	chained = binary.LittleEndian.AppendUint64(chained, 4096)
	chained = binary.LittleEndian.AppendUint32(chained, 128)
	chained = binary.LittleEndian.AppendUint32(chained, 99)

	decoded, err := decode(chained)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ID != value.ID || decoded.ChangeSequence != 0 {
		t.Fatalf("decoded chained record = %#v", decoded)
	}
}

// TestUnknownFlagsAreRefused pins that the flags word stays a closed set: a bit
// this build does not understand means the record was written by something
// newer, and guessing at its trailers would misread the payload.
func TestUnknownFlagsAreRefused(t *testing.T) {
	t.Parallel()

	databases, value := rowFixture()
	table := databases[0].Tables[0]
	payload, err := encode(value, table)
	if err != nil {
		t.Fatal(err)
	}
	binary.LittleEndian.PutUint16(payload[2:4], 1<<15)

	if _, err := decode(payload); err == nil {
		t.Fatal("an unknown flag bit must be refused")
	}
}
