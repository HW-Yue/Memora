package sqlstore_test

import (
	"context"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/sqlstore"
)

// A Database's three description fields are what a cold-start agent reads to
// decide where knowledge belongs, so "当前有效" wording is exactly the kind that
// goes stale: the fields cannot be welded in at creation. These tests pin the
// amend path's semantics — it writes only the fields it names, it moves the
// Database's schema version like every other catalog mutation, an empty
// `ANTI SCOPE ''` is how a field is cleared, and the result is on disk rather
// than in this process.

func TestAlterDatabaseDescriptionWritesOnlyNamedFields(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE '关于 Memora 的产品与仓库知识' SCOPE '现行原则、规格与风险' ANTI SCOPE '个人身份与求职'`,
		nil, executor.MutationOptions{})

	before := h.run(`DESCRIBE DATABASE work COMPACT`, nil, executor.MutationOptions{})
	version := text(before.Rows[0]["schema_version"])

	amended := h.run(`ALTER DATABASE work SET SCOPE '现行原则、规格、能力、风险与工作方向'`, nil, executor.MutationOptions{})
	if len(amended.Rows) != 1 {
		t.Fatalf("ALTER DATABASE returned %d rows, want the amended Database", len(amended.Rows))
	}
	row := amended.Rows[0]
	if got := text(row["purpose"]); got != "关于 Memora 的产品与仓库知识" {
		t.Fatalf("purpose = %q, want the unnamed field kept", got)
	}
	if got := text(row["scope"]); got != "现行原则、规格、能力、风险与工作方向" {
		t.Fatalf("scope = %q", got)
	}
	if got := text(row["anti_scope"]); got != "个人身份与求职" {
		t.Fatalf("anti_scope = %q, want the unnamed field kept", got)
	}
	if got := text(row["schema_version"]); got == version {
		t.Fatalf("schema_version stayed at %s: a description change is a catalog mutation", got)
	}

	read := h.run(`DESCRIBE DATABASE work COMPACT`, nil, executor.MutationOptions{})
	if got := text(read.Rows[0]["scope"]); got != "现行原则、规格、能力、风险与工作方向" {
		t.Fatalf("DESCRIBE scope = %q", got)
	}
	if got := text(read.Rows[0]["purpose"]); got != "关于 Memora 的产品与仓库知识" {
		t.Fatalf("DESCRIBE purpose = %q", got)
	}
}

func TestAlterDatabaseDescriptionSetsEveryFieldInOneStatement(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})

	amended := h.run(`ALTER DATABASE work SET ANTI SCOPE '个人身份、求职与个人项目经历归 me' SCOPE '收哪些' PURPOSE '是什么'`,
		nil, executor.MutationOptions{})
	row := amended.Rows[0]
	if got := text(row["purpose"]); got != "是什么" {
		t.Fatalf("purpose = %q", got)
	}
	if got := text(row["scope"]); got != "收哪些" {
		t.Fatalf("scope = %q", got)
	}
	if got := text(row["anti_scope"]); got != "个人身份、求职与个人项目经历归 me" {
		t.Fatalf("anti_scope = %q", got)
	}
}

func TestAlterDatabaseDescriptionClearsAntiScopeWithAnEmptyString(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's' ANTI SCOPE 'not this'`, nil, executor.MutationOptions{})

	cleared := h.run(`ALTER DATABASE work SET ANTI SCOPE ''`, nil, executor.MutationOptions{})
	if _, present := cleared.Rows[0]["anti_scope"]; present {
		t.Fatalf("anti_scope = %v, want it cleared", cleared.Rows[0]["anti_scope"])
	}
	if got := text(cleared.Rows[0]["scope"]); got != "s" {
		t.Fatalf("scope = %q, want it kept", got)
	}
}

func TestAlterDatabaseDescriptionRefusesEmptyRequiredFields(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})

	for _, source := range []string{
		`ALTER DATABASE work SET PURPOSE ''`,
		`ALTER DATABASE work SET PURPOSE '   '`,
		`ALTER DATABASE work SET SCOPE ''`,
	} {
		if code := h.fails(source, nil, executor.MutationOptions{}); code != result.CodeValidation {
			t.Fatalf("%s: code = %s, want %s", source, code, result.CodeValidation)
		}
	}

	// The refused statements changed nothing.
	read := h.run(`DESCRIBE DATABASE work COMPACT`, nil, executor.MutationOptions{})
	if got := text(read.Rows[0]["purpose"]); got != "p" {
		t.Fatalf("purpose = %q after refused amendments", got)
	}
}

func TestSetDatabaseDescriptionReportsAnUnknownDatabase(t *testing.T) {
	h := newHarness(t)
	scope := "anything"
	if _, err := h.db.SetDatabaseDescription(context.Background(), "absent",
		catalog.DatabaseDescription{Scope: &scope}); err == nil {
		t.Fatal("amending an unknown Database succeeded")
	}
}

func TestAlterDatabaseDescriptionSurvivesReopen(t *testing.T) {
	h := newHarness(t)
	h.run(`CREATE DATABASE work PURPOSE 'p' SCOPE 's'`, nil, executor.MutationOptions{})
	h.run(`ALTER DATABASE work SET PURPOSE '关于 Memora 的产品与仓库知识' ANTI SCOPE '个人身份与求职'`,
		nil, executor.MutationOptions{})

	// A second handle on the same file proves the description is on disk and not
	// in this process: the amendment has to be durable, not just visible.
	reopened, err := sqlstore.Open(h.path, sqlstore.Options{CheckInvariants: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	database, err := reopened.DescribeDatabase(context.Background(), "work")
	if err != nil {
		t.Fatal(err)
	}
	if database.Purpose != "关于 Memora 的产品与仓库知识" || database.Scope != "s" || database.AntiScope != "个人身份与求职" {
		t.Fatalf("reopened description = %+v", database)
	}
}
