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
	Page(string, string, uint64) (currentrowindex.CursorPage, error)
}

type VersionLookup interface {
	ByRevision(string, uint64) (rowversionindex.Locator, error)
	AsOfCommit(string, uint64) (rowversionindex.Locator, error)
	VisibleAt(string, uint64) (rowversionindex.Locator, error)
	HighWater() (uint64, error)
}

func (reader *IndexedReader) Capture(ctx context.Context) (uint64, error) {
	if reader == nil || reader.versions == nil {
		return 0, fmt.Errorf("%w: indexed Row reader", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return reader.versions.HighWater()
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
	snapshot uint64,
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
	version, err := reader.visibleLocator(table, current, snapshot)
	if err != nil {
		if errors.Is(err, rowversionindex.ErrNotFound) {
			return row.Row{}, serviceFailure(result.CodeNotFound, fmt.Sprintf("row %q was not found", rowID), err)
		}
		return row.Row{}, err
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

// CurrentIncludingDeleted resolves the current immutable body without hiding a
// tombstone. Mutation conflict checks use it instead of enumerating Row IDs.
func (reader *IndexedReader) CurrentIncludingDeleted(
	ctx context.Context, table catalog.Table, rowID string,
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
		return row.Row{}, err
	}
	if !currentMatchesVersion(current, version) || !locatorMatchesTable(version, table) {
		return row.Row{}, fmt.Errorf("%w: current and version Row locators disagree", ErrCorrupt)
	}
	value, err := reader.readBody(table, version)
	if err != nil {
		return row.Row{}, err
	}
	return value, nil
}

func (reader *IndexedReader) ListPage(
	ctx context.Context, table catalog.Table, limit int, snapshot uint64,
) ([]row.Row, bool, error) {
	if limit < 1 || limit > 1000 {
		return nil, false, fmt.Errorf("%w: limit must be between 1 and 1000", ErrInvalid)
	}
	view := &ReadView{
		reader: reader, sequence: snapshot, overlay: make(map[string]row.Row),
	}
	defer func() { _ = view.Discard() }()
	page, err := view.PageLocators(ctx, table, "", uint64(limit))
	if err != nil {
		return nil, false, err
	}
	values := make([]row.Row, 0, len(page.Locators))
	for _, locator := range page.Locators {
		value, err := reader.readBody(table, locator)
		if err != nil {
			return nil, false, err
		}
		if value.State == row.StateDeleted || value.State == row.StateSuperseded {
			continue
		}
		values = append(values, project(table, value))
	}
	return values, page.HasMore, nil
}

func (reader *IndexedReader) visibleLocator(
	table catalog.Table,
	current currentrowindex.Locator,
	snapshot uint64,
) (rowversionindex.Locator, error) {
	if current.DatabaseID != table.DatabaseID || current.TableID != table.ID {
		return rowversionindex.Locator{}, fmt.Errorf("%w: current Row locator has wrong Table", ErrCorrupt)
	}
	version, err := reader.versions.VisibleAt(current.RowID, snapshot)
	if err != nil {
		return rowversionindex.Locator{}, err
	}
	if !locatorMatchesTable(version, table) {
		return rowversionindex.Locator{}, fmt.Errorf("%w: visible Row locator has wrong Table", ErrCorrupt)
	}
	if current.CommitSequence == 0 || current.CommitSequence <= snapshot {
		if !currentMatchesVersion(current, version) {
			return rowversionindex.Locator{}, fmt.Errorf("%w: current and visible Row locators disagree", ErrCorrupt)
		}
	} else if (version.CommitSequence != 0 && version.CommitSequence > snapshot) ||
		version.Revision >= current.Revision {
		return rowversionindex.Locator{}, fmt.Errorf("%w: snapshot Row ordering", ErrCorrupt)
	}
	return version, nil
}

func (reader *IndexedReader) AsOfRevision(
	ctx context.Context,
	table catalog.Table,
	rowID string,
	revision uint64,
	snapshot uint64,
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
	if locator.CommitSequence != 0 && locator.CommitSequence > snapshot {
		return row.Row{}, serviceFailure(result.CodeNotFound, "historical Row revision is newer than the read snapshot", nil)
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
	snapshot uint64,
) (row.Row, error) {
	if err := validateIndexedTable(ctx, table); err != nil {
		return row.Row{}, err
	}
	if snapshot < commitSequence {
		commitSequence = snapshot
	}
	if commitSequence == 0 {
		return row.Row{}, serviceFailure(result.CodeNotFound, "no Row history is visible at the requested commit sequence", nil)
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

// clusteredBody prefers the Row the leaf carries and falls back to the record
// log only for revisions written before leaves carried bodies. A secondary key
// (commit, identity) stores no body by design, so its locator is resolved
// through the clustered key first.
func (reader *IndexedReader) clusteredBody(
	table catalog.Table,
	locator rowversionindex.Locator,
) (row.Row, error) {
	if locator.Body == "" {
		clustered, err := reader.versions.ByRevision(locator.RowID, locator.Revision)
		if err == nil {
			locator.Body = clustered.Body
		} else if !errors.Is(err, rowversionindex.ErrNotFound) {
			return row.Row{}, err
		}
	}
	if locator.Body != "" {
		value, err := RowFromLocator(locator, table)
		if err == nil {
			return value, nil
		}
		if !errors.Is(err, ErrNoBody) {
			return row.Row{}, err
		}
	}
	value, err := reader.repository.ReadRevisionWithTable(locator.RowID, locator.Revision, table)
	if err != nil {
		if errors.Is(err, nativestore.ErrNotFound) {
			return row.Row{}, fmt.Errorf("%w: indexed Row body is missing", ErrCorrupt)
		}
		return row.Row{}, err
	}
	return value, nil
}

func (reader *IndexedReader) readBody(
	table catalog.Table,
	locator rowversionindex.Locator,
) (row.Row, error) {
	// The clustered leaf carries the Row. When it does, reaching the leaf was
	// reaching the data and there is nothing further to resolve. The identity
	// cross-check below still runs: RowFromLocator has already verified the body
	// against its key, so the repeated check is cheap and keeps one guarantee in
	// one place for both paths.
	value, err := reader.clusteredBody(table, locator)
	if err != nil {
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

// locatorMatchesTable checks that an index entry belongs to this Table. It
// deliberately does not require the Row's recorded schema revision to equal
// the Table's current one: a Catalog change bumps the Table while existing
// Rows keep the revision they were written at, and the engine rewrites Rows
// only when a schema-change plan says RequiresRowRewrite. Demanding equality
// made every Row unreadable after any Catalog bump, a rename included.
// A revision ahead of the Table is still corrupt — no Row can conform to a
// schema that does not exist yet.
func locatorMatchesTable(locator rowversionindex.Locator, table catalog.Table) bool {
	return locator.DatabaseID == table.DatabaseID &&
		locator.TableID == table.ID &&
		locator.SchemaRevision != 0 &&
		locator.SchemaRevision <= table.SchemaVersion
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
