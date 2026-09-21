package cli

import "testing"

// The language carries a batch as several statements, and each statement may
// need its own parameters, mutation and authorization. The CLI has to accept
// that shape: "batch = a batch of statements" is only true if a client can send
// one, and the Admin gateway has always done it as an array.
func TestStatementInputsAcceptOneObjectOrAList(t *testing.T) {
	one, err := decodeStatementInputs(`{"parameters":{"named":{"unit":7}}}`)
	if err != nil || len(one) != 1 || one[0].Parameters.Named["unit"] == nil {
		t.Fatalf("one object: %+v, %v", one, err)
	}
	list, err := decodeStatementInputs(`[
		{"parameters":{"named":{"unit":7}},"mutation":{"max_affected_rows":1}},
		{"parameters":{"named":{"unit":8}},"mutation":{"max_affected_rows":1}}
	]`)
	if err != nil || len(list) != 2 {
		t.Fatalf("a list must decode into one input per statement: %+v, %v", list, err)
	}
	if list[1].Parameters.Named["unit"] == nil || list[0].Mutation.MaxAffectedRows != 1 {
		t.Fatalf("each statement keeps its own input: %+v", list)
	}

	// Both shapes stay strict: an unknown field is a mistake, not something to
	// ignore, and trailing content is not a second input.
	for _, malformed := range []string{
		`{"parameters":{"named":{}},"unexpected":1}`,
		`[{"parameters":{"named":{}},"unexpected":1}]`,
		`[]`,
		`{"parameters":{"named":{}}} {"parameters":{"named":{}}}`,
		``,
		`[{"parameters":{"named":{}}}`,
	} {
		if _, err := decodeStatementInputs(malformed); err == nil {
			t.Fatalf("%q must be refused", malformed)
		}
	}
}
