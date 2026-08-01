package snapshot

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/store"
)

func FilterDatabase(encoded []byte, name string) ([]byte, catalog.Database, error) {
	value, err := decodeDocument(encoded)
	if err != nil {
		return nil, catalog.Database{}, err
	}
	if _, err := prepare(value); err != nil {
		return nil, catalog.Database{}, err
	}
	var catalogValue struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	if json.Unmarshal(value.Catalog, &catalogValue) != nil {
		return nil, catalog.Database{}, snapshotError(result.CodeValidation, "logical snapshot Catalog is invalid")
	}
	var selected catalog.Database
	for _, database := range catalogValue.Databases {
		if strings.EqualFold(database.Name, name) || database.ID == name {
			if selected.ID != "" {
				return nil, catalog.Database{}, snapshotError(result.CodeRevisionConflict, "Database selector is ambiguous")
			}
			selected = database
		}
	}
	if selected.ID == "" {
		return nil, catalog.Database{}, snapshotError(result.CodeNotFound, "Database was not found")
	}
	tables := map[string]bool{}
	for _, table := range selected.Tables {
		tables[table.ID] = true
	}
	filtered := document{
		Version: Version, Rows: []json.RawMessage{}, History: []json.RawMessage{},
		RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{}, Unknown: map[string]json.RawMessage{},
	}
	filtered.Catalog, _ = json.Marshal(struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}{Version: catalog.Version, Databases: []catalog.Database{selected}})
	rows := map[string]bool{}
	for _, raw := range value.Rows {
		var header rowHeader
		_ = json.Unmarshal(raw, &header)
		if header.DatabaseID == selected.ID && tables[header.TableID] {
			filtered.Rows = append(filtered.Rows, raw)
			rows[header.TableID+"\x00"+header.ID] = true
			filtered.CommitSequence = max(filtered.CommitSequence, header.CommitSequence)
		}
	}
	for _, raw := range value.History {
		var record history.Record
		_ = json.Unmarshal(raw, &record)
		if record.DatabaseID == selected.ID && rows[record.TableID+"\x00"+record.RowID] {
			filtered.History = append(filtered.History, raw)
			filtered.CommitSequence = max(filtered.CommitSequence, record.CommitSequence)
		}
	}
	filterRelations := func(values []json.RawMessage) []json.RawMessage {
		filteredValues := []json.RawMessage{}
		for _, raw := range values {
			var relation struct {
				Source struct {
					DatabaseID string `json:"database_id"`
				} `json:"source"`
				Target struct {
					DatabaseID string `json:"database_id"`
				} `json:"target"`
				CommitSequence uint64 `json:"commit_sequence"`
			}
			_ = json.Unmarshal(raw, &relation)
			if relation.Source.DatabaseID == selected.ID && relation.Target.DatabaseID == selected.ID {
				filteredValues = append(filteredValues, raw)
				filtered.CommitSequence = max(filtered.CommitSequence, relation.CommitSequence)
			}
		}
		return filteredValues
	}
	filtered.RelationNow = filterRelations(value.RelationNow)
	filtered.RelationPast = filterRelations(value.RelationPast)
	encodedFiltered, err := encodeDocument(filtered)
	if err != nil {
		return nil, catalog.Database{}, err
	}
	if _, err := prepare(filtered); err != nil {
		return nil, catalog.Database{}, err
	}
	return encodedFiltered, selected, nil
}

type MergeOptions struct {
	ReadOnly                bool
	PackageSHA256           string
	PackageSnapshotSHA256   string
	PackageSignerKeyID      string
	ReplaceDatabaseID       string
	ReplaceForkBaseSnapshot string
}

func (service *Service) MergeImport(ctx context.Context, encoded []byte) error {
	return service.MergeImportWithOptions(ctx, encoded, MergeOptions{})
}

func (service *Service) MergeImportWithOptions(ctx context.Context, encoded []byte, options MergeOptions) error {
	incoming, err := decodeDocument(encoded)
	if err != nil {
		return err
	}
	prepared, err := prepare(incoming)
	if err != nil {
		return err
	}
	var incomingCatalog struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	if json.Unmarshal(incoming.Catalog, &incomingCatalog) != nil || len(incomingCatalog.Databases) != 1 {
		return snapshotError(result.CodeValidation, "merge import requires exactly one Database")
	}
	tx, err := service.store.Begin(ctx, store.ReadWrite)
	if err != nil {
		return stableError(err)
	}
	defer func() { _ = tx.Rollback() }()
	var currentCatalog struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	currentCatalog.Version = catalog.Version
	if current, getErr := tx.Get(ctx, catalogBucket, catalogKey); getErr == nil {
		if json.Unmarshal(current, &currentCatalog) != nil {
			return snapshotError(result.CodeInternal, "existing Catalog is corrupt")
		}
	} else if !errors.Is(getErr, store.ErrNotFound) {
		return stableError(getErr)
	}
	candidate := incomingCatalog.Databases[0]
	candidate.ReadOnly = options.ReadOnly
	candidate.PackageSHA256 = options.PackageSHA256
	candidate.PackageSnapshotSHA256 = options.PackageSnapshotSHA256
	candidate.PackageSignerKeyID = options.PackageSignerKeyID
	existingTableIDs := map[string]bool{}
	nextDatabases := make([]catalog.Database, 0, len(currentCatalog.Databases)+1)
	replaced := false
	for _, existing := range currentCatalog.Databases {
		if options.ReplaceDatabaseID != "" && existing.ID == options.ReplaceDatabaseID {
			allowedReplacement := existing.ReadOnly ||
				(options.ReplaceForkBaseSnapshot != "" && existing.ForkedFromSnapshotSHA256 == options.ReplaceForkBaseSnapshot)
			if !allowedReplacement || candidate.ID != existing.ID || !strings.EqualFold(candidate.Name, existing.Name) {
				return snapshotError(result.CodePermissionDenied, "only the matching installed read-only Database can be replaced")
			}
			if err := removeDatabaseAuthority(ctx, tx, existing); err != nil {
				return err
			}
			replaced = true
			continue
		}
		if existing.ID == candidate.ID || strings.EqualFold(existing.Name, candidate.Name) {
			return snapshotError(result.CodeAlreadyExists, "Database identity or name already exists")
		}
		nextDatabases = append(nextDatabases, existing)
		for _, table := range existing.Tables {
			existingTableIDs[table.ID] = true
		}
	}
	if options.ReplaceDatabaseID != "" && !replaced {
		return snapshotError(result.CodeRevisionConflict, "installed Database changed before package replacement")
	}
	for _, table := range candidate.Tables {
		if existingTableIDs[table.ID] {
			return snapshotError(result.CodeAlreadyExists, "Table identity already exists")
		}
	}
	currentCatalog.Databases = append(nextDatabases, candidate)
	mergedCatalog, _ := json.Marshal(currentCatalog)
	if err := tx.Put(ctx, catalogBucket, catalogKey, mergedCatalog); err != nil {
		return stableError(err)
	}
	for index, raw := range incoming.Rows {
		header := prepared.rows[index]
		if err := tx.Put(ctx, rowPrefix+header.TableID, header.ID, raw); err != nil {
			return stableError(err)
		}
	}
	for tableID, ids := range prepared.rowIDs {
		if err := putJSON(ctx, tx, rowIndex, tableID, ids); err != nil {
			return err
		}
	}
	for index, raw := range incoming.History {
		header := prepared.history[index]
		if err := tx.Put(ctx, historyRows, historyRecordKey(header), raw); err != nil {
			return stableError(err)
		}
	}
	for key, revisions := range prepared.historyRevisions {
		if err := putJSON(ctx, tx, historyIndex, key, struct {
			Version   string   `json:"version"`
			Revisions []uint64 `json:"revisions"`
		}{history.Version, revisions}); err != nil {
			return err
		}
	}
	for index, raw := range incoming.RelationNow {
		current := prepared.relationNow[index]
		if _, getErr := tx.Get(ctx, relationNow, current.ID); getErr == nil {
			return snapshotError(result.CodeAlreadyExists, "Relation identity already exists")
		} else if !errors.Is(getErr, store.ErrNotFound) {
			return stableError(getErr)
		}
		if err := tx.Put(ctx, relationNow, current.ID, raw); err != nil {
			return stableError(err)
		}
	}
	for index, raw := range incoming.RelationPast {
		version := prepared.relationPast[index]
		if err := tx.Put(ctx, relationPast, relationVersionKey(version.ID, version.Revision), raw); err != nil {
			return stableError(err)
		}
	}
	for key, ids := range prepared.relationOutgoing {
		if err := putRelationIndex(ctx, tx, relationOut, key, ids); err != nil {
			return err
		}
	}
	for key, ids := range prepared.relationIncoming {
		if err := putRelationIndex(ctx, tx, relationIn, key, ids); err != nil {
			return err
		}
	}
	currentSequence, err := loadCommitSequence(ctx, tx)
	if err != nil {
		return err
	}
	if prepared.commitSequence < currentSequence {
		prepared.commitSequence = currentSequence
	}
	if err := putJSON(ctx, tx, historyMeta, historyKey, struct {
		Version string `json:"version"`
		Value   uint64 `json:"value"`
	}{history.Version, prepared.commitSequence}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return stableError(err)
	}
	return nil
}

func removeDatabaseAuthority(ctx context.Context, tx store.Tx, database catalog.Database) error {
	tableIDs := make(map[string]bool, len(database.Tables))
	for _, table := range database.Tables {
		tableIDs[table.ID] = true
		entries, err := tx.Scan(ctx, rowPrefix+table.ID)
		if err != nil {
			return stableError(err)
		}
		for _, entry := range entries {
			if err := tx.Delete(ctx, rowPrefix+table.ID, entry.Key); err != nil {
				return stableError(err)
			}
		}
		if err := tx.Delete(ctx, rowIndex, table.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return stableError(err)
		}
	}
	for _, bucket := range []string{historyRows, historyIndex} {
		entries, err := tx.Scan(ctx, bucket)
		if err != nil {
			return stableError(err)
		}
		for _, entry := range entries {
			tableID := strings.SplitN(entry.Key, "\x00", 2)[0]
			if tableIDs[tableID] {
				if err := tx.Delete(ctx, bucket, entry.Key); err != nil {
					return stableError(err)
				}
			}
		}
	}
	for _, bucket := range []string{relationNow, relationPast, relationOut, relationIn} {
		entries, err := tx.Scan(ctx, bucket)
		if err != nil {
			return stableError(err)
		}
		for _, entry := range entries {
			remove := strings.HasPrefix(entry.Key, database.ID+"\x00")
			if bucket == relationNow || bucket == relationPast {
				var value struct {
					Source struct {
						DatabaseID string `json:"database_id"`
					} `json:"source"`
					Target struct {
						DatabaseID string `json:"database_id"`
					} `json:"target"`
				}
				remove = json.Unmarshal(entry.Value, &value) == nil &&
					(value.Source.DatabaseID == database.ID || value.Target.DatabaseID == database.ID)
			}
			if remove {
				if err := tx.Delete(ctx, bucket, entry.Key); err != nil {
					return stableError(err)
				}
			}
		}
	}
	return nil
}
