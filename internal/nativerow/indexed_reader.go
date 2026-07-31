package nativerow

import (
	"context"
	"errors"
	"fmt"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

type IndexedCatalog interface {
	DescribeTable(context.Context, string, string) (catalog.Table, error)
}

type CurrentLookup interface {
	Lookup(string, string) (currentrowindex.Locator, error)
}

type VersionLookup interface {
	ByRevision(string, uint64) (rowversionindex.Locator, error)
	AsOfCommit(string, uint64) (rowversionindex.Locator, error)
}

// IndexedReader implements the strict F102 point-read lane. Once selected it
// never consults the legacy Catalog or Row enumeration paths.
type IndexedReader struct {
	repository *Repository
	catalog    IndexedCatalog
	current    CurrentLookup
	versions   VersionLookup
}

func NewIndexedReader(
	repository *Repository,
	dictionary IndexedCatalog,
	current CurrentLookup,
	versions VersionLookup,
) (*IndexedReader, error) {
	if repository == nil || repository.file == nil || dictionary == nil || current == nil || versions == nil {
		return nil, fmt.Errorf("%w: Row repository, Catalog, and indexes are required", ErrInvalid)
	}
	return &IndexedReader{
		repository: repository, catalog: dictionary, current: current, versions: versions,
	}, nil
}

func (reader *IndexedReader) DescribeTable(
	ctx context.Context,
	databaseName, tableName string,
) (catalog.Table, error) {
	if reader == nil || reader.catalog == nil {
		return catalog.Table{}, fmt.Errorf("%w: indexed Row reader", ErrInvalid)
	}
	return reader.catalog.DescribeTable(ctx, databaseName, tableName)
}

func (reader *IndexedReader) Get(
	ctx context.Context,
	table catalog.Table,
	rowID string,
) (row.Row, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return row.Row{}, err
	}
	current, err := reader.current.Lookup(table.ID, rowID)
	if err != nil {
		if errors.Is(err, currentrowindex.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return row.Row{}, err
	}
	version, err := reader.versions.ByRevision(rowID, current.Revision)
	if err != nil {
		if errors.Is(err, rowversionindex.ErrNotFound) {
			return row.Row{}, fmt.Errorf("%w: current Row has no immutable revision", ErrCorrupt)
		}
		return row.Row{}, err
	}
	if !currentMatchesVersion(current, version) || !locatorMatchesTable(version, table) {
		return row.Row{}, fmt.Errorf("%w: current and immutable Row locators disagree", ErrCorrupt)
	}
	value, err := reader.readBody(table, version)
	if err != nil {
		return row.Row{}, err
	}
	if value.State == row.StateDeleted || value.State == row.StateSuperseded {
		return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), nil)
	}
	return project(table, value), nil
}

func (reader *IndexedReader) AsOfRevision(
	ctx context.Context,
	table catalog.Table,
	rowID string,
	revision uint64,
) (row.Row, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return row.Row{}, err
	}
	locator, err := reader.versions.ByRevision(rowID, revision)
	if err != nil {
		return row.Row{}, versionLookupError(err, "historical Row revision was not found")
	}
	if !locatorMatchesTable(locator, table) {
		return row.Row{}, serviceFailure(result.CodeNotFound, "historical Row was not found in requested table", nil)
	}
	value, err := reader.readBody(table, locator)
	if err != nil {
		return row.Row{}, err
	}
	return project(table, value), nil
}

func (reader *IndexedReader) AsOfCommit(
	ctx context.Context,
	table catalog.Table,
	rowID string,
	commitSequence uint64,
) (row.Row, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return row.Row{}, err
	}
	locator, err := reader.versions.AsOfCommit(rowID, commitSequence)
	if err != nil {
		return row.Row{}, versionLookupError(err, "no Row history is visible at the requested commit sequence")
	}
	if !locatorMatchesTable(locator, table) {
		return row.Row{}, serviceFailure(result.CodeNotFound, "historical Row was not found in requested table", nil)
	}
	value, err := reader.readBody(table, locator)
	if err != nil {
		return row.Row{}, err
	}
	return project(table, value), nil
}

func (reader *IndexedReader) readBody(
	table catalog.Table,
	locator rowversionindex.Locator,
) (row.Row, error) {
	value, err := reader.repository.ReadRevisionWithTable(locator.RowID, locator.Revision, table)
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, fmt.Errorf("%w: indexed Row body is missing", ErrCorrupt)
		}
		return row.Row{}, err
	}
	if value.DatabaseID != locator.DatabaseID ||
		value.TableID != locator.TableID ||
		value.ID != locator.RowID ||
		value.SchemaVersion != locator.SchemaRevision ||
		value.Revision != locator.Revision ||
		value.CommitSequence != locator.CommitSequence ||
		value.State != locator.State {
		return row.Row{}, fmt.Errorf("%w: Row locator and body disagree", ErrCorrupt)
	}
	return value, nil
}

func versionLookupError(err error, message string) error {
	if errors.Is(err, rowversionindex.ErrNotFound) {
		return serviceFailure(result.CodeNotFound, message, err)
	}
	return err
}

func validateIndexedTable(ctx context.Context, table catalog.Table) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if table.ID == "" || table.DatabaseID == "" || table.SchemaVersion == 0 {
		return fmt.Errorf("%w: indexed Table identity", ErrInvalid)
	}
	return nil
}

func locatorMatchesTable(locator rowversionindex.Locator, table catalog.Table) bool {
	return locator.DatabaseID == table.DatabaseID &&
		locator.TableID == table.ID &&
		locator.SchemaRevision == table.SchemaVersion
}

func currentMatchesVersion(
	current currentrowindex.Locator,
	version rowversionindex.Locator,
) bool {
	return current.DatabaseID == version.DatabaseID &&
		current.TableID == version.TableID &&
		current.RowID == version.RowID &&
		current.SchemaRevision == version.SchemaRevision &&
		current.Revision == version.Revision &&
		current.CommitSequence == version.CommitSequence &&
		current.State == version.State
}
