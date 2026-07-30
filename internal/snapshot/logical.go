package snapshot

import "encoding/json"

// LogicalDocument is the backend-neutral authority carried by a logical snapshot.
// Raw messages preserve forward-compatible fields exactly across migrations.
type LogicalDocument struct {
	Catalog        json.RawMessage
	Rows           []json.RawMessage
	History        []json.RawMessage
	RelationNow    []json.RawMessage
	RelationPast   []json.RawMessage
	CommitSequence uint64
	Unknown        map[string]json.RawMessage
}

func DecodeLogical(encoded []byte) (LogicalDocument, error) {
	value, err := decodeDocument(encoded)
	if err != nil {
		return LogicalDocument{}, err
	}
	if _, err := prepare(value); err != nil {
		return LogicalDocument{}, err
	}
	return LogicalDocument{Catalog: value.Catalog, Rows: value.Rows, History: value.History, RelationNow: value.RelationNow, RelationPast: value.RelationPast, CommitSequence: value.CommitSequence, Unknown: value.Unknown}, nil
}

func EncodeLogical(value LogicalDocument) ([]byte, error) {
	internal := document{Version: Version, Catalog: value.Catalog, Rows: value.Rows, History: value.History, RelationNow: value.RelationNow, RelationPast: value.RelationPast, CommitSequence: value.CommitSequence, Unknown: value.Unknown}
	if _, err := prepare(internal); err != nil {
		return nil, err
	}
	return encodeDocument(internal)
}
