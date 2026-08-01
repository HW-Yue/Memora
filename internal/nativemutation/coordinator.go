package nativemutation

import (
	"context"
	"fmt"
	"time"

	"github.com/HW-Yue/Memora/internal/history"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/relation"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

type Plan struct {
	Row         row.Row
	Operation   history.Operation
	Metadata    row.WriteMetadata
	RecordedAt  time.Time
	Changes     []RowChange
	Relations   []relation.Relation
	Routes      []router.Node
	Memberships []router.Membership
}

type RowChange struct {
	Row        row.Row
	Operation  history.Operation
	Metadata   row.WriteMetadata
	RecordedAt time.Time
	Initial    bool
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
	if len(changes) == 0 {
		changes = []RowChange{{Row: plan.Row, Operation: plan.Operation, Metadata: plan.Metadata, RecordedAt: plan.RecordedAt}}
	}
	for _, change := range changes {
		if change.Operation != history.OperationUpdate && change.Operation != history.OperationDelete && change.Operation != history.OperationCompensate &&
			change.Operation != history.OperationSplit && change.Operation != history.OperationMerge {
			return fmt.Errorf("native mutation has an unsupported row operation")
		}
	}
	transaction, err := coordinator.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	for _, change := range changes {
		if change.Initial {
			err = coordinator.rows.StageInitial(transaction, change.Row)
		} else {
			err = coordinator.rows.StageRevision(transaction, change.Row)
		}
		if err != nil {
			return err
		}
		if err := coordinator.rows.StageHistory(transaction, change.Row, change.Operation, change.Metadata, change.RecordedAt); err != nil {
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
	for _, value := range plan.Memberships {
		if err := coordinator.router.StageMembership(transaction, value); err != nil {
			return err
		}
	}
	if coordinator.pages != nil {
		values := make([]row.Row, 0, len(changes))
		for _, change := range changes {
			values = append(values, change.Row)
		}
		return coordinator.pages.PublishRows(context.Background(), values, transaction.Commit)
	}
	return transaction.Commit()
}
