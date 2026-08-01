package executor

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/dbpackage"
	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
)

func (engine *Engine) executePackage(
	ctx context.Context,
	statement *ast.PackageStatement,
	bound bindings,
) (Output, error) {
	if engine.packages == nil {
		return Output{}, executeError(result.CodeUnsupported, "database package operations require an autocommit package session")
	}
	switch statement.Action {
	case "PACK":
		if len(statement.Database.Parts) != 1 {
			return Output{}, executeError(result.CodeValidation, "PACK DATABASE requires one unqualified Database name")
		}
		author, err := packageText(statement.Author, bound, "package author")
		if err != nil {
			return Output{}, err
		}
		if err := engine.authorizeDatabaseReference(ctx, statement.Database.Parts[0].Value); err != nil {
			return Output{}, err
		}
		encoded, manifest, err := engine.packages.Pack(ctx, statement.Database.Parts[0].Value, author)
		if err != nil {
			return Output{}, normalizeError(err)
		}
		return Output{
			Columns: packageColumns("package", "package_sha256", "database_id", "name", "snapshot_sha256"),
			Rows: []result.Row{{
				"package": string(encoded), "package_sha256": dbpackage.Hash(encoded),
				"database_id": manifest.DatabaseID, "name": manifest.Name,
				"snapshot_sha256": manifest.SnapshotSHA256,
			}},
		}, nil
	case "OPEN":
		encoded, err := packageText(statement.Value, bound, "package value")
		if err != nil {
			return Output{}, err
		}
		opened, err := engine.packages.Open([]byte(encoded))
		if err != nil {
			return Output{}, normalizeError(err)
		}
		return Output{
			Columns: packageColumns("database_id", "name", "package_sha256", "snapshot_sha256", "read_only"),
			Rows: []result.Row{{
				"database_id": opened.Manifest.DatabaseID, "name": opened.Manifest.Name,
				"package_sha256":  opened.PackageSHA256,
				"snapshot_sha256": opened.Manifest.SnapshotSHA256, "read_only": true,
			}},
		}, nil
	case "INSTALL":
		encoded, err := packageText(statement.Value, bound, "package value")
		if err != nil {
			return Output{}, err
		}
		if err := security.RequireApproval(
			ctx, security.ActionInstallPackage, dbpackage.Hash([]byte(encoded)),
		); err != nil {
			return Output{}, normalizeError(err)
		}
		receipt, err := engine.packages.Install(ctx, []byte(encoded), dbpackage.InstallOptions{Trusted: statement.Trusted})
		if err != nil {
			return Output{}, normalizeError(err)
		}
		return Output{
			Columns: packageColumns("database_id", "name", "package_sha256", "snapshot_sha256", "read_only", "signer_key_id", "signature_verified"),
			Rows: []result.Row{{
				"database_id": receipt.DatabaseID, "name": receipt.Name,
				"package_sha256":  receipt.PackageSHA256,
				"snapshot_sha256": receipt.SnapshotSHA256,
				"read_only":       receipt.ReadOnly, "signer_key_id": receipt.SignerKeyID,
				"signature_verified": receipt.SignatureVerified,
			}},
			AffectedRows: 1,
		}, nil
	default:
		return Output{}, executeError(result.CodeUnsupported, "unknown database package action")
	}
}

func packageText(expression *ast.Expression, bound bindings, label string) (string, error) {
	value, err := evaluate(expression, emptyTable(), nil, bound)
	if err != nil {
		return "", err
	}
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", executeError(result.CodeValidation, label+" must be a non-empty TEXT value")
	}
	return text, nil
}

func emptyTable() (value catalog.Table) { return value }

func packageColumns(names ...string) []result.Column {
	columns := make([]result.Column, 0, len(names))
	for _, name := range names {
		typeName := "TEXT"
		if name == "read_only" || name == "signature_verified" {
			typeName = "BOOLEAN"
		} else if name == "object_count" {
			typeName = "INTEGER"
		}
		columns = append(columns, result.Column{Name: name, Type: typeName})
	}
	return columns
}
