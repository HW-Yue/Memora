package recall

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// A vector travels as one thing and one thing only: base64 (raw URL-safe) of
// little-endian IEEE-754 float32, packed with no padding bytes between values.
//
// The form is fixed rather than left to the implementation because a decode that
// is merely *different* produces a perfectly valid vector — valid paths for the
// wrong places, with nothing in a recall answer to show it. The language has no
// array type, so a list of numbers would also have to become a language feature;
// bytes do not.
//
// Callers compute the vector (the engine never does), so normalisation and
// quantisation stay on the host: the engine receives the final bits and cannot
// silently round them twice, because recall returns no scores and a last-bit
// difference near a tie could change the answer set with no signal at all.
const (
	// VectorEncoding is the name this form is documented and refused under.
	VectorEncoding = "base64(raw-url) of little-endian float32"
)

// EncodeVector packs a vector for the wire, or for a statement parameter.
func EncodeVector(vector []float32) (string, error) {
	if len(vector) == 0 {
		return "", errors.New("a vector cannot be empty")
	}
	for position, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return "", fmt.Errorf("a vector cannot carry NaN or infinity (position %d)", position)
		}
	}
	buffer := new(bytes.Buffer)
	if err := binary.Write(buffer, binary.LittleEndian, vector); err != nil {
		return "", fmt.Errorf("encode vector: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer.Bytes()), nil
}

// DecodeVector reads a vector back, refusing anything that is not the documented
// form. Strict decoding rejects non-canonical base64, so two spellings of the
// same bytes cannot both be accepted.
func DecodeVector(encoded string) ([]float32, error) {
	if encoded == "" {
		return nil, errors.New("a vector cannot be empty")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("a vector must be %s", VectorEncoding)
	}
	if len(raw) == 0 || len(raw)%4 != 0 {
		return nil, fmt.Errorf("a vector must be a whole number of float32 values, got %d bytes", len(raw))
	}
	vector := make([]float32, len(raw)/4)
	if err := binary.Read(bytes.NewReader(raw), binary.LittleEndian, vector); err != nil {
		return nil, fmt.Errorf("read vector: %w", err)
	}
	for position, value := range vector {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return nil, fmt.Errorf("a vector cannot carry NaN or infinity (position %d)", position)
		}
	}
	return vector, nil
}
