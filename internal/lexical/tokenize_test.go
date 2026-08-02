package lexical

import (
	"errors"
	"reflect"
	"testing"
)

func TestTermsPreserveFrequencyAcrossLatinAndHanText(t *testing.T) {
	t.Parallel()

	got, err := Terms("Go GO,数据库数据库")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"go", "go", "数据", "据库", "库数", "数据", "据库"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Terms() = %#v, want %#v", got, want)
	}
}

func TestUniqueTermsPreservesFirstOccurrenceOrder(t *testing.T) {
	t.Parallel()

	got, err := UniqueTerms("Beta beta ALPHA 数据数据库")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"beta", "alpha", "数据", "据数", "据库"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("UniqueTerms() = %#v, want %#v", got, want)
	}
}

func TestTermsRejectInvalidUTF8(t *testing.T) {
	t.Parallel()

	if _, err := Terms(string([]byte{0xff})); !errors.Is(err, ErrInvalidText) {
		t.Fatalf("Terms() error = %v, want %v", err, ErrInvalidText)
	}
}
