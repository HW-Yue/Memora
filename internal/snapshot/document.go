package snapshot

import (
	"bytes"
	"encoding/json"

	"github.com/HW-Yue/Memora/internal/result"
)

func decodeDocument(encoded []byte) (document, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var fields map[string]json.RawMessage
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return document{}, snapshotError(result.CodeValidation, "logical snapshot JSON is invalid")
	}
	var version string
	if json.Unmarshal(fields["version"], &version) != nil {
		return document{}, snapshotError(result.CodeValidation, "logical snapshot version is missing")
	}
	if version != Version && version != legacyVersion {
		return document{}, snapshotError(result.CodeValidation, "logical snapshot version is unsupported")
	}
	value := document{
		Version: Version, Catalog: cloneRaw(fields["catalog"]),
		Rows: []json.RawMessage{}, History: []json.RawMessage{},
		RelationNow: []json.RawMessage{}, RelationPast: []json.RawMessage{},
		Unknown: map[string]json.RawMessage{},
	}
	if len(value.Catalog) == 0 ||
		decodeRawArray(fields["rows"], &value.Rows) != nil ||
		decodeRawArray(fields["history"], &value.History) != nil {
		return document{}, snapshotError(result.CodeValidation, "logical snapshot authority sections are invalid")
	}
	if raw := fields["commit_sequence"]; len(raw) > 0 {
		if json.Unmarshal(raw, &value.CommitSequence) != nil {
			return document{}, snapshotError(result.CodeValidation, "logical snapshot commit sequence is invalid")
		}
	}
	if version == legacyVersion {
		if decodeRawArray(fields["relations"], &value.RelationNow) != nil {
			return document{}, snapshotError(result.CodeValidation, "legacy snapshot relations are invalid")
		}
		value.RelationPast = append(value.RelationPast, cloneRaws(value.RelationNow)...)
	} else {
		var relations struct {
			Current  []json.RawMessage `json:"current"`
			Versions []json.RawMessage `json:"versions"`
		}
		if json.Unmarshal(fields["relations"], &relations) != nil {
			return document{}, snapshotError(result.CodeValidation, "logical snapshot relations are invalid")
		}
		if relations.Current != nil {
			value.RelationNow = relations.Current
		}
		if relations.Versions != nil {
			value.RelationPast = relations.Versions
		}
	}
	known := map[string]struct{}{
		"version": {}, "catalog": {}, "rows": {}, "history": {},
		"relations": {}, "commit_sequence": {},
	}
	for key, raw := range fields {
		if _, exists := known[key]; !exists {
			value.Unknown[key] = cloneRaw(raw)
		}
	}
	return value, nil
}

func encodeDocument(value document) ([]byte, error) {
	fields := make(map[string]json.RawMessage, len(value.Unknown)+6)
	for key, raw := range value.Unknown {
		fields[key] = cloneRaw(raw)
	}
	version, _ := json.Marshal(Version)
	rows, err := json.Marshal(value.Rows)
	if err != nil {
		return nil, snapshotError(result.CodeInternal, "logical snapshot Rows could not be encoded")
	}
	historyRows, err := json.Marshal(value.History)
	if err != nil {
		return nil, snapshotError(result.CodeInternal, "logical snapshot History could not be encoded")
	}
	relations, err := json.Marshal(struct {
		Current  []json.RawMessage `json:"current"`
		Versions []json.RawMessage `json:"versions"`
	}{Current: value.RelationNow, Versions: value.RelationPast})
	if err != nil {
		return nil, snapshotError(result.CodeInternal, "logical snapshot Relations could not be encoded")
	}
	commitSequence, _ := json.Marshal(value.CommitSequence)
	fields["version"] = version
	fields["catalog"] = cloneRaw(value.Catalog)
	fields["rows"] = rows
	fields["history"] = historyRows
	fields["relations"] = relations
	fields["commit_sequence"] = commitSequence
	encoded, err := json.Marshal(fields)
	if err != nil {
		return nil, snapshotError(result.CodeInternal, "logical snapshot could not be encoded")
	}
	return encoded, nil
}

func decodeRawArray(encoded json.RawMessage, target *[]json.RawMessage) error {
	if len(encoded) == 0 {
		return nil
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		return err
	}
	if *target == nil {
		*target = []json.RawMessage{}
	}
	return nil
}

func cloneRaws(values []json.RawMessage) []json.RawMessage {
	clones := make([]json.RawMessage, 0, len(values))
	for _, value := range values {
		clones = append(clones, cloneRaw(value))
	}
	return clones
}
