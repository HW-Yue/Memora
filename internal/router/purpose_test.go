package router_test

import (
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/router"
)

// The one comparison behind both halves of the rule: refusing a new Route and
// reporting an existing one must agree on what "the purpose only repeats the
// name" means, or a writer refused at creation could reach the same state by a
// rename and never be told.
func TestPurposeRepeatsNameComparesAfterFolding(t *testing.T) {
	repeats := []struct{ name, purpose string }{
		{"storage", "storage"},
		{"storage", "  storage  "}, // padding is not content
		{"storage", "STORAGE"},     // nor is case
		{"storage", "ｓｔｏｒａｇｅ"},     // nor is width
		{"向量 rekey", "向量　rekey"},   // an ideographic space is still a space
		{"storage", ""},            // nothing written is the same absence
		{"storage", "   "},         // and so is whitespace standing in for it
	}
	for _, sample := range repeats {
		if !router.PurposeRepeatsName(sample.name, sample.purpose) {
			t.Fatalf("purpose %q must count as a repeat of name %q", sample.purpose, sample.name)
		}
	}

	descriptions := []struct{ name, purpose string }{
		{"storage", "为什么存储层是 SQLite"},
		{"storage", "storage engine choices and why"},
		{"向量 rekey", "换了 embedding 模型或维度之后怎么把整库向量换过去"},
		{"wal", "预写日志"},
	}
	for _, sample := range descriptions {
		if router.PurposeRepeatsName(sample.name, sample.purpose) {
			t.Fatalf("purpose %q describes name %q and must be accepted", sample.purpose, sample.name)
		}
	}
}

// The refusal is the rule's own text: a caller told only "invalid" answers by
// adding a space to the same word.
func TestCheckPurposeQuotesTheRule(t *testing.T) {
	if err := router.CheckPurpose("storage", "为什么存储层是 SQLite"); err != nil {
		t.Fatalf("a described Route must be accepted: %v", err)
	}
	err := router.CheckPurpose("storage", "Storage")
	if err == nil {
		t.Fatal("a purpose that repeats the name must be refused")
	}
	for _, expected := range []string{"purpose", "storage"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("the refusal must name %q: %s", expected, err.Error())
		}
	}
}
