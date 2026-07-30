package hostharness_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/hostharness"
	"github.com/HW-Yue/Memora/internal/msql/executor"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	nativekvstore "github.com/HW-Yue/Memora/internal/store/nativekv"
	"github.com/HW-Yue/Memora/internal/testkit"
)

func TestReplayScriptedHostTranscriptWithInjectedError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	session, closeSession := hostSession(t, ctx)
	defer closeSession()
	invocations := 0
	tool := hostharness.ToolFunc(func(ctx context.Context, call hostharness.Call) (hostharness.Response, error) {
		invocations++
		envelope := session.Execute(ctx, executor.BatchRequest{
			RequestID:  call.ID,
			Source:     call.MSQL,
			Statements: []executor.StatementInput{call.Input},
		})
		return hostharness.Response{Envelope: &envelope}, nil
	})

	fixture, err := hostharness.LoadFile(filepath.Join("testdata", "update-after-injected-error.json"))
	if err != nil {
		t.Fatal(err)
	}
	report, err := hostharness.New(tool).Replay(ctx, fixture)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(report.Calls), 5; got != want {
		t.Fatalf("host calls = %d, want %d", got, want)
	}
	if got, want := report.Injected, 1; got != want {
		t.Fatalf("injected outcomes = %d, want %d", got, want)
	}
	if got, want := report.FinalAssertions, 1; got != want {
		t.Fatalf("final assertions = %d, want %d", got, want)
	}
	if got, want := invocations, 5; got != want {
		t.Fatalf("real tool invocations = %d, want %d", got, want)
	}
	if got, want := report.Replies, []string{
		"Saved the refined routing decision as row_one revision 2 after a transient retry.",
	}; len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("replies = %#v, want %#v", got, want)
	}
}

func TestFixtureValidationRejectsToolSequenceDrift(t *testing.T) {
	t.Parallel()

	fixture, err := hostharness.LoadFile(filepath.Join("testdata", "update-after-injected-error.json"))
	if err != nil {
		t.Fatal(err)
	}
	fixture.ExpectedToolSequence[2].Input.Mutation.ExpectedRevision = 99
	if err := fixture.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want expected sequence drift")
	}
}

func TestFixtureLoaderRejectsUnknownFieldsAndQueryMutations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{
			name: "unknown field",
			content: `{
				"version":"memora.host-transcript/v1",
				"name":"unknown",
				"turns":[],
				"expected_tool_sequence":[],
				"final_assertions":[],
				"surprise":true
			}`,
			want: "unknown field",
		},
		{
			name: "mutation through query",
			content: `{
				"version":"memora.host-transcript/v1",
				"name":"unsafe-query",
				"turns":[{
					"user":"delete it",
					"actions":[{
						"call":{"id":"unsafe","command":"query","msql":"DELETE FROM work.notes WHERE row_id = :row"},
						"expect":{"ok":false}
					}],
					"reply":"Not changed.",
					"reply_expect":{}
				}],
				"expected_tool_sequence":[{
					"id":"unsafe",
					"command":"query",
					"msql":"DELETE FROM work.notes WHERE row_id = :row"
				}],
				"final_assertions":[]
			}`,
			want: "contains mutation",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "fixture.json")
			if err := os.WriteFile(path, []byte(test.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := hostharness.LoadFile(path)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("LoadFile() error = %v, want containing %q", err, test.want)
			}
			var stable interface{ StableCode() string }
			if !errors.As(err, &stable) || stable.StableCode() != string(result.CodeValidation) {
				t.Fatalf("LoadFile() error = %v, want stable validation code", err)
			}
		})
	}
}

func TestReplayValidatesTransportErrorsAndUserReply(t *testing.T) {
	t.Parallel()

	ok := true
	fixture := hostharness.Fixture{
		Version: hostharness.Version,
		Name:    "transport-recovery",
		Turns: []hostharness.Turn{{
			User: "read it",
			Actions: []hostharness.Action{{
				Call: hostharness.Call{ID: "offline", Command: "query", MSQL: "SHOW DATABASES"},
				Expect: hostharness.Expectation{
					ErrorContains: "daemon unavailable",
				},
			}},
			Reply: "The local database is unavailable; no result was claimed.",
			ReplyExpect: hostharness.ReplyExpectation{
				Contains:      []string{"unavailable"},
				Excludes:      []string{"saved"},
				MaxCharacters: 2000,
			},
		}},
		ExpectedToolSequence: []hostharness.Call{{
			ID: "offline", Command: "query", MSQL: "SHOW DATABASES",
		}},
	}
	tool := hostharness.ToolFunc(func(context.Context, hostharness.Call) (hostharness.Response, error) {
		return hostharness.Response{}, errors.New("daemon unavailable")
	})
	report, err := hostharness.New(tool).Replay(context.Background(), fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Replies) != 1 {
		t.Fatalf("replies = %#v", report.Replies)
	}

	fixture.Turns[0].Actions[0].Expect = hostharness.Expectation{OK: &ok}
	if _, err := hostharness.New(tool).Replay(context.Background(), fixture); err == nil {
		t.Fatal("Replay() error = nil, want unexpected transport error")
	}
}

func hostSession(t *testing.T, ctx context.Context) (*executor.BatchSession, func()) {
	t.Helper()
	database, err := nativekvstore.Open(filepath.Join(t.TempDir(), "host-harness.db"))
	if err != nil {
		t.Fatal(err)
	}
	dictionary := catalog.New(database, catalog.Options{
		IDs: testkit.NewFakeIDs("db_work", "table_notes", "column_title"),
	})
	if _, err := dictionary.CreateDatabase(ctx, catalog.DatabaseDefinition{
		Name: "work", Purpose: "Work knowledge", Scope: "Reviewed project decisions",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := dictionary.CreateTable(ctx, "work", catalog.TableDefinition{
		Name: "notes", Purpose: "Project decisions", RowSemantics: "One durable decision",
		Columns: []catalog.ColumnDefinition{{
			Name: "title", Type: "TEXT(200)", Purpose: "Decision title",
		}},
	}); err != nil {
		t.Fatal(err)
	}
	rows := row.New(database, dictionary, row.Options{
		IDs: testkit.NewFakeIDs("one"),
	})
	session := executor.NewBatchSession(ctx, dictionary, rows)
	return session, func() {
		_ = session.Close()
		if err := database.Close(); err != nil {
			t.Errorf("close test database: %v", err)
		}
	}
}
