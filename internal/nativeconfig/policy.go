package nativeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HW-Yue/Memora/internal/change"
	"github.com/HW-Yue/Memora/internal/nativechange"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	RoutePolicyKey         = "route_policy"
	PolicySnapshotKey      = "memora.configuration.route_policy"
	DefaultBranchFanout    = router.DefaultBranchFanout
	minimumBranchFanout    = 2
	maximumBranchFanout    = router.MaxConfigurableBranchFanout
	routePolicyBootReason  = "materialize database Route policy defaults"
	routePolicyBootstrapBy = "engine:bootstrap"
)

// RoutePolicy holds the semantic structure limits a Database enforces on its
// Router tree. BranchFanout is the maximum number of live children one root or
// branch may carry; it is not the SHOW ROUTES read page budget.
type RoutePolicy struct {
	BranchFanout int `json:"branch_fanout"`
}

type PolicyRevision struct {
	Version          string      `json:"version"`
	Key              string      `json:"key"`
	Revision         uint64      `json:"revision"`
	Policy           RoutePolicy `json:"policy"`
	Actor            string      `json:"actor"`
	Reason           string      `json:"reason"`
	RestoredRevision uint64      `json:"restored_revision,omitempty"`
	RecordedAt       time.Time   `json:"recorded_at"`
}

func DefaultRoutePolicy() RoutePolicy {
	return RoutePolicy{BranchFanout: DefaultBranchFanout}
}

func (service *Service) materializeRoutePolicy() error {
	if _, err := service.currentPolicy(); err == nil {
		return nil
	} else if !errors.Is(err, nativestore.ErrNotFound) {
		return err
	}
	return service.putPolicy(PolicyRevision{
		Version: Version, Key: RoutePolicyKey, Revision: 1, Policy: DefaultRoutePolicy(),
		Actor: routePolicyBootstrapBy, Reason: routePolicyBootReason,
		RecordedAt: time.Now().UTC(),
	})
}

func (service *Service) CurrentPolicy() (PolicyRevision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.currentPolicy()
}

func (service *Service) currentPolicy() (PolicyRevision, error) {
	revisions, err := service.policyHistory()
	if err != nil {
		return PolicyRevision{}, err
	}
	if len(revisions) == 0 {
		return PolicyRevision{}, nativestore.ErrNotFound
	}
	return revisions[len(revisions)-1], nil
}

func (service *Service) PolicyHistory() ([]PolicyRevision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.policyHistory()
}

// policyHistory walks the revision chain forward from 1, for the reason given
// on history: revisions are dense, so the chain finds itself.
func (service *Service) policyHistory() ([]PolicyRevision, error) {
	values := make([]PolicyRevision, 0)
	for revision := uint64(1); ; revision++ {
		payload, err := service.file.Get(
			nativestore.ObjectKindConfiguration,
			fmt.Sprintf("%s_r%020d", RoutePolicyKey, revision),
		)
		if errors.Is(err, nativestore.ErrNotFound) {
			return values, nil
		}
		if err != nil {
			return nil, err
		}
		var value PolicyRevision
		if err := json.Unmarshal(payload, &value); err != nil ||
			value.Version != Version || value.Key != RoutePolicyKey ||
			value.Revision != revision || validateRoutePolicy(value.Policy) != nil {
			return nil, configError(result.CodeInternal, "native Route policy configuration is corrupt")
		}
		values = append(values, value)
	}
}

func (service *Service) UpdatePolicy(policy RoutePolicy, expected uint64, actor, reason string) (PolicyRevision, error) {
	return service.UpdatePolicyCommitted(policy, expected, actor, reason, 0)
}

func (service *Service) UpdatePolicyCommitted(
	policy RoutePolicy, expected uint64, actor, reason string, sequence uint64,
) (PolicyRevision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := validateMutation(expected, actor, reason); err != nil {
		return PolicyRevision{}, err
	}
	if err := validateRoutePolicy(policy); err != nil {
		return PolicyRevision{}, err
	}
	current, err := service.currentPolicy()
	if err != nil {
		return PolicyRevision{}, err
	}
	if current.Revision != expected {
		return PolicyRevision{}, configError(result.CodeRevisionConflict, "configuration revision conflicts with latest")
	}
	if err := validateFanoutStep(current.Policy.BranchFanout, policy.BranchFanout); err != nil {
		return PolicyRevision{}, err
	}
	next := PolicyRevision{
		Version: Version, Key: RoutePolicyKey, Revision: current.Revision + 1,
		Policy: policy, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
		RecordedAt: time.Now().UTC(),
	}
	return next, service.commitPolicy(next, sequence, change.OperationUpdate)
}

func (service *Service) RestorePolicy(target, expected uint64, actor, reason string) (PolicyRevision, error) {
	return service.RestorePolicyCommitted(target, expected, actor, reason, 0)
}

func (service *Service) RestorePolicyCommitted(
	target, expected uint64, actor, reason string, sequence uint64,
) (PolicyRevision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if target == 0 {
		return PolicyRevision{}, configError(result.CodeValidation, "configuration restore target revision is required")
	}
	if err := validateMutation(expected, actor, reason); err != nil {
		return PolicyRevision{}, err
	}
	history, err := service.policyHistory()
	if err != nil {
		return PolicyRevision{}, err
	}
	if len(history) == 0 {
		return PolicyRevision{}, nativestore.ErrNotFound
	}
	current := history[len(history)-1]
	if current.Revision != expected {
		return PolicyRevision{}, configError(result.CodeRevisionConflict, "configuration revision conflicts with latest")
	}
	if target > uint64(len(history)) {
		return PolicyRevision{}, configError(result.CodeNotFound, "configuration target revision was not found")
	}
	// Restoring an older revision must not become a way around the step limit:
	// moving back up to a fan-out reached earlier is still a raise from here.
	if err := validateFanoutStep(
		current.Policy.BranchFanout, history[target-1].Policy.BranchFanout,
	); err != nil {
		return PolicyRevision{}, err
	}
	next := PolicyRevision{
		Version: Version, Key: RoutePolicyKey, Revision: current.Revision + 1,
		Policy: history[target-1].Policy, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
		RestoredRevision: target, RecordedAt: time.Now().UTC(),
	}
	return next, service.commitPolicy(next, sequence, change.OperationRestore)
}

func (service *Service) commitPolicy(value PolicyRevision, sequence uint64, operation change.Operation) error {
	if sequence == 0 {
		var err error
		sequence, err = nativechange.New(service.file).NextSequence(0)
		if err != nil {
			return err
		}
	}
	transaction, err := service.file.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = transaction.Rollback() }()
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := transaction.Put(
		nativestore.ObjectKindConfiguration, recordSchema, policyRecordID(value.Revision), payload,
	); err != nil {
		return err
	}
	envelope, err := change.NewEnvelope(sequence, value.RecordedAt, change.Metadata{
		Actor: value.Actor, Source: "msql", Reason: value.Reason,
	}, []change.Entry{{
		ObjectKind: change.ObjectConfiguration, ObjectID: value.Key, Operation: operation,
		BeforeRevision: value.Revision - 1, AfterRevision: value.Revision,
	}})
	if err != nil {
		return err
	}
	if err := nativechange.Stage(transaction, envelope); err != nil {
		return err
	}
	return transaction.Commit()
}

func (service *Service) putPolicy(value PolicyRevision) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return service.file.Put(
		nativestore.ObjectKindConfiguration, recordSchema, policyRecordID(value.Revision), payload,
	)
}

func (service *Service) StagePolicyHistory(transaction *nativestore.Transaction, values []PolicyRevision) error {
	if transaction == nil {
		return configError(result.CodeInternal, "native configuration transaction is required")
	}
	for index, value := range values {
		if value.Version != Version || value.Key != RoutePolicyKey ||
			value.Revision != uint64(index+1) || validateRoutePolicy(value.Policy) != nil ||
			strings.TrimSpace(value.Actor) == "" || strings.TrimSpace(value.Reason) == "" {
			return configError(result.CodeValidation, "logical snapshot Route policy history is invalid")
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if err := transaction.Put(
			nativestore.ObjectKindConfiguration, recordSchema, policyRecordID(value.Revision), payload,
		); err != nil {
			return err
		}
	}
	return nil
}

func policyRecordID(revision uint64) string {
	return fmt.Sprintf("%s_r%020d", RoutePolicyKey, revision)
}

func validateRoutePolicy(value RoutePolicy) error {
	if value.BranchFanout < minimumBranchFanout || value.BranchFanout > maximumBranchFanout {
		return configError(result.CodeConstraint, fmt.Sprintf(
			"branch_fanout must be between %d and %d", minimumBranchFanout, maximumBranchFanout,
		))
	}
	return nil
}

// validateFanoutStep bounds how far one mutation may raise branch_fanout.
// Widening the semantic tree must stay a repeated, justified decision, so an
// Agent cannot answer one crowded parent by jumping to the ceiling. Lowering
// is unrestricted.
func validateFanoutStep(current, next int) error {
	if next <= current {
		return nil
	}
	allowed := router.NextBranchFanout(current, maximumBranchFanout)
	if next > allowed {
		return configError(result.CodeConstraint, fmt.Sprintf(
			"branch_fanout may rise by at most %d per change: this database allows %d and may move to %d next, not %d",
			router.MaxBranchFanoutIncrease, current, allowed, next,
		))
	}
	return nil
}
