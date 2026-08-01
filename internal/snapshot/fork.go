package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/row"
)

type ForkOptions struct {
	DatabaseID     string
	Name           string
	SourcePackage  string
	SourceSnapshot string
}

func ForkDatabase(encoded []byte, options ForkOptions) ([]byte, catalog.Database, error) {
	if strings.TrimSpace(options.DatabaseID) == "" || strings.TrimSpace(options.Name) == "" ||
		strings.TrimSpace(options.SourceSnapshot) == "" {
		return nil, catalog.Database{}, snapshotError(result.CodeValidation, "fork target identity and source snapshot are required")
	}
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
	if json.Unmarshal(value.Catalog, &catalogValue) != nil || len(catalogValue.Databases) != 1 {
		return nil, catalog.Database{}, snapshotError(result.CodeValidation, "fork requires one Database snapshot")
	}
	source := catalogValue.Databases[0]
	tableIDs, columnIDs := map[string]string{}, map[string]string{}
	target := source
	target.ID, target.Name, target.Aliases = options.DatabaseID, options.Name, []string{}
	target.ReadOnly = false
	target.ForkedFromDatabaseID, target.ForkedFromPackageSHA256 = source.ID, options.SourcePackage
	target.ForkedFromSnapshotSHA256 = options.SourceSnapshot
	target.PackageSHA256, target.PackageSnapshotSHA256, target.PackageSignerKeyID = "", "", ""
	target.Tables = append([]catalog.Table(nil), source.Tables...)
	for tableIndex := range target.Tables {
		table := &target.Tables[tableIndex]
		tableIDs[table.ID] = forkID("tbl", options.DatabaseID, table.ID)
		table.ID, table.DatabaseID = tableIDs[table.ID], options.DatabaseID
		table.Columns = append([]catalog.Column(nil), table.Columns...)
		for columnIndex := range table.Columns {
			column := &table.Columns[columnIndex]
			columnIDs[column.ID] = forkID("col", options.DatabaseID, column.ID)
			column.ID = columnIDs[column.ID]
		}
		table.ColumnSummaries = append([]catalog.ColumnSummary(nil), table.ColumnSummaries...)
		for summaryIndex := range table.ColumnSummaries {
			table.ColumnSummaries[summaryIndex].ID = columnIDs[table.ColumnSummaries[summaryIndex].ID]
		}
	}
	catalogValue.Databases = []catalog.Database{target}
	value.Catalog, _ = json.Marshal(catalogValue)
	remapValues := func(values map[string]any) map[string]any {
		remapped := make(map[string]any, len(values))
		for key, item := range values {
			if mapped := columnIDs[key]; mapped != "" {
				key = mapped
			}
			remapped[key] = item
		}
		return remapped
	}
	for index, raw := range value.Rows {
		var item row.Row
		if json.Unmarshal(raw, &item) != nil {
			return nil, catalog.Database{}, snapshotError(result.CodeValidation, "fork Row is invalid")
		}
		item.DatabaseID, item.TableID = options.DatabaseID, tableIDs[item.TableID]
		item.Values = remapValues(item.Values)
		value.Rows[index], _ = json.Marshal(item)
	}
	for index, raw := range value.History {
		var item history.Record
		if json.Unmarshal(raw, &item) != nil {
			return nil, catalog.Database{}, snapshotError(result.CodeValidation, "fork History is invalid")
		}
		item.DatabaseID, item.TableID = options.DatabaseID, tableIDs[item.TableID]
		item.Values = remapValues(item.Values)
		value.History[index], _ = json.Marshal(item)
	}
	relationIDs := map[string]string{}
	remapRelation := func(raw json.RawMessage) (json.RawMessage, error) {
		var item relation.Relation
		if json.Unmarshal(raw, &item) != nil {
			return nil, snapshotError(result.CodeValidation, "fork Relation is invalid")
		}
		mapped := relationIDs[item.ID]
		if mapped == "" {
			mapped = forkID("rel", options.DatabaseID, item.ID)
			relationIDs[item.ID] = mapped
		}
		item.ID = mapped
		item.Source.DatabaseID, item.Target.DatabaseID = options.DatabaseID, options.DatabaseID
		item.Source.TableID, item.Target.TableID = tableIDs[item.Source.TableID], tableIDs[item.Target.TableID]
		return json.Marshal(item)
	}
	for index, raw := range value.RelationNow {
		value.RelationNow[index], err = remapRelation(raw)
		if err != nil {
			return nil, catalog.Database{}, err
		}
	}
	for index, raw := range value.RelationPast {
		value.RelationPast[index], err = remapRelation(raw)
		if err != nil {
			return nil, catalog.Database{}, err
		}
	}
	if _, err := prepare(value); err != nil {
		return nil, catalog.Database{}, err
	}
	resultBytes, err := encodeDocument(value)
	if err != nil {
		return nil, catalog.Database{}, err
	}
	return resultBytes, target, nil
}

func forkID(prefix, targetDatabaseID, sourceID string) string {
	sum := sha256.Sum256([]byte(targetDatabaseID + "\x00" + sourceID))
	return prefix + "_" + hex.EncodeToString(sum[:12])
}
