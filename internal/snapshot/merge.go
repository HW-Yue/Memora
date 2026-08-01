package snapshot

import (
	"bytes"
	"encoding/json"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/result"
)

type MergeForkOptions struct {
	SourceDatabaseID string
	IncomingPackage  string
	IncomingSnapshot string
}

func MergeFork(baseEncoded, forkEncoded, incomingEncoded []byte, options MergeForkOptions) ([]byte, []string, error) {
	base, err := checkedDocument(baseEncoded)
	if err != nil {
		return nil, nil, err
	}
	local, err := checkedDocument(forkEncoded)
	if err != nil {
		return nil, nil, err
	}
	incoming, err := checkedDocument(incomingEncoded)
	if err != nil {
		return nil, nil, err
	}
	conflicts := []string{}
	selectedCatalog, conflict, err := mergeCatalog(base.Catalog, local.Catalog, incoming.Catalog, options)
	if err != nil {
		return nil, nil, err
	}
	if conflict {
		conflicts = append(conflicts, "SCHEMA")
	}
	rows, rowChoices, rowConflicts := mergeRawObjects(base.Rows, local.Rows, incoming.Rows, rawRowID)
	for _, id := range rowConflicts {
		conflicts = append(conflicts, "ROW:"+id)
	}
	relations, relationChoices, relationConflicts := mergeRawObjects(base.RelationNow, local.RelationNow, incoming.RelationNow, rawRelationID)
	for _, id := range relationConflicts {
		conflicts = append(conflicts, "RELATION:"+id)
	}
	sort.Strings(conflicts)
	if len(conflicts) > 0 {
		return nil, conflicts, nil
	}
	merged := document{
		Version: Version, Catalog: selectedCatalog, Rows: rows,
		History:     chooseHistory(local.History, incoming.History, rowChoices),
		RelationNow: relations, RelationPast: chooseRelationHistory(local.RelationPast, incoming.RelationPast, relationChoices),
		CommitSequence: max(local.CommitSequence, incoming.CommitSequence), Unknown: local.Unknown,
	}
	if _, err := prepare(merged); err != nil {
		return nil, nil, err
	}
	encoded, err := encodeDocument(merged)
	if err != nil {
		return nil, nil, err
	}
	return encoded, nil, nil
}

func checkedDocument(encoded []byte) (document, error) {
	value, err := decodeDocument(encoded)
	if err != nil {
		return document{}, err
	}
	if _, err := prepare(value); err != nil {
		return document{}, err
	}
	return value, nil
}

func mergeCatalog(baseRaw, localRaw, incomingRaw json.RawMessage, options MergeForkOptions) (json.RawMessage, bool, error) {
	_, baseComparable, err := forkCatalog(baseRaw)
	if err != nil {
		return nil, false, err
	}
	local, localComparable, err := forkCatalog(localRaw)
	if err != nil {
		return nil, false, err
	}
	incoming, incomingComparable, err := forkCatalog(incomingRaw)
	if err != nil {
		return nil, false, err
	}
	choice := local
	switch {
	case bytes.Equal(localComparable, baseComparable):
		choice = incoming
	case bytes.Equal(incomingComparable, baseComparable), bytes.Equal(localComparable, incomingComparable):
		choice = local
	default:
		return nil, true, nil
	}
	database := &choice.Databases[0]
	database.ReadOnly = false
	database.PackageSHA256, database.PackageSnapshotSHA256, database.PackageSignerKeyID = "", "", ""
	database.ForkedFromDatabaseID = options.SourceDatabaseID
	database.ForkedFromPackageSHA256 = options.IncomingPackage
	database.ForkedFromSnapshotSHA256 = options.IncomingSnapshot
	encoded, err := json.Marshal(choice)
	if err != nil {
		return nil, false, snapshotError(result.CodeInternal, "merged fork Catalog could not be encoded")
	}
	return encoded, false, nil
}

type forkCatalogEnvelope struct {
	Version   string             `json:"version"`
	Databases []catalog.Database `json:"databases"`
}

func forkCatalog(raw json.RawMessage) (forkCatalogEnvelope, []byte, error) {
	var value forkCatalogEnvelope
	if json.Unmarshal(raw, &value) != nil || value.Version != catalog.Version || len(value.Databases) != 1 {
		return value, nil, snapshotError(result.CodeValidation, "fork merge Catalog is invalid")
	}
	comparable := value
	database := &comparable.Databases[0]
	database.ReadOnly = false
	database.PackageSHA256, database.PackageSnapshotSHA256, database.PackageSignerKeyID = "", "", ""
	database.ForkedFromDatabaseID, database.ForkedFromPackageSHA256, database.ForkedFromSnapshotSHA256 = "", "", ""
	encoded, _ := json.Marshal(comparable)
	return value, encoded, nil
}

type rawKey func(json.RawMessage) (string, error)

func mergeRawObjects(base, local, incoming []json.RawMessage, key rawKey) ([]json.RawMessage, map[string]string, []string) {
	b, bErr := rawMap(base, key)
	l, lErr := rawMap(local, key)
	i, iErr := rawMap(incoming, key)
	if bErr != nil || lErr != nil || iErr != nil {
		return nil, nil, []string{"INVALID"}
	}
	keys := map[string]bool{}
	for id := range b {
		keys[id] = true
	}
	for id := range l {
		keys[id] = true
	}
	for id := range i {
		keys[id] = true
	}
	ordered := make([]string, 0, len(keys))
	for id := range keys {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	resultValues := []json.RawMessage{}
	choices, conflicts := map[string]string{}, []string{}
	for _, id := range ordered {
		baseValue, localValue, incomingValue := b[id], l[id], i[id]
		choice, ok := threeWay(baseValue, localValue, incomingValue)
		if !ok {
			conflicts = append(conflicts, id)
			continue
		}
		choices[id] = choice
		var selected json.RawMessage
		if choice == "incoming" {
			selected = incomingValue
		} else {
			selected = localValue
		}
		if selected != nil {
			resultValues = append(resultValues, cloneRaw(selected))
		}
	}
	return resultValues, choices, conflicts
}

func threeWay(base, local, incoming []byte) (string, bool) {
	switch {
	case bytes.Equal(local, base):
		return "incoming", true
	case bytes.Equal(incoming, base), bytes.Equal(local, incoming):
		return "local", true
	default:
		return "", false
	}
}

func rawMap(values []json.RawMessage, key rawKey) (map[string]json.RawMessage, error) {
	resultValues := make(map[string]json.RawMessage, len(values))
	for _, raw := range values {
		id, err := key(raw)
		if err != nil || id == "" {
			return nil, snapshotError(result.CodeValidation, "fork merge object is invalid")
		}
		resultValues[id] = raw
	}
	return resultValues, nil
}

func rawRowID(raw json.RawMessage) (string, error) {
	var value struct {
		ID      string `json:"row_id"`
		TableID string `json:"table_id"`
	}
	err := json.Unmarshal(raw, &value)
	return value.TableID + "/" + value.ID, err
}

func rawRelationID(raw json.RawMessage) (string, error) {
	var value struct {
		ID string `json:"relation_id"`
	}
	err := json.Unmarshal(raw, &value)
	return value.ID, err
}

func chooseHistory(local, incoming []json.RawMessage, choices map[string]string) []json.RawMessage {
	resultValues := []json.RawMessage{}
	for _, source := range []struct {
		name   string
		values []json.RawMessage
	}{{"local", local}, {"incoming", incoming}} {
		for _, raw := range source.values {
			var value history.Record
			if json.Unmarshal(raw, &value) == nil && choices[value.TableID+"/"+value.RowID] == source.name {
				resultValues = append(resultValues, cloneRaw(raw))
			}
		}
	}
	return resultValues
}

func chooseRelationHistory(local, incoming []json.RawMessage, choices map[string]string) []json.RawMessage {
	resultValues := []json.RawMessage{}
	for _, source := range []struct {
		name   string
		values []json.RawMessage
	}{{"local", local}, {"incoming", incoming}} {
		for _, raw := range source.values {
			var value relation.Relation
			if json.Unmarshal(raw, &value) == nil && choices[value.ID] == source.name {
				resultValues = append(resultValues, cloneRaw(raw))
			}
		}
	}
	return resultValues
}
