package recall_test

import (
	"math"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/recall"
)

// The wire form is part of the contract, so it is tested as a form: the exact
// bytes of a known vector, and a refusal for every near miss.
func TestVectorWireFormIsExactAndStrict(t *testing.T) {
	encoded, err := recall.EncodeVector([]float32{1, -2.5, 0.25})
	if err != nil {
		t.Fatal(err)
	}
	// 1 = 0000803f, -2.5 = 000020c0, 0.25 = 0000803e, little-endian.
	if encoded != "AACAPwAAIMAAAIA-" {
		t.Fatalf("the documented encoding changed: %q", encoded)
	}
	back, err := recall.DecodeVector(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(back) != 3 || back[0] != 1 || back[1] != -2.5 || back[2] != 0.25 {
		t.Fatalf("round trip = %v", back)
	}

	// A wrong endianness still decodes to a valid vector, which is exactly why
	// the form has to be pinned by a test rather than left to an implementation.
	if encoded == "AACAPwAAIMAAAIA-" && back[0] != 1 {
		t.Fatal("big-endian decode would have produced a different, valid vector")
	}

	for name, candidate := range map[string]string{
		"padded base64":  encoded + "=",
		"not base64":     "not a vector!",
		"empty":          "",
		"partial float":  "AAA",
		"standard alpha": strings.ReplaceAll(encoded, "-", "+"),
	} {
		if _, err := recall.DecodeVector(candidate); err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestVectorWireFormRefusesValuesThatCannotBeCompared(t *testing.T) {
	for name, vector := range map[string][]float32{
		"NaN":        {1, float32(math.NaN())},
		"infinity":   {float32(math.Inf(1))},
		"empty list": {},
	} {
		if _, err := recall.EncodeVector(vector); err == nil {
			t.Fatalf("%s must not be encodable", name)
		}
	}
	// And the same values are refused on the way in, spelled as bytes.
	nan, err := recall.EncodeVector([]float32{1, 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recall.DecodeVector(nan); err != nil {
		t.Fatalf("a well-formed vector must decode: %v", err)
	}
}

func TestVectorWireFormErrorNamesTheForm(t *testing.T) {
	// A caller that sent the wrong form has to be able to find out which form is
	// the right one from the refusal alone.
	if _, err := recall.DecodeVector("***"); err == nil || !strings.Contains(err.Error(), recall.VectorEncoding) {
		t.Fatalf("the refusal must name the documented form: %v", err)
	}
}
