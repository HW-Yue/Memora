package routetrace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
)

const storeVersion = "memora.route-trace-store/v1"

type storeState struct {
	Version        string `json:"version"`
	HighWater      uint64 `json:"high_water"`
	RetentionEpoch uint64 `json:"retention_epoch"`
	Checksum       string `json:"checksum"`
}

func encodeTrace(value Trace) ([]byte, error) {
	if value.Validate() != nil {
		return nil, ErrInvalid
	}
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > maxTraceBytes {
		return nil, ErrInvalid
	}
	return encoded, nil
}

func decodeTrace(encoded []byte) (Trace, error) {
	if len(encoded) == 0 || len(encoded) > maxTraceBytes {
		return Trace{}, ErrCorrupt
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value Trace
	if err := decoder.Decode(&value); err != nil {
		return Trace{}, ErrCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Trace{}, ErrCorrupt
	}
	canonical, err := json.Marshal(value)
	if err != nil || !bytes.Equal(canonical, encoded) || value.Validate() != nil {
		return Trace{}, ErrCorrupt
	}
	return value, nil
}

func defaultStoreState() storeState {
	return storeState{Version: storeVersion, RetentionEpoch: 1}
}

func encodeStoreState(value storeState) ([]byte, error) {
	if value.Version != storeVersion || value.RetentionEpoch == 0 {
		return nil, ErrCorrupt
	}
	checksum, err := storeStateChecksum(value)
	if err != nil {
		return nil, err
	}
	value.Checksum = checksum
	return json.Marshal(value)
}

func decodeStoreState(encoded []byte) (storeState, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value storeState
	if len(encoded) == 0 || decoder.Decode(&value) != nil {
		return storeState{}, ErrCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return storeState{}, ErrCorrupt
	}
	canonical, err := json.Marshal(value)
	want, checksumErr := storeStateChecksum(value)
	if err != nil || checksumErr != nil || !bytes.Equal(canonical, encoded) ||
		value.Version != storeVersion || value.RetentionEpoch == 0 || value.Checksum != want {
		return storeState{}, ErrCorrupt
	}
	return value, nil
}

func storeStateChecksum(value storeState) (string, error) {
	encoded, err := json.Marshal(struct {
		Version        string `json:"version"`
		HighWater      uint64 `json:"high_water"`
		RetentionEpoch uint64 `json:"retention_epoch"`
	}{value.Version, value.HighWater, value.RetentionEpoch})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func bodyKey(sequence uint64) string { return fmt.Sprintf("trace_%020d", sequence) }
