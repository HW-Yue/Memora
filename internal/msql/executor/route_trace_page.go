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
	routeTraceCursorVersion = "memora.route-trace-cursor/v1"
	routeTraceTimelineKind  = "trace_timeline"
	routeTraceStepsKind     = "trace_steps"
	routeTraceCursorMaximum = 4096
)

type routeTraceCursor struct {
	Version        string `json:"version"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	HighWater      uint64 `json:"high_water"`
	RetentionEpoch uint64 `json:"retention_epoch"`
	Position       uint64 `json:"position"`
	TraceID        string `json:"trace_id"`
	TraceChecksum  string `json:"trace_checksum"`
	Offset         uint64 `json:"offset"`
	Checksum       string `json:"checksum"`
}

type routeTraceCursorCore struct {
	Version        string `json:"version"`
	Kind           string `json:"kind"`
	Scope          string `json:"scope"`
	HighWater      uint64 `json:"high_water"`
	RetentionEpoch uint64 `json:"retention_epoch"`
	Position       uint64 `json:"position"`
	TraceID        string `json:"trace_id"`
	TraceChecksum  string `json:"trace_checksum"`
	Offset         uint64 `json:"offset"`
}

func encodeRouteTraceCursor(core routeTraceCursorCore) (string, error) {
	checksum, err := routeTraceCursorChecksum(core)
	if err != nil {
		return "", err
	}
	encoded, err := json.Marshal(routeTraceCursor{
		Version: core.Version, Kind: core.Kind, Scope: core.Scope,
		HighWater: core.HighWater, RetentionEpoch: core.RetentionEpoch,
		Position: core.Position, TraceID: core.TraceID, TraceChecksum: core.TraceChecksum,
		Offset: core.Offset, Checksum: checksum,
	})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeRouteTraceCursor(value string) (routeTraceCursor, error) {
	encoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(encoded) == 0 || len(encoded) > routeTraceCursorMaximum {
		return routeTraceCursor{}, fmt.Errorf("invalid Route Trace cursor encoding")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var decoded routeTraceCursor
	if err := decoder.Decode(&decoded); err != nil {
		return routeTraceCursor{}, fmt.Errorf("invalid Route Trace cursor payload")
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return routeTraceCursor{}, fmt.Errorf("invalid Route Trace cursor trailing data")
	}
	canonical, err := json.Marshal(decoded)
	if err != nil || !bytes.Equal(canonical, encoded) || !validRouteTraceCursor(decoded) {
		return routeTraceCursor{}, fmt.Errorf("invalid or non-canonical Route Trace cursor")
	}
	want, err := routeTraceCursorChecksum(routeTraceCursorCore{
		Version: decoded.Version, Kind: decoded.Kind, Scope: decoded.Scope,
		HighWater: decoded.HighWater, RetentionEpoch: decoded.RetentionEpoch,
		Position: decoded.Position, TraceID: decoded.TraceID,
		TraceChecksum: decoded.TraceChecksum, Offset: decoded.Offset,
	})
	if err != nil || subtle.ConstantTimeCompare([]byte(decoded.Checksum), []byte(want)) != 1 {
		return routeTraceCursor{}, fmt.Errorf("invalid Route Trace cursor checksum")
	}
	return decoded, nil
}

func validRouteTraceCursor(value routeTraceCursor) bool {
	if value.Version != routeTraceCursorVersion || !strings.HasPrefix(value.Scope, "sha256:") ||
		len(value.Scope) != len("sha256:")+64 || value.Checksum == "" {
		return false
	}
	switch value.Kind {
	case routeTraceTimelineKind:
		return value.HighWater > 0 && value.RetentionEpoch > 0 && value.Position > 0 &&
			value.Position <= value.HighWater && value.TraceID == "" && value.TraceChecksum == "" && value.Offset == 0
	case routeTraceStepsKind:
		return value.HighWater > 0 && value.RetentionEpoch == 0 && value.Position == 0 &&
			value.TraceID != "" && len(value.TraceChecksum) == 64 && value.Offset > 0
	default:
		return false
	}
}

func routeTraceCursorChecksum(core routeTraceCursorCore) (string, error) {
	encoded, err := json.Marshal(core)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func routeTraceScope(kind, databaseID string) string {
	digest := sha256.Sum256([]byte(kind + "\x00" + databaseID))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func routeTraceSnapshot(scope string, highWater, retentionEpoch uint64, checksum string) string {
	encoded, _ := json.Marshal(struct {
		Scope          string `json:"scope"`
		HighWater      uint64 `json:"high_water"`
		RetentionEpoch uint64 `json:"retention_epoch"`
		Checksum       string `json:"checksum"`
	}{scope, highWater, retentionEpoch, checksum})
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}
