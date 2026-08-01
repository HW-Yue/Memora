package executor

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestRouteTraceCursorRejectsTamperUnknownTrailingAndNoncanonical(t *testing.T) {
	t.Parallel()
	core := routeTraceCursorCore{
		Version: routeTraceCursorVersion, Kind: routeTraceTimelineKind,
		Scope:     routeTraceScope(routeTraceTimelineKind, "db_work"),
		HighWater: 9, RetentionEpoch: 3, Position: 4,
	}
	encoded, err := encodeRouteTraceCursor(core)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRouteTraceCursor(encoded)
	if err != nil || decoded.HighWater != 9 || decoded.RetentionEpoch != 3 || decoded.Position != 4 {
		t.Fatalf("decodeRouteTraceCursor() = %#v, %v", decoded, err)
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
		if _, err := decodeRouteTraceCursor(base64.RawURLEncoding.EncodeToString(candidate)); err == nil {
			t.Fatalf("corrupt cursor corpus[%d] succeeded", number)
		}
	}
	if _, err := decodeRouteTraceCursor(encoded + "="); err == nil {
		t.Fatal("padded cursor succeeded")
	}
}

func TestRouteTraceStepCursorBindsTraceChecksum(t *testing.T) {
	t.Parallel()
	core := routeTraceCursorCore{
		Version: routeTraceCursorVersion, Kind: routeTraceStepsKind,
		Scope: routeTraceScope(routeTraceStepsKind, "db_work"), HighWater: 7,
		TraceID: "trace_" + strings.Repeat("a", 32), TraceChecksum: strings.Repeat("b", 64), Offset: 2,
	}
	encoded, err := encodeRouteTraceCursor(core)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeRouteTraceCursor(encoded)
	if err != nil || decoded.TraceID != core.TraceID || decoded.TraceChecksum != core.TraceChecksum || decoded.Offset != 2 {
		t.Fatalf("trace step cursor = %#v, %v", decoded, err)
	}
}
