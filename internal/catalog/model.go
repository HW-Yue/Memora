package catalog

import (
	"time"

	"github.com/HW-Yue/Memora/internal/logical"
)

const Version = "memora.catalog/v1"

type DatabaseDefinition struct {
	Name      string `json:"name"`
	Purpose   string `json:"purpose"`
	Scope     string `json:"scope"`
	AntiScope string `json:"anti_scope,omitempty"`
}

type TableDefinition struct {
	Name         string             `json:"name"`
	Purpose      string             `json:"purpose"`
	Scope        string             `json:"scope,omitempty"`
	AntiScope    string             `json:"anti_scope,omitempty"`
	RowSemantics string             `json:"row_semantics"`
	Columns      []ColumnDefinition `json:"columns"`
}

type ColumnDefinition struct {
	Name         string `json:"name"`
	Type         string `json:"type"`
	Nullable     bool   `json:"nullable"`
	Purpose      string `json:"purpose"`
	SemanticRole string `json:"semantic_role,omitempty"`
}

type Database struct {
	ID                    string    `json:"database_id"`
	Name                  string    `json:"name"`
	Aliases               []string  `json:"aliases"`
	Purpose               string    `json:"purpose"`
	Scope                 string    `json:"scope"`
	AntiScope             string    `json:"anti_scope,omitempty"`
	SchemaVersion         uint64    `json:"schema_version"`
	ReadOnly              bool      `json:"read_only,omitempty"`
	PackageSHA256         string    `json:"package_sha256,omitempty"`
	PackageSnapshotSHA256 string    `json:"package_snapshot_sha256,omitempty"`
	PackageSignerKeyID    string    `json:"package_signer_key_id,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
	UpdatedAt             time.Time `json:"updated_at"`
	Tables                []Table   `json:"tables"`
}

type Table struct {
	ID              string          `json:"table_id"`
	DatabaseID      string          `json:"database_id"`
	Name            string          `json:"name"`
	Aliases         []string        `json:"aliases"`
	Purpose         string          `json:"purpose"`
	Scope           string          `json:"scope,omitempty"`
	AntiScope       string          `json:"anti_scope,omitempty"`
	RowSemantics    string          `json:"row_semantics"`
	SchemaVersion   uint64          `json:"schema_version"`
	CreatedAt       time.Time       `json:"created_at"`
	UpdatedAt       time.Time       `json:"updated_at"`
	Columns         []Column        `json:"columns"`
	ColumnSummaries []ColumnSummary `json:"column_summaries,omitempty"`
}

type ColumnSummary struct {
	ID            string `json:"column_id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	MaxCharacters int    `json:"max_characters,omitempty"`
	Nullable      bool   `json:"nullable"`
	Purpose       string `json:"purpose"`
	SemanticRole  string `json:"semantic_role,omitempty"`
}

type Column struct {
	ID            string    `json:"column_id"`
	Name          string    `json:"name"`
	Aliases       []string  `json:"aliases"`
	Type          string    `json:"type"`
	MaxCharacters int       `json:"max_characters,omitempty"`
	Nullable      bool      `json:"nullable"`
	Purpose       string    `json:"purpose"`
	SemanticRole  string    `json:"semantic_role,omitempty"`
	SchemaVersion uint64    `json:"schema_version"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

func (column Column) Validate(value any) (any, error) {
	return logical.Validate(logical.Constraint{
		Name: column.Name,
		Definition: logical.Definition{
			Kind: logical.Kind(column.Type), MaxCharacters: column.MaxCharacters,
		},
		Nullable: column.Nullable,
	}, value)
}

type snapshot struct {
	Version   string     `json:"version"`
	Databases []Database `json:"databases"`
}
