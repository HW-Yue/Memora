package nativemutation

import (
	"context"
	"fmt"
	"time"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

type Plan struct {
	Row        row.Row
	Operation  history.Operation
	Metadata   row.WriteMetadata
	RecordedAt time.Time
	Changes    []RowChange
	Relations  []relation.Relation
	Routes     []router.Node
}

type RowChange struct {
	Row        row.Row
	Operation  history.Operation
	Metadata   row.WriteMetadata
	RecordedAt time.Time
	Initial    bool
}

type RoutePlanCommit struct {
	Routes []router.Node
	// Rows carries the Row side of any mount this plan moves. A leaf names the
	// Row it holds and the Row names its leaves; both ends move together or the
	// two disagree.
	Rows        []row.Row
	Created     map[string]bool
	Metadata    change.Metadata
	RowMetadata row.WriteMetadata
	CommittedAt time.Time
}

type Coordinator struct {
	file   *nativestore.File
	rows   *nativerow.Repository
	router *nativerouter.Repository
	pages  nativerow.PageAuthority
}

func New(
	file *nativestore.File,
	rows *nativerow.Repository,
	routes *nativerouter.Repository,
	authorities ...nativerow.PageAuthority,
) *Coordinator {
	coordinator := &Coordinator{file: file, rows: rows, router: routes}
	if len(authorities) > 0 {
		coordinator.pages = authorities[0]
	}
	return coordinator
}

func (coordinator *Coordinator) Commit(plan Plan) error {
	if coordinator == nil || coordinator.file == nil || coordinator.rows == nil || coordinator.router == nil {
		return fmt.Errorf("native mutation coordinator is incomplete")
	}
	changes := plan.Changes
	if len(changes) == 0 && plan.Row.ID != "" {
		changes = []RowChange{{Row: plan.Row, Operation: plan.Operation, Metadata: plan.Metadata, RecordedAt: plan.RecordedAt}}
	}
	for _, change := range changes {
		if change.Operation != history.OperationInsert && change.Operation != history.OperationUpdate && change.Operation != history.OperationDelete && change.Operation != history.OperationCompensate &&
			change.Operation != history.OperationSplit && change.Operation != history.OperationMerge {
			return fmt.Errorf("native mutation has an unsupported row operation")
		}
	}
	sequence, err := coordinator.commitSequence(changes, plan.Relations)
	if err != nil {
		return err
	}
	for index := range changes {
		if changes[index].Row.CommitSequence == 0 {
			changes[index].Row.CommitSequence = sequence
		}
	}
	for index := range plan.Relations {
		if plan.Relations[index].CommitSequence == 0 {
			plan.Relations[index].CommitSequence = sequence
		}
	}
	changeSequence, err := coordinator.changeSequence()
	if err != nil {
		return err
	}
	transaction, err := coordinator.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	for index := range changes {
		// Every Row this transaction writes points at the one envelope that
		// records who wrote it and why.
		changes[index].Row.ChangeSequence = changeSequence
	}
	for _, change := range changes {
		if change.Initial {
			err = coordinator.rows.StageInitial(transaction, change.Row)
		} else {
			err = coordinator.rows.StageRevision(transaction, change.Row)
		}
		if err != nil {
			return err
		}
	}
	for _, value := range plan.Relations {
		if err := coordinator.rows.StageRelation(transaction, value); err != nil {
			return err
		}
	}
	for _, value := range plan.Routes {
		if err := coordinator.router.StageNode(transaction, value); err != nil {
			return err
		}
	}
	envelope, err := mutationEnvelope(changeSequence, changes, plan.Relations, plan.Routes)
	if err != nil {
		return err
	}
	if err := nativechange.Stage(transaction, envelope); err != nil {
		return err
	}
	if coordinator.pages != nil {
		values := make([]row.Row, 0, len(changes))
		for _, change := range changes {
			values = append(values, change.Row)
		}
		return coordinator.pages.PublishMutation(context.Background(),
			nativerow.Mutation{Rows: values, Routes: plan.Routes, Relations: plan.Relations},
			transaction.Commit)
	}
	return transaction.Commit()
}

func (coordinator *Coordinator) CommitRoutePlan(plan RoutePlanCommit) (uint64, error) {
	if coordinator == nil || coordinator.file == nil || coordinator.router == nil ||
		coordinator.rows == nil || len(plan.Routes) == 0 {
		return 0, fmt.Errorf("native Route plan commit is incomplete")
	}
	sequence, err := coordinator.changeSequence()
	if err != nil {
		return 0, err
	}
	transaction, err := coordinator.file.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = transaction.Rollback() }()
	entries := make([]change.Entry, 0, len(plan.Routes))
	for _, value := range plan.Routes {
		operation := change.OperationUpdate
		if plan.Created[value.ID] {
			operation = change.OperationInsert
			err = coordinator.router.StagePlannedCreate(transaction, value)
		} else {
			if value.Deleted {
				operation = change.OperationDelete
			}
			err = coordinator.router.StagePlannedRevision(transaction, value)
		}
		if err != nil {
			return 0, err
		}
		entries = append(entries, nativechange.RouteNodeEntry(value, operation))
	}
	committedAt := plan.CommittedAt.UTC()
	if committedAt.IsZero() {
		committedAt = time.Now().UTC()
	}
	// The Row side of every mount this plan moved. It is a revision like any
	// other, with a history record explaining it, because "which leaves hold
	// this Row" is part of the Row.
	for _, value := range plan.Rows {
		value.ChangeSequence = sequence
		value.UpdatedAt = committedAt
		if err := coordinator.rows.StageRevision(transaction, value); err != nil {
			return 0, err
		}
		entries = append(entries, nativechange.RowEntry(value, history.OperationUpdate))
	}
	envelope, err := change.NewEnvelope(sequence, committedAt, plan.Metadata, entries)
	if err != nil {
		return 0, err
	}
	if err := nativechange.Stage(transaction, envelope); err != nil {
		return 0, err
	}
	commit := transaction.Commit
	if coordinator.pages != nil {
		commit = func() error {
			return coordinator.pages.PublishMutation(context.Background(),
				nativerow.Mutation{Rows: plan.Rows, Routes: plan.Routes},
				transaction.Commit)
		}
	}
	if err := commit(); err != nil {
		return 0, err
	}
	return sequence, nil
}

func (coordinator *Coordinator) commitSequence(
	changes []RowChange, relations []relation.Relation,
) (uint64, error) {
	sequence := uint64(0)
	for _, value := range changes {
		if value.Row.CommitSequence == 0 {
			continue
		}
		if sequence != 0 && sequence != value.Row.CommitSequence {
			return 0, fmt.Errorf("native mutation has mixed commit sequences")
		}
		sequence = value.Row.CommitSequence
	}
	for _, value := range relations {
		if value.CommitSequence == 0 {
			continue
		}
		if sequence != 0 && sequence != value.CommitSequence {
			return 0, fmt.Errorf("native mutation has mixed commit sequences")
		}
		sequence = value.CommitSequence
	}
	if sequence != 0 {
		return sequence, nil
	}
	return coordinator.rows.NextCommitSequence()
}

func (coordinator *Coordinator) changeSequence() (uint64, error) {
	if coordinator.pages != nil {
		return coordinator.pages.NextChangeSequence(context.Background())
	}
	return nativechange.New(coordinator.file).NextSequence(0)
}

func mutationEnvelope(
	sequence uint64,
	changes []RowChange,
	relations []relation.Relation,
	routes []router.Node,
) (change.Envelope, error) {
	entries := make([]change.Entry, 0, len(changes)+len(relations)+len(routes))
	relatedRows := make([]string, 0, len(changes))
	committedAt := time.Time{}
	metadata := change.Metadata{}
	for _, value := range changes {
		relatedRows = append(relatedRows, value.Row.ID)
		if value.RecordedAt.After(committedAt) {
			committedAt = value.RecordedAt
		}
		if metadata.Actor == "" {
			metadata = nativechange.RowMetadata(value.Metadata)
		}
	}
	for _, value := range changes {
		entries = append(entries, nativechange.RowEntry(value.Row, value.Operation, relatedRows...))
	}
	for _, value := range relations {
		entries = append(entries, nativechange.RelationEntry(value))
		if value.UpdatedAt.After(committedAt) {
			committedAt = value.UpdatedAt
		}
	}
	for _, value := range routes {
		operation := change.OperationUpdate
		if value.Revision == 1 {
			operation = change.OperationInsert
		} else if value.Deleted {
			operation = change.OperationDelete
		}
		entries = append(entries, nativechange.RouteNodeEntry(value, operation))
	}
	if committedAt.IsZero() {
		committedAt = time.Now().UTC()
	}
	if metadata.Actor == "" {
		metadata = change.Metadata{
			Actor: "system:direct-api", Source: "direct-api", Reason: "logical mutation",
		}
	}
	return change.NewEnvelope(sequence, committedAt, metadata, entries)
}
