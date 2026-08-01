package changeindex

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

func encodeLocator(locator Locator) ([]byte, error) {
	if err := validateLocator(locator); err != nil {
		return nil, err
	}
	return json.Marshal(locator)
}

func decodeLocator(encoded []byte) (Locator, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var locator Locator
	if err := decoder.Decode(&locator); err != nil {
		return Locator{}, fmt.Errorf("%w: locator encoding", ErrCorrupt)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Locator{}, fmt.Errorf("%w: locator trailing data", ErrCorrupt)
	}
	canonical, err := json.Marshal(locator)
	if err != nil || !bytes.Equal(canonical, encoded) || validateLocator(locator) != nil {
		return Locator{}, fmt.Errorf("%w: locator payload", ErrCorrupt)
	}
	return locator, nil
}
