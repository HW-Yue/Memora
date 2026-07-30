package nativeconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/HW-Yue/Memora/internal/result"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
)

const (
	Version         = "memora.database-configuration/v1"
	QueryBudgetsKey = "query_budgets"
	SnapshotKey     = "memora.configuration.query_budgets"
	recordSchema    = 1
)

type QueryBudgets struct {
	RouteChildren   int `json:"route_children"`
	OpenLocators    int `json:"open_locators"`
	SelectScan      int `json:"select_scan"`
	SelectRows      int `json:"select_rows"`
	RouteFrameNodes int `json:"route_frame_nodes"`
}

type Revision struct {
	Version          string       `json:"version"`
	Key              string       `json:"key"`
	Revision         uint64       `json:"revision"`
	Budgets          QueryBudgets `json:"budgets"`
	Actor            string       `json:"actor"`
	Reason           string       `json:"reason"`
	RestoredRevision uint64       `json:"restored_revision,omitempty"`
	RecordedAt       time.Time    `json:"recorded_at"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

type Service struct {
	file *nativestore.File
	mu   sync.Mutex
}

func Defaults() QueryBudgets {
	return QueryBudgets{
		RouteChildren: 12, OpenLocators: 24, SelectScan: 1000,
		SelectRows: 10, RouteFrameNodes: 12,
	}
}

func New(file *nativestore.File) (*Service, error) {
	if file == nil {
		return nil, configError(result.CodeInternal, "native configuration file is required")
	}
	service := &Service{file: file}
	if _, err := service.Current(); errors.Is(err, nativestore.ErrNotFound) {
		initial := Revision{
			Version: Version, Key: QueryBudgetsKey, Revision: 1, Budgets: Defaults(),
			Actor: "engine:bootstrap", Reason: "materialize database query budget defaults",
			RecordedAt: time.Now().UTC(),
		}
		if err := service.put(initial); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return service, nil
}

func Existing(file *nativestore.File) *Service {
	return &Service{file: file}
}

func (service *Service) Current() (Revision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.current()
}

func (service *Service) current() (Revision, error) {
	revisions, err := service.history()
	if err != nil {
		return Revision{}, err
	}
	if len(revisions) == 0 {
		return Revision{}, nativestore.ErrNotFound
	}
	return revisions[len(revisions)-1], nil
}

func (service *Service) History() ([]Revision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.history()
}

func (service *Service) history() ([]Revision, error) {
	ids, err := service.file.IDs(nativestore.ObjectKindConfiguration)
	if err != nil {
		return nil, err
	}
	values := make([]Revision, 0, len(ids))
	for _, id := range ids {
		payload, err := service.file.Get(nativestore.ObjectKindConfiguration, id)
		if err != nil {
			return nil, err
		}
		var value Revision
		if err := json.Unmarshal(payload, &value); err != nil ||
			value.Version != Version || value.Key != QueryBudgetsKey ||
			value.Revision == 0 || validateBudgets(value.Budgets) != nil {
			return nil, configError(result.CodeInternal, "native query budget configuration is corrupt")
		}
		values = append(values, value)
	}
	sort.Slice(values, func(left, right int) bool { return values[left].Revision < values[right].Revision })
	for index := range values {
		if values[index].Revision != uint64(index+1) {
			return nil, configError(result.CodeInternal, "native query budget revision chain is incomplete")
		}
	}
	return values, nil
}

func (service *Service) Update(budgets QueryBudgets, expected uint64, actor, reason string) (Revision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := validateMutation(expected, actor, reason); err != nil {
		return Revision{}, err
	}
	if err := validateBudgets(budgets); err != nil {
		return Revision{}, err
	}
	current, err := service.current()
	if err != nil {
		return Revision{}, err
	}
	if current.Revision != expected {
		return Revision{}, configError(result.CodeRevisionConflict, "configuration revision conflicts with latest")
	}
	next := Revision{
		Version: Version, Key: QueryBudgetsKey, Revision: current.Revision + 1,
		Budgets: budgets, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
		RecordedAt: time.Now().UTC(),
	}
	return next, service.put(next)
}

func (service *Service) Restore(target, expected uint64, actor, reason string) (Revision, error) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if target == 0 {
		return Revision{}, configError(result.CodeValidation, "configuration restore target revision is required")
	}
	if err := validateMutation(expected, actor, reason); err != nil {
		return Revision{}, err
	}
	history, err := service.history()
	if err != nil {
		return Revision{}, err
	}
	current := history[len(history)-1]
	if current.Revision != expected {
		return Revision{}, configError(result.CodeRevisionConflict, "configuration revision conflicts with latest")
	}
	if target > uint64(len(history)) {
		return Revision{}, configError(result.CodeNotFound, "configuration target revision was not found")
	}
	next := Revision{
		Version: Version, Key: QueryBudgetsKey, Revision: current.Revision + 1,
		Budgets: history[target-1].Budgets, Actor: strings.TrimSpace(actor), Reason: strings.TrimSpace(reason),
		RestoredRevision: target, RecordedAt: time.Now().UTC(),
	}
	return next, service.put(next)
}

func (service *Service) put(value Revision) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%s_r%020d", QueryBudgetsKey, value.Revision)
	if err := service.file.Put(nativestore.ObjectKindConfiguration, recordSchema, id, payload); err != nil {
		return err
	}
	return nil
}

func (service *Service) StageHistory(transaction *nativestore.Transaction, values []Revision) error {
	if transaction == nil {
		return configError(result.CodeInternal, "native configuration transaction is required")
	}
	for index, value := range values {
		if value.Version != Version || value.Key != QueryBudgetsKey ||
			value.Revision != uint64(index+1) || validateBudgets(value.Budgets) != nil ||
			strings.TrimSpace(value.Actor) == "" || strings.TrimSpace(value.Reason) == "" {
			return configError(result.CodeValidation, "logical snapshot query budget history is invalid")
		}
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		id := fmt.Sprintf("%s_r%020d", QueryBudgetsKey, value.Revision)
		if err := transaction.Put(nativestore.ObjectKindConfiguration, recordSchema, id, payload); err != nil {
			return err
		}
	}
	return nil
}

func validateMutation(expected uint64, actor, reason string) error {
	if expected == 0 {
		return configError(result.CodeValidation, "configuration mutation requires expected revision")
	}
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" {
		return configError(result.CodeValidation, "configuration mutation requires actor and reason")
	}
	return nil
}

func validateBudgets(value QueryBudgets) error {
	if value.RouteChildren < 1 || value.RouteChildren > 100 {
		return configError(result.CodeConstraint, "route_children must be between 1 and 100")
	}
	if value.OpenLocators < 1 || value.OpenLocators > 100 {
		return configError(result.CodeConstraint, "open_locators must be between 1 and 100")
	}
	if value.SelectScan < 1 || value.SelectScan > 1000 {
		return configError(result.CodeConstraint, "select_scan must be between 1 and 1000")
	}
	if value.SelectRows < 1 || value.SelectRows > value.SelectScan {
		return configError(result.CodeConstraint, "select_rows must be between 1 and select_scan")
	}
	if value.RouteFrameNodes < 1 || value.RouteFrameNodes > 100 {
		return configError(result.CodeConstraint, "route_frame_nodes must be between 1 and 100")
	}
	return nil
}

func configError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
