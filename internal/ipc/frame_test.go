package ipc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"reflect"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	want := Request{Version: Version, RequestID: "request-1", Method: "ping"}
	if err := WriteFrame(&buffer, want, DefaultMaxFrameSize); err != nil {
		t.Fatalf("WriteFrame() error = %v", err)
	}
	var got Request
	if err := ReadFrame(&buffer, &got, DefaultMaxFrameSize); err != nil {
		t.Fatalf("ReadFrame() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadFrame() = %#v, want %#v", got, want)
	}
}

func TestReadFrameRejectsOversizedPayloadBeforeReadingBody(t *testing.T) {
	t.Parallel()

	var input bytes.Buffer
	if err := binary.Write(&input, binary.BigEndian, uint32(65)); err != nil {
		t.Fatal(err)
	}
	if err := ReadFrame(&input, &Request{}, 64); !errors.Is(err, ErrFrameTooLarge) {
		t.Fatalf("ReadFrame() error = %v, want %v", err, ErrFrameTooLarge)
	}
}

func TestReadFrameRejectsTruncatedPayload(t *testing.T) {
	t.Parallel()

	var input bytes.Buffer
	if err := binary.Write(&input, binary.BigEndian, uint32(10)); err != nil {
		t.Fatal(err)
	}
	input.WriteString("{}")
	if err := ReadFrame(&input, &Request{}, 64); err == nil {
		t.Fatal("ReadFrame() succeeded for a truncated payload")
	}
}
