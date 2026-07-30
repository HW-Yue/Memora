package nativemutation

import (
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
	Relations   []relation.Relation
	Memberships []router.Membership
}

type Coordinator struct {
	file   *nativestore.File
	rows   *nativerow.Repository
	router *nativerouter.Repository
}

func New(file *nativestore.File, rows *nativerow.Repository, routes *nativerouter.Repository) *Coordinator {
	return &Coordinator{file: file, rows: rows, router: routes}
}

func (coordinator *Coordinator) Commit(plan Plan) error {
	if coordinator == nil || coordinator.file == nil || coordinator.rows == nil || coordinator.router == nil {
		return fmt.Errorf("native mutation coordinator is incomplete")
	}
	if plan.Operation != history.OperationUpdate && plan.Operation != history.OperationDelete && plan.Operation != history.OperationCompensate {
		return fmt.Errorf("native mutation requires UPDATE, DELETE, or COMPENSATE operation")
	}
	transaction, err := coordinator.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := coordinator.rows.StageRevision(transaction, plan.Row); err != nil {
		return err
	}
	if err := coordinator.rows.StageHistory(transaction, plan.Row, plan.Operation, plan.Metadata, plan.RecordedAt); err != nil {
		return err
	}
	for _, value := range plan.Relations {
		if err := coordinator.rows.StageRelation(transaction, value); err != nil {
			return err
		}
	}
	for _, value := range plan.Memberships {
		if err := coordinator.router.StageMembership(transaction, value); err != nil {
			return err
		}
	}
	return transaction.Commit()
}
