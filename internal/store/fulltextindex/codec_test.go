package fulltextindex

import (
	"bytes"
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
)

func TestCodecRoundTripsCanonicalObjectOwnerAndPosting(t *testing.T) {
	prepared, err := prepareDocument(document("row_codec", 9, "codec codec"))
	if err != nil {
		t.Fatal(err)
	}
	kind, objectID, err := decodeObjectKey(prepared.objectKey)
	if err != nil || kind != prepared.object.kind || objectID != prepared.object.objectID {
		t.Fatalf("object key = %q/%q, %v", kind, objectID, err)
	}
	object, err := decodeObjectValue(prepared.objectValue, kind, objectID)
	if err != nil || object != prepared.object {
		t.Fatalf("object value = %#v, %v; want %#v", object, err, prepared.object)
	}
	for _, posting := range prepared.postings {
		ownerKey, err := encodeOwnerKey(posting)
		if err != nil {
			t.Fatal(err)
		}
		owner, err := decodeOwnerKey(ownerKey)
		if err != nil || owner.kind != posting.Kind || owner.objectID != posting.ObjectID ||
			owner.term != posting.Term || owner.fieldID != posting.FieldID {
			t.Fatalf("owner key = %#v, %v", owner, err)
		}
		postingKey, err := encodePostingKey(posting)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodePostingKey(postingKey)
		if err != nil || decoded.posting.Term != posting.Term || decoded.posting.ObjectID != posting.ObjectID {
			t.Fatalf("posting key = %#v, %v", decoded, err)
		}
		value, err := encodePostingValue(posting.Revision, posting.Frequency)
		if err != nil {
			t.Fatal(err)
		}
		revision, frequency, err := decodePostingValue(value)
		if err != nil || revision != posting.Revision || frequency != posting.Frequency {
			t.Fatalf("posting value = %d/%d, %v", revision, frequency, err)
		}
	}
}

func TestCodecRejectsCorruptionCorpus(t *testing.T) {
	prepared, err := prepareDocument(document("row_codec", 9, "codec"))
	if err != nil {
		t.Fatal(err)
	}
	posting := prepared.postings[postingIdentity{term: "codec", fieldID: "col_title"}]
	ownerKey, err := encodeOwnerKey(posting)
	if err != nil {
		t.Fatal(err)
	}
	postingKey, err := encodePostingKey(posting)
	if err != nil {
		t.Fatal(err)
	}
	postingValue, err := encodePostingValue(posting.Revision, posting.Frequency)
	if err != nil {
		t.Fatal(err)
	}

	keyCorpus := [][]byte{
		prepared.objectKey[:len(prepared.objectKey)-1],
		append(bytes.Clone(prepared.objectKey), 0),
		append([]byte{9}, prepared.objectKey[1:]...),
		ownerKey[:len(ownerKey)-1],
		append(bytes.Clone(ownerKey), 0),
		postingKey[:len(postingKey)-1],
		append(bytes.Clone(postingKey), 0),
		func() []byte { value := bytes.Clone(postingKey); value[4] = 0xff; return value }(),
	}
	for number, value := range keyCorpus {
		var decodeErr error
		switch {
		case len(value) > 1 && value[1] == keyKindObject:
			_, _, decodeErr = decodeObjectKey(value)
		case len(value) > 1 && value[1] == keyKindOwner:
			_, decodeErr = decodeOwnerKey(value)
		default:
			_, decodeErr = decodePostingKey(value)
		}
		if !errors.Is(decodeErr, ErrCorrupt) {
			t.Fatalf("key corpus[%d] error = %v", number, decodeErr)
		}
	}

	objectCorpus := [][]byte{
		prepared.objectValue[:len(prepared.objectValue)-1],
		append(bytes.Clone(prepared.objectValue), 0),
		func() []byte { value := bytes.Clone(prepared.objectValue); value[0] ^= 1; return value }(),
		func() []byte { value := bytes.Clone(prepared.objectValue); value[12] = 1; return value }(),
		func() []byte { value := bytes.Clone(prepared.objectValue); value[16] = 0; return value }(),
		func() []byte {
			value := bytes.Clone(prepared.objectValue)
			value[objectHeaderSize] = 0xff
			return value
		}(),
	}
	for number, value := range objectCorpus {
		if _, err := decodeObjectValue(value, fulltext.KindRow, "row_codec"); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("object corpus[%d] error = %v", number, err)
		}
	}
	postingCorpus := [][]byte{
		postingValue[:len(postingValue)-1],
		append(bytes.Clone(postingValue), 0),
		func() []byte { value := bytes.Clone(postingValue); value[0] ^= 1; return value }(),
		func() []byte { value := bytes.Clone(postingValue); value[10] = 1; return value }(),
		make([]byte, postingValueSize),
	}
	for number, value := range postingCorpus {
		if _, _, err := decodePostingValue(value); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("posting corpus[%d] error = %v", number, err)
		}
	}
}
