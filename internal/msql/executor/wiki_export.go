package executor

import (
	"context"
	"path/filepath"

	"github.com/HW-Yue/Memora/internal/msql/ast"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/wikiexport"
)

func (engine *Engine) executeWikiExport(
	ctx context.Context,
	statement *ast.ExportStatement,
	bound bindings,
) (Output, error) {
	if engine.wiki == nil || statement.Format != "WIKI" {
		return Output{}, executeError(result.CodeUnsupported, "Wiki export requires an autocommit export session")
	}
	root, err := packageText(statement.Path, bound, "Wiki export path")
	if err != nil {
		return Output{}, err
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return Output{}, executeError(result.CodeValidation, "Wiki export path must be absolute and normalized")
	}
	profileJSON, err := packageText(statement.Profile, bound, "Wiki Export Profile")
	if err != nil {
		return Output{}, err
	}
	profile, err := wikiexport.DecodeProfile([]byte(profileJSON))
	if err != nil {
		return Output{}, normalizeError(err)
	}
	manifest, err := engine.wiki.Export(ctx, root, profile)
	if err != nil {
		return Output{}, normalizeError(err)
	}
	return Output{
		Columns: packageColumns("path", "snapshot_sha256", "profile_sha256", "object_count"),
		Rows: []result.Row{{
			"path": root, "snapshot_sha256": manifest.SnapshotSHA256,
			"profile_sha256": manifest.ProfileSHA256, "object_count": int64(len(manifest.Objects)),
		}},
	}, nil
}
