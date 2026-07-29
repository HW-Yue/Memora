package parser

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCatalogStatementsGolden(t *testing.T) {
	t.Parallel()

	sources := []string{
		"CREATE DATABASE work PURPOSE 'Project knowledge' SCOPE 'Active projects' ANTI SCOPE 'Personal journals'",
		"CREATE TABLE work.notes PURPOSE 'Durable notes' SCOPE 'Reviewed knowledge' ANTI SCOPE 'Raw documents' ROW SEMANTICS 'One reviewed note' (title TEXT NOT NULL PURPOSE 'Display title', body TEXT(1200) PURPOSE 'Complete note')",
		"SHOW COLUMNS FROM work.notes COMPACT",
		"DESCRIBE COLUMN work.notes.title COMPACT",
		"ALTER DATABASE work RENAME TO projects",
		"ALTER TABLE projects.notes RENAME TO knowledge",
		"ALTER TABLE projects.knowledge RENAME COLUMN title TO heading",
		"ALTER TABLE projects.knowledge ADD COLUMN status TEXT NULL PURPOSE 'Workflow state'",
	}
	var encoded strings.Builder
	for _, source := range sources {
		document, err := Parse(source)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", source, err)
		}
		line, err := json.Marshal(document)
		if err != nil {
			t.Fatalf("Marshal(%q) error = %v", source, err)
		}
		encoded.Write(line)
		encoded.WriteByte('\n')
	}
	want, err := os.ReadFile(filepath.Join("testdata", "catalog_statements.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if encoded.String() != string(want) {
		t.Fatalf("catalog AST mismatch\n--- got ---\n%s--- want ---\n%s", encoded.String(), want)
	}
}

func TestParseCatalogMetadataReportsPreciseErrors(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"CREATE DATABASE work PURPOSE 7 SCOPE 'projects'",
		"CREATE DATABASE work PURPOSE 'work' PURPOSE 'duplicate' SCOPE 'projects'",
		"CREATE TABLE work.notes PURPOSE 'notes' ROW 'missing semantics' (body TEXT PURPOSE 'body')",
		"ALTER TABLE work.notes RENAME COLUMN title heading",
	} {
		if _, err := Parse(source); err == nil {
			t.Fatalf("Parse(%q) succeeded", source)
		}
	}
}
