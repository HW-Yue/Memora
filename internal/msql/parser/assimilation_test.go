package parser

import "testing"

func TestParseRejectsRetiredAssimilation(t *testing.T) {
	for _, source := range []string{
		"REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
		"SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work",
		"SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
