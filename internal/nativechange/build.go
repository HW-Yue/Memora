package nativechange

import (
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
)

func RowMetadata(value row.WriteMetadata) change.Metadata {
	// The envelope is the single home for attribution, so every provenance field
	// a write carries has to reach it — including the document- and
	// repository-anchor fields, which used to be recorded only per Row.
	metadata := change.Metadata{
		Actor: value.Actor, Source: value.Source, Reason: value.Reason,
		SourceReceiptID:   value.SourceReceiptID,
		SourceKind:        string(value.SourceKind),
		SourceLocator:     value.SourceLocator,
		SourceContentHash: value.SourceContentHash,
	}
	if strings.TrimSpace(metadata.SourceKind) == "" {
		metadata.SourceKind = string(history.SourceConversationAssertion)
	}
	if strings.TrimSpace(metadata.Actor) == "" {
		metadata.Actor = "system:direct-api"
	}
	if strings.TrimSpace(metadata.Source) == "" {
		metadata.Source = "direct-api"
	}
	if strings.TrimSpace(metadata.Reason) == "" {
		// Matches the default the per-Row History record applies
		// (nativerow.normalizedMetadata). The envelope is becoming the single
		// source of attribution, so a Row write with no stated reason has to read
		// the same either way — otherwise SHOW HISTORY would silently change
		// wording the day it starts reading envelopes.
		metadata.Reason = "row mutation"
	}
	return metadata
}

func RowEntry(value row.Row, operation history.Operation, related ...string) change.Entry {
	historyLocator := value.ID
	if value.Revision > 1 {
		historyLocator = fmt.Sprintf("%s@%020d", value.ID, value.Revision)
	}
	return change.Entry{
		ObjectKind: change.ObjectRow, DatabaseID: value.DatabaseID, TableID: value.TableID,
		ObjectID: value.ID, Operation: changeOperation(operation),
		BeforeRevision: value.Revision - 1, AfterRevision: value.Revision,
		SchemaVersion:    value.SchemaVersion,
		HistoryLocator:   historyLocator,
		RelatedObjectIDs: uniqueIDs(related),
	}
}

func RelationEntry(value relation.Relation) change.Entry {
	operation := change.OperationUpdate
	if value.Revision == 1 {
		operation = change.OperationInsert
	} else if value.State == relation.StateDeleted {
		operation = change.OperationDelete
	}
	return change.Entry{
		ObjectKind: change.ObjectRelation, DatabaseID: value.Source.DatabaseID,
		TableID: value.Source.TableID, ObjectID: value.ID, Operation: operation,
		BeforeRevision: value.Revision - 1, AfterRevision: value.Revision,
		RelatedObjectIDs: uniqueIDs([]string{value.Source.RowID, value.Target.RowID}),
	}
}

func RouteNodeEntry(value router.Node, operation change.Operation) change.Entry {
	return change.Entry{
		ObjectKind: change.ObjectRouteNode, DatabaseID: value.DatabaseID, TableID: value.TableID,
		ObjectID: value.ID, Operation: operation, BeforeRevision: value.Revision - 1,
		AfterRevision: value.Revision,
	}
}

func MembershipEntry(value router.Membership) change.Entry {
	operation := change.OperationUpdate
	if value.MembershipRevision == 1 {
		operation = change.OperationInsert
	} else if value.Deleted {
		operation = change.OperationDelete
	}
	return change.Entry{
		ObjectKind: change.ObjectRouteMembership, DatabaseID: value.DatabaseID,
		TableID: value.TableID, ObjectID: value.LeafID + "@" + value.RowID,
		Operation: operation, BeforeRevision: value.MembershipRevision - 1,
		AfterRevision:    value.MembershipRevision,
		RelatedObjectIDs: uniqueIDs([]string{value.LeafID, value.RowID}),
	}
}

func changeOperation(operation history.Operation) change.Operation {
	switch operation {
	case history.OperationInsert:
		return change.OperationInsert
	case history.OperationUpdate:
		return change.OperationUpdate
	case history.OperationDelete:
		return change.OperationDelete
	case history.OperationCompensate:
		return change.OperationCompensate
	case history.OperationSplit:
		return change.OperationSplit
	case history.OperationMerge:
		return change.OperationMerge
	default:
		return ""
	}
}

func uniqueIDs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			seen[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for value := range seen {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
