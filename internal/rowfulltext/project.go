package rowfulltext

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/logical"
	"github.com/HW-Yue/Memora/internal/row"
)

var ErrInvalid = errors.New("Row fulltext projection is invalid")

func Project(table catalog.Table, value row.Row) (fulltext.Document, error) {
	if table.ID == "" || table.DatabaseID == "" || table.SchemaVersion == 0 ||
		value.ID == "" || value.DatabaseID != table.DatabaseID || value.TableID != table.ID ||
		value.SchemaVersion != table.SchemaVersion || value.Revision == 0 {
		return fulltext.Document{}, projectionError("Row and Table identity or revision disagrees")
	}
	state, err := projectState(value.State)
	if err != nil {
		return fulltext.Document{}, err
	}
	document := fulltext.Document{
		Version: fulltext.DocumentVersion, Kind: fulltext.KindRow,
		DatabaseID: value.DatabaseID, TableID: value.TableID, ObjectID: value.ID,
		Revision: value.Revision, SchemaRevision: value.SchemaVersion,
		State: state, Complete: true,
	}
	if state != fulltext.StateLive {
		if _, err := fulltext.Compile(document); err != nil {
			return fulltext.Document{}, projectionError("compile tombstone: %v", err)
		}
		return document, nil
	}

	columns := append([]catalog.Column(nil), table.Columns...)
	sort.Slice(columns, func(left, right int) bool { return columns[left].ID < columns[right].ID })
	known := make(map[string]struct{}, len(columns))
	fields := make([]fulltext.Field, 0, len(columns))
	for _, column := range columns {
		if column.ID == "" {
			return fulltext.Document{}, projectionError("Column identity is empty")
		}
		if _, duplicate := known[column.ID]; duplicate {
			return fulltext.Document{}, projectionError("Column %q is duplicated", column.ID)
		}
		known[column.ID] = struct{}{}
		raw, exists := value.Values[column.ID]
		if !exists {
			if column.Nullable {
				continue
			}
			return fulltext.Document{}, projectionError("non-null Column %q is missing", column.ID)
		}
		if raw == nil {
			if !column.Nullable {
				return fulltext.Document{}, projectionError("non-null Column %q is NULL", column.ID)
			}
			continue
		}
		normalized, err := column.Validate(raw)
		if err != nil {
			return fulltext.Document{}, projectionError("Column %q: %v", column.ID, err)
		}
		if logical.Kind(column.Type) == logical.KindText {
			text, ok := normalized.(string)
			if !ok {
				return fulltext.Document{}, projectionError("Column %q: TEXT normalization changed type", column.ID)
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
		}
		projected, err := projectValue(logical.Kind(column.Type), normalized)
		if err != nil {
			return fulltext.Document{}, projectionError("Column %q: %v", column.ID, err)
		}
		fields = append(fields, fulltext.Field{ID: column.ID, Values: []fulltext.Value{projected}})
	}
	for columnID := range value.Values {
		if _, exists := known[columnID]; !exists {
			return fulltext.Document{}, projectionError("Row contains unknown Column %q", columnID)
		}
	}
	document.Fields = fields
	if _, err := fulltext.Compile(document); err != nil {
		return fulltext.Document{}, projectionError("compile Row document: %v", err)
	}
	return document, nil
}

func projectState(value row.State) (fulltext.State, error) {
	switch value {
	case row.StateLive:
		return fulltext.StateLive, nil
	case row.StateDeleted:
		return fulltext.StateDeleted, nil
	case row.StateSuperseded:
		return fulltext.StateSuperseded, nil
	default:
		return "", projectionError("Row state is invalid")
	}
}

func projectValue(kind logical.Kind, value any) (fulltext.Value, error) {
	switch kind {
	case logical.KindText:
		text, ok := value.(string)
		if !ok {
			return fulltext.Value{}, errors.New("TEXT normalization changed type")
		}
		return fulltext.TextValue(text), nil
	case logical.KindInteger:
		integer, ok := value.(int64)
		if !ok {
			return fulltext.Value{}, errors.New("INTEGER normalization changed type")
		}
		return fulltext.IntegerValue(integer), nil
	case logical.KindBoolean:
		boolean, ok := value.(bool)
		if !ok {
			return fulltext.Value{}, errors.New("BOOLEAN normalization changed type")
		}
		return fulltext.BooleanValue(boolean), nil
	case logical.KindTimestamp:
		stamp, ok := value.(time.Time)
		if !ok {
			return fulltext.Value{}, errors.New("TIMESTAMP normalization changed type")
		}
		return fulltext.TimestampValue(stamp), nil
	case logical.KindRelationID:
		relationID, ok := value.(string)
		if !ok {
			return fulltext.Value{}, errors.New("RELATION_ID normalization changed type")
		}
		return fulltext.RelationIDValue(relationID), nil
	default:
		return fulltext.Value{}, fmt.Errorf("logical kind %q is unsupported", kind)
	}
}

func projectionError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}
