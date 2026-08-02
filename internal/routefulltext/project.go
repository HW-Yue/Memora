package routefulltext

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/router"
)

var ErrInvalid = errors.New("Route fulltext projection is invalid")

func Project(nodes []router.Node) ([]fulltext.Document, error) {
	ordered := append([]router.Node(nil), nodes...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].ID < ordered[right].ID })
	documents := make([]fulltext.Document, 0, len(ordered))
	seen := make(map[string]struct{}, len(ordered))
	for _, node := range ordered {
		if node.Version != router.Version || node.Deleted || node.Revision == 0 ||
			node.DatabaseID == "" || node.TableID == "" || node.ID == "" ||
			strings.TrimSpace(node.Name) == "" || strings.TrimSpace(node.Path) == "" ||
			strings.TrimSpace(node.Purpose) == "" || !validKind(node.Kind) {
			return nil, fmt.Errorf("%w: Route %q identity or semantic surface", ErrInvalid, node.ID)
		}
		if _, duplicate := seen[node.ID]; duplicate {
			return nil, fmt.Errorf("%w: duplicate Route %q", ErrInvalid, node.ID)
		}
		seen[node.ID] = struct{}{}
		fields := []fulltext.Field{
			textField("name", node.Name),
			textValuesField("aliases", node.Aliases),
			textField("path", node.Path),
			textField("kind", string(node.Kind)),
			textField("purpose", node.Purpose),
			textField("synopsis", node.Synopsis),
		}
		document := fulltext.Document{
			Version: fulltext.DocumentVersion, Kind: fulltext.KindRoute,
			DatabaseID: node.DatabaseID, TableID: node.TableID, ObjectID: node.ID,
			Revision: node.Revision, State: fulltext.StateLive, Complete: true,
		}
		for _, field := range fields {
			if len(field.Values) != 0 {
				document.Fields = append(document.Fields, field)
			}
		}
		if _, err := fulltext.Compile(document); err != nil {
			return nil, fmt.Errorf("%w: Route %q: %v", ErrInvalid, node.ID, err)
		}
		documents = append(documents, document)
	}
	return documents, nil
}

func ProjectChanges([]router.Node) ([]fulltext.Document, error) {
	return nil, fmt.Errorf("%w: change projection is not implemented", ErrInvalid)
}

func Tombstone(object fulltext.Object) (fulltext.Document, error) {
	if object.Kind != fulltext.KindRoute || object.State != fulltext.StateLive ||
		object.Revision == 0 || object.Revision == math.MaxUint64 {
		return fulltext.Document{}, fmt.Errorf("%w: tombstone source", ErrInvalid)
	}
	document := fulltext.Document{
		Version: fulltext.DocumentVersion, Kind: fulltext.KindRoute,
		DatabaseID: object.DatabaseID, TableID: object.TableID, ObjectID: object.ObjectID,
		Revision: object.Revision + 1, State: fulltext.StateDeleted, Complete: true,
	}
	if _, err := fulltext.Compile(document); err != nil {
		return fulltext.Document{}, fmt.Errorf("%w: tombstone: %v", ErrInvalid, err)
	}
	return document, nil
}

func validKind(kind router.Kind) bool {
	return kind == router.KindRoot || kind == router.KindBranch || kind == router.KindLeaf
}

func textField(id, value string) fulltext.Field {
	if strings.TrimSpace(value) == "" {
		return fulltext.Field{}
	}
	return fulltext.Field{ID: id, Values: []fulltext.Value{fulltext.TextValue(value)}}
}

func textValuesField(id string, values []string) fulltext.Field {
	result := fulltext.Field{ID: id}
	ordered := append([]string(nil), values...)
	sort.Strings(ordered)
	for _, value := range ordered {
		if strings.TrimSpace(value) != "" {
			result.Values = append(result.Values, fulltext.TextValue(value))
		}
	}
	return result
}
