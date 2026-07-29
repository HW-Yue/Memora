package row

import (
	"context"
	"time"

	"github.com/HW-Yue/Memora/internal/reindex"
	"github.com/HW-Yue/Memora/internal/result"
)

func (transaction *Transaction) enqueueReindex(
	ctx context.Context,
	stored storedRow,
	needAgent, needRouter bool,
) error {
	if !needAgent && !needRouter {
		return nil
	}
	_, err := transaction.service.reindex.EnqueueIn(
		ctx, transaction.tx, reindexLocator(stored), needAgent, needRouter,
		stored.UpdatedAt,
	)
	return stableError(err)
}

func (transaction *Transaction) clearReindex(
	ctx context.Context,
	stored storedRow,
) error {
	return stableError(transaction.service.reindex.RemoveIn(
		ctx, transaction.tx, stored.DatabaseID, stored.TableID, stored.ID,
	))
}

func reindexLocator(stored storedRow) reindex.Locator {
	return reindex.Locator{
		DatabaseID: stored.DatabaseID, TableID: stored.TableID,
		RowID: stored.ID, Revision: stored.Revision,
	}
}

func (service *Service) ListReindexTasks(ctx context.Context) ([]reindex.Task, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = transaction.Rollback() }()
	tasks, err := service.reindex.ListIn(ctx, transaction.tx)
	return tasks, stableError(err)
}

func (service *Service) ClaimReindex(
	ctx context.Context,
	owner string,
	lease time.Duration,
) (reindex.Task, error) {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return reindex.Task{}, err
	}
	defer func() { _ = transaction.Rollback() }()
	task, err := service.reindex.ClaimIn(
		ctx, transaction.tx, owner, service.clock.Now(), lease,
	)
	if err != nil {
		return reindex.Task{}, stableError(err)
	}
	if err := transaction.Commit(); err != nil {
		return reindex.Task{}, err
	}
	return task, nil
}

func (service *Service) FailReindex(
	ctx context.Context,
	claim reindex.Task,
	message string,
) error {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.reindex.FailIn(
		ctx, transaction.tx, claim, message, service.clock.Now(),
	); err != nil {
		return stableError(err)
	}
	return transaction.Commit()
}

func (service *Service) RetryReindex(
	ctx context.Context,
	databaseID, tableID, rowID string,
) error {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	if err := service.reindex.RetryIn(
		ctx, transaction.tx, databaseID, tableID, rowID, service.clock.Now(),
	); err != nil {
		return stableError(err)
	}
	return transaction.Commit()
}

func (service *Service) CompleteReindex(
	ctx context.Context,
	claim reindex.Task,
	terms, leafIDs []string,
) error {
	transaction, err := service.BeginTransaction(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	now := service.clock.Now()
	current, err := service.reindex.ValidateClaimIn(ctx, transaction.tx, claim, now)
	if err != nil {
		return stableError(err)
	}
	if current.State == reindex.StateCompleted {
		return nil
	}
	stored, err := getStored(ctx, transaction.tx, current.TableID, current.RowID)
	if err != nil {
		return stableError(err)
	}
	if stored.State != StateLive || stored.Revision != current.Revision {
		return rowError(result.CodeRevisionConflict, "reindex Row revision conflict")
	}
	if current.NeedAgent {
		if terms == nil {
			return rowError(result.CodeValidation, "reindex result requires Agent terms")
		}
		if err := transaction.validateIndexTerms(terms); err != nil {
			return err
		}
		if err := transaction.replaceAgentIndex(ctx, stored, terms); err != nil {
			return err
		}
	}
	if current.NeedRouter {
		if leafIDs == nil {
			return rowError(result.CodeValidation, "reindex result requires Router memberships")
		}
		if err := transaction.replaceRouterMemberships(ctx, stored, leafIDs); err != nil {
			return err
		}
	}
	if err := service.reindex.CompleteIn(
		ctx, transaction.tx, claim, now,
	); err != nil {
		return stableError(err)
	}
	return transaction.Commit()
}
