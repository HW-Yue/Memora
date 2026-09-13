package sqlstore

import (
	"context"
	"strings"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativeconfig"
	"github.com/HW-Yue/Memora/internal/result"
)

func (t *tx) configRevisions(ctx context.Context, key string, decode func(string) error) error {
	rows, err := t.q().QueryContext(ctx, `SELECT body FROM mem_config WHERE key = ? ORDER BY revision`, key)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return err
		}
		if err := decode(body); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (t *tx) budgetHistory(ctx context.Context) ([]nativeconfig.Revision, error) {
	values := []nativeconfig.Revision{}
	err := t.configRevisions(ctx, nativeconfig.QueryBudgetsKey, func(body string) error {
		var value nativeconfig.Revision
		if err := decodeJSON(body, &value); err != nil {
			return err
		}
		values = append(values, value)
		return nil
	})
	if err == nil && len(values) == 0 {
		values = append(values, nativeconfig.Revision{
			Version: nativeconfig.Version, Key: nativeconfig.QueryBudgetsKey, Revision: 1,
			Budgets: nativeconfig.Defaults(), Actor: "engine:bootstrap", Reason: "default query budgets",
			RecordedAt: t.now,
		})
	}
	return values, err
}

func (t *tx) policyHistory(ctx context.Context) ([]nativeconfig.PolicyRevision, error) {
	values := []nativeconfig.PolicyRevision{}
	err := t.configRevisions(ctx, nativeconfig.RoutePolicyKey, func(body string) error {
		var value nativeconfig.PolicyRevision
		if err := decodeJSON(body, &value); err != nil {
			return err
		}
		values = append(values, value)
		return nil
	})
	if err == nil && len(values) == 0 {
		values = append(values, nativeconfig.PolicyRevision{
			Version: nativeconfig.Version, Key: nativeconfig.RoutePolicyKey, Revision: 1,
			Policy: nativeconfig.DefaultRoutePolicy(), Actor: "engine:bootstrap",
			Reason: "materialize database Route policy defaults", RecordedAt: t.now,
		})
	}
	return values, err
}

func (t *tx) currentRoutePolicy(ctx context.Context) (nativeconfig.PolicyRevision, error) {
	values, err := t.policyHistory(ctx)
	if err != nil {
		return nativeconfig.PolicyRevision{}, err
	}
	return values[len(values)-1], nil
}

func (t *tx) putConfig(ctx context.Context, key string, revision uint64, body any, reason string) error {
	if _, err := t.q().ExecContext(ctx, `INSERT INTO mem_config(key, revision, body) VALUES (?, ?, ?)`, key, revision, encodeJSON(body)); err != nil {
		return err
	}
	t.recordEntry(change.Entry{
		ObjectKind: change.ObjectConfiguration, ObjectID: key, Operation: change.OperationUpdate,
		BeforeRevision: revision - 1, AfterRevision: revision,
	}, change.Metadata{Actor: "system:configuration", Source: "msql", Reason: fallback(reason, "configuration")})
	return nil
}

func (db *DB) CurrentQueryBudgets(ctx context.Context) (nativeconfig.Revision, error) {
	history, err := db.QueryBudgetHistory(ctx)
	if err != nil {
		return nativeconfig.Revision{}, err
	}
	return history[len(history)-1], nil
}

func (db *DB) QueryBudgetHistory(ctx context.Context) ([]nativeconfig.Revision, error) {
	var values []nativeconfig.Revision
	err := db.view(ctx, func(t *tx) error {
		var err error
		values, err = t.budgetHistory(ctx)
		return err
	})
	return values, err
}

func (db *DB) UpdateQueryBudgets(ctx context.Context, budgets nativeconfig.QueryBudgets, expected uint64, actor, reason string) (nativeconfig.Revision, error) {
	if err := nativeconfig.ValidateMutation(expected, actor, reason); err != nil {
		return nativeconfig.Revision{}, err
	}
	if err := nativeconfig.ValidateBudgets(budgets); err != nil {
		return nativeconfig.Revision{}, err
	}
	return db.writeBudgets(ctx, expected, func(nativeconfig.Revision, []nativeconfig.Revision) (nativeconfig.QueryBudgets, uint64, error) {
		return budgets, 0, nil
	}, actor, reason)
}

func (db *DB) RestoreQueryBudgets(ctx context.Context, target, expected uint64, actor, reason string) (nativeconfig.Revision, error) {
	if err := nativeconfig.ValidateMutation(expected, actor, reason); err != nil {
		return nativeconfig.Revision{}, err
	}
	return db.writeBudgets(ctx, expected, func(_ nativeconfig.Revision, history []nativeconfig.Revision) (nativeconfig.QueryBudgets, uint64, error) {
		for _, value := range history {
			if value.Revision == target {
				return value.Budgets, target, nil
			}
		}
		return nativeconfig.QueryBudgets{}, 0, fail(result.CodeNotFound, "query budgets revision %d was not found", target)
	}, actor, reason)
}

func (db *DB) writeBudgets(ctx context.Context, expected uint64,
	next func(nativeconfig.Revision, []nativeconfig.Revision) (nativeconfig.QueryBudgets, uint64, error), actor, reason string) (nativeconfig.Revision, error) {
	var written nativeconfig.Revision
	err := db.update(ctx, func(t *tx) error {
		history, err := t.budgetHistory(ctx)
		if err != nil {
			return err
		}
		current := history[len(history)-1]
		if current.Revision != expected {
			return fail(result.CodeRevisionConflict, "query budgets are at revision %d, not %d", current.Revision, expected)
		}
		budgets, restored, err := next(current, history)
		if err != nil {
			return err
		}
		written = nativeconfig.Revision{
			Version: nativeconfig.Version, Key: nativeconfig.QueryBudgetsKey, Revision: current.Revision + 1,
			Budgets: budgets, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
			RestoredRevision: restored, RecordedAt: t.now,
		}
		return t.putConfig(ctx, nativeconfig.QueryBudgetsKey, written.Revision, written, reason)
	})
	return written, err
}

func (db *DB) CurrentRoutePolicy(ctx context.Context) (nativeconfig.PolicyRevision, error) {
	var value nativeconfig.PolicyRevision
	err := db.view(ctx, func(t *tx) error {
		var err error
		value, err = t.currentRoutePolicy(ctx)
		return err
	})
	return value, err
}

func (db *DB) RoutePolicyHistory(ctx context.Context) ([]nativeconfig.PolicyRevision, error) {
	var values []nativeconfig.PolicyRevision
	err := db.view(ctx, func(t *tx) error {
		var err error
		values, err = t.policyHistory(ctx)
		return err
	})
	return values, err
}

func (db *DB) UpdateRoutePolicy(ctx context.Context, policy nativeconfig.RoutePolicy, expected uint64, actor, reason string) (nativeconfig.PolicyRevision, error) {
	if err := nativeconfig.ValidateMutation(expected, actor, reason); err != nil {
		return nativeconfig.PolicyRevision{}, err
	}
	if err := nativeconfig.ValidateRoutePolicy(policy); err != nil {
		return nativeconfig.PolicyRevision{}, err
	}
	return db.writePolicy(ctx, expected, func(current nativeconfig.PolicyRevision, _ []nativeconfig.PolicyRevision) (nativeconfig.RoutePolicy, uint64, error) {
		if err := nativeconfig.ValidateFanoutStep(current.Policy.BranchFanout, policy.BranchFanout); err != nil {
			return nativeconfig.RoutePolicy{}, 0, err
		}
		return policy, 0, nil
	}, actor, reason)
}

func (db *DB) RestoreRoutePolicy(ctx context.Context, target, expected uint64, actor, reason string) (nativeconfig.PolicyRevision, error) {
	if err := nativeconfig.ValidateMutation(expected, actor, reason); err != nil {
		return nativeconfig.PolicyRevision{}, err
	}
	return db.writePolicy(ctx, expected, func(_ nativeconfig.PolicyRevision, history []nativeconfig.PolicyRevision) (nativeconfig.RoutePolicy, uint64, error) {
		for _, value := range history {
			if value.Revision == target {
				return value.Policy, target, nil
			}
		}
		return nativeconfig.RoutePolicy{}, 0, fail(result.CodeNotFound, "route policy revision %d was not found", target)
	}, actor, reason)
}

func (db *DB) writePolicy(ctx context.Context, expected uint64,
	next func(nativeconfig.PolicyRevision, []nativeconfig.PolicyRevision) (nativeconfig.RoutePolicy, uint64, error), actor, reason string) (nativeconfig.PolicyRevision, error) {
	var written nativeconfig.PolicyRevision
	err := db.update(ctx, func(t *tx) error {
		history, err := t.policyHistory(ctx)
		if err != nil {
			return err
		}
		current := history[len(history)-1]
		if current.Revision != expected {
			return fail(result.CodeRevisionConflict, "route policy is at revision %d, not %d", current.Revision, expected)
		}
		policy, restored, err := next(current, history)
		if err != nil {
			return err
		}
		written = nativeconfig.PolicyRevision{
			Version: nativeconfig.Version, Key: nativeconfig.RoutePolicyKey, Revision: current.Revision + 1,
			Policy: policy, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
			RestoredRevision: restored, RecordedAt: t.now,
		}
		return t.putConfig(ctx, nativeconfig.RoutePolicyKey, written.Revision, written, reason)
	})
	return written, err
}
