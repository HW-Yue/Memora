package parser_test

import (
	"reflect"
	"testing"

	"github.com/HW-Yue/Memora/internal/msql/parser"
)

func TestAssimilationMSQLParsesReviewSubmitAndReceipt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		source     string
		wantKind   string
		parameters []string
	}{
		{
			source:   "REVIEW ASSIMILATION FOR DATABASE work USING :proposal",
			wantKind: "REVIEW_ASSIMILATION", parameters: []string{"proposal"},
		},
		{
			source:   "SUBMIT ASSIMILATION PLAN :plan FOR DATABASE work",
			wantKind: "SUBMIT_ASSIMILATION", parameters: []string{"plan"},
		},
		{
			source:   "SHOW ASSIMILATION RECEIPT :receipt IN DATABASE work",
			wantKind: "SHOW_ASSIMILATION_RECEIPT", parameters: []string{"receipt"},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.wantKind, func(t *testing.T) {
			document, err := parser.Parse(test.source)
			if err != nil {
				t.Fatal(err)
			}
			if document.Statement.Kind != test.wantKind {
				t.Fatalf("kind = %q, want %q", document.Statement.Kind, test.wantKind)
			}
			parameters := document.Parameters()
			got := make([]string, len(parameters))
			for index := range parameters {
				got[index] = parameters[index].Name
			}
			if !reflect.DeepEqual(got, test.parameters) {
				t.Fatalf("parameters = %#v, want %#v", got, test.parameters)
			}
		})
	}
}

func TestAssimilationMSQLRejectsAmbiguousOrIncompleteForms(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"REVIEW ASSIMILATION USING :proposal",
		"REVIEW ASSIMILATION FOR DATABASE work :proposal",
		"SUBMIT ASSIMILATION :plan FOR DATABASE work",
		"SUBMIT ASSIMILATION PLAN :plan",
		"SHOW ASSIMILATION RECEIPT :receipt",
		"SHOW ASSIMILATION RECEIPT :receipt IN work",
	} {
		if _, err := parser.Parse(source); err == nil {
			t.Fatalf("Parse(%q) unexpectedly succeeded", source)
		}
	}
}
