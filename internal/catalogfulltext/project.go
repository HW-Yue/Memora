package catalogfulltext

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
)

var ErrInvalid = errors.New("Catalog fulltext projection is invalid")

func Project(databases []catalog.Database) ([]fulltext.Document, error) {
	ordered := append([]catalog.Database(nil), databases...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	documents := make([]fulltext.Document, 0)
	seen := make(map[string]struct{})
	for _, database := range ordered {
		if err := requireText("Database", database.ID, database.Name, database.Purpose, database.Scope); err != nil {
			return nil, err
		}
		if err := addIdentity(seen, fulltext.KindDatabase, database.ID); err != nil {
			return nil, err
		}
		document := liveDocument(
			fulltext.KindDatabase, database.ID, "", database.ID, database.SchemaVersion,
			[]fulltext.Field{
				textField("name", database.Name),
				textValuesField("aliases", database.Aliases),
				textField("purpose", database.Purpose),
				textField("scope", database.Scope),
				textField("anti_scope", database.AntiScope),
			},
		)
		if err := validate(document); err != nil {
			return nil, fmt.Errorf("%w: Database %q: %v", ErrInvalid, database.ID, err)
		}
		documents = append(documents, document)

		tables := append([]catalog.Table(nil), database.Tables...)
		sort.Slice(tables, func(left, right int) bool { return tables[left].ID < tables[right].ID })
		for _, table := range tables {
			if table.DatabaseID != database.ID {
				return nil, fmt.Errorf("%w: Table %q has a different Database", ErrInvalid, table.ID)
			}
			if err := requireText("Table", table.ID, table.Name, table.Purpose, table.RowSemantics); err != nil {
				return nil, err
			}
			if err := addIdentity(seen, fulltext.KindTable, table.ID); err != nil {
				return nil, err
			}
			document = liveDocument(
				fulltext.KindTable, database.ID, table.ID, table.ID, table.SchemaVersion,
				[]fulltext.Field{
					textField("name", table.Name),
					textValuesField("aliases", table.Aliases),
					textField("purpose", table.Purpose),
					textField("scope", table.Scope),
					textField("anti_scope", table.AntiScope),
					textField("row_semantics", table.RowSemantics),
				},
			)
			if err := validate(document); err != nil {
				return nil, fmt.Errorf("%w: Table %q: %v", ErrInvalid, table.ID, err)
			}
			documents = append(documents, document)

			columns := append([]catalog.Column(nil), table.Columns...)
			sort.Slice(columns, func(left, right int) bool { return columns[left].ID < columns[right].ID })
			for _, column := range columns {
				if err := requireText("Column", column.ID, column.Name, column.Type, column.Purpose); err != nil {
					return nil, err
				}
				if err := addIdentity(seen, fulltext.KindColumn, column.ID); err != nil {
					return nil, err
				}
				document = liveDocument(
					fulltext.KindColumn, database.ID, table.ID, column.ID, column.SchemaVersion,
					[]fulltext.Field{
						textField("name", column.Name),
						textValuesField("aliases", column.Aliases),
						textField("type", column.Type),
						textField("purpose", column.Purpose),
						textField("semantic_role", column.SemanticRole),
					},
				)
				if err := validate(document); err != nil {
					return nil, fmt.Errorf("%w: Column %q: %v", ErrInvalid, column.ID, err)
				}
				documents = append(documents, document)
			}
		}
	}
	return documents, nil
}

func Tombstone(object fulltext.Object) (fulltext.Document, error) {
	if (object.Kind != fulltext.KindDatabase && object.Kind != fulltext.KindTable && object.Kind != fulltext.KindColumn) ||
		object.State != fulltext.StateLive || object.Revision == 0 || object.Revision == math.MaxUint64 {
		return fulltext.Document{}, fmt.Errorf("%w: tombstone source", ErrInvalid)
	}
	document := fulltext.Document{
		Version: fulltext.DocumentVersion, Kind: object.Kind, DatabaseID: object.DatabaseID,
		TableID: object.TableID, ObjectID: object.ObjectID, Revision: object.Revision + 1,
		SchemaRevision: object.Revision + 1, State: fulltext.StateDeleted, Complete: true,
	}
	if err := validate(document); err != nil {
		return fulltext.Document{}, fmt.Errorf("%w: tombstone: %v", ErrInvalid, err)
	}
	return document, nil
}

func liveDocument(
	kind fulltext.ObjectKind,
	databaseID, tableID, objectID string,
	revision uint64,
	fields []fulltext.Field,
) fulltext.Document {
	result := fulltext.Document{
		Version: fulltext.DocumentVersion, Kind: kind, DatabaseID: databaseID, TableID: tableID,
		ObjectID: objectID, Revision: revision, SchemaRevision: revision,
		State: fulltext.StateLive, Complete: true,
	}
	for _, field := range fields {
		if len(field.Values) != 0 {
			result.Fields = append(result.Fields, field)
		}
	}
	return result
}

func textField(id, value string) fulltext.Field {
	if strings.TrimSpace(value) == "" {
		return fulltext.Field{}
	}
	return fulltext.Field{ID: id, Values: []fulltext.Value{fulltext.TextValue(value)}}
}

func textValuesField(id string, values []string) fulltext.Field {
	result := fulltext.Field{ID: id}
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			result.Values = append(result.Values, fulltext.TextValue(value))
		}
	}
	return result
}

func addIdentity(seen map[string]struct{}, kind fulltext.ObjectKind, objectID string) error {
	key := string(kind) + "\x00" + objectID
	if _, duplicate := seen[key]; duplicate {
		return fmt.Errorf("%w: duplicate %s %q", ErrInvalid, kind, objectID)
	}
	seen[key] = struct{}{}
	return nil
}

func requireText(kind, objectID string, values ...string) error {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%w: %s %q is missing required semantic text", ErrInvalid, kind, objectID)
		}
	}
	return nil
}

func validate(document fulltext.Document) error {
	_, err := fulltext.Compile(document)
	return err
}
