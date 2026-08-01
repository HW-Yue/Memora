package routetrace

import (
	"errors"
	"testing"
)

func TestTraceCodecRejectsUnknownTrailingAndNoncanonicalJSON(t *testing.T) {
	value, err := seal(draft(1), 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := encodeTrace(value)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeTrace(encoded)
	if err != nil || decoded.Checksum != value.Checksum {
		t.Fatalf("decodeTrace() = %#v, %v", decoded, err)
	}
	unknown := append(encoded[:len(encoded)-1], []byte(`,"unknown":true}`)...)
	trailing := append(append([]byte(nil), encoded...), []byte(`{}`)...)
	noncanonical := append([]byte(" "), encoded...)
	tampered := append([]byte(nil), encoded...)
	tampered[len(tampered)/2] ^= 1
	for number, candidate := range [][]byte{encoded[:len(encoded)-1], unknown, trailing, noncanonical, tampered} {
		if _, err := decodeTrace(candidate); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("corrupt trace corpus[%d] error = %v", number, err)
		}
	}
}

func TestStoreStateCodecRejectsTamper(t *testing.T) {
	encoded, err := encodeStoreState(storeState{
		Version: storeVersion, HighWater: 7, RetentionEpoch: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeStoreState(encoded)
	if err != nil || decoded.HighWater != 7 || decoded.RetentionEpoch != 3 {
		t.Fatalf("decodeStoreState() = %#v, %v", decoded, err)
	}
	encoded[len(encoded)/2] ^= 1
	if _, err := decodeStoreState(encoded); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered state error = %v", err)
	}
}
