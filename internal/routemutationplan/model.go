package routemutationplan

import (
	"context"

	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/router"
)

const (
	ProposalVersion = "memora.route-mutation-proposal/v1"
	PlanVersion     = "memora.route-mutation-plan/v1"
	ReceiptVersion  = "memora.route-mutation-receipt/v1"
)

type Operation string

const (
	OperationSplit Operation = "SPLIT"
	OperationMerge Operation = "MERGE"
	OperationMove  Operation = "MOVE"
)

type Status string

const StatusReviewRequired Status = "review_required"

type Scope struct {
	DatabaseID string `json:"database_id"`
	Database   string `json:"database,omitempty"`
	TableID    string `json:"table_id"`
	Table      string `json:"table,omitempty"`
}

type SourceRef struct {
	RouteID          string `json:"route_id"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

type TargetProposal struct {
	Key           string   `json:"key"`
	Name          string   `json:"name"`
	Purpose       string   `json:"purpose"`
	Synopsis      string   `json:"synopsis,omitempty"`
	ChildRouteIDs []string `json:"child_route_ids,omitempty"`
	RowIDs        []string `json:"row_ids,omitempty"`
}

type Proposal struct {
	Version        string           `json:"version"`
	ID             string           `json:"proposal_id"`
	Operation      Operation        `json:"operation"`
	Actor          string           `json:"actor"`
	SourceEventID  string           `json:"source_event_id"`
	Reason         string           `json:"reason"`
	Sources        []SourceRef      `json:"sources"`
	TargetParentID string           `json:"target_parent_id,omitempty"`
	Targets        []TargetProposal `json:"targets,omitempty"`
}

type NodeGuard struct {
	RouteID  string `json:"route_id"`
	Revision uint64 `json:"revision"`
}

type ChildSetGuard struct {
	ParentRouteID string   `json:"parent_route_id"`
	ChildRouteIDs []string `json:"child_route_ids"`
}

type LocatorGuard struct {
	RowID    string `json:"row_id"`
	Revision uint64 `json:"revision"`
}

type LocatorSetGuard struct {
	LeafRouteID string         `json:"leaf_route_id"`
	Locators    []LocatorGuard `json:"locators"`
}

type NodeCreate struct {
	TargetKey string      `json:"target_key"`
	RouteID   string      `json:"route_id"`
	ParentID  string      `json:"parent_id"`
	Name      string      `json:"name"`
	Kind      router.Kind `json:"kind"`
	Purpose   string      `json:"purpose"`
	Synopsis  string      `json:"synopsis,omitempty"`
}

type NodeMove struct {
	RouteID      string `json:"route_id"`
	FromParentID string `json:"from_parent_id"`
	ToParentID   string `json:"to_parent_id"`
}

type MembershipMove struct {
	RowID       string   `json:"row_id"`
	Revision    uint64   `json:"revision"`
	FromLeafIDs []string `json:"from_leaf_ids"`
	ToLeafID    string   `json:"to_leaf_id"`
}

type NodeDelete struct {
	RouteID  string `json:"route_id"`
	Revision uint64 `json:"revision"`
}

type Impact struct {
	CreatedNodes     int `json:"created_nodes"`
	ReparentedNodes  int `json:"reparented_nodes"`
	MovedMemberships int `json:"moved_memberships"`
	DeletedNodes     int `json:"deleted_nodes"`
}

type Plan struct {
	Version          string            `json:"version"`
	PlanID           string            `json:"plan_id"`
	ProposalID       string            `json:"proposal_id"`
	Operation        Operation         `json:"operation"`
	Status           Status            `json:"status"`
	Scope            Scope             `json:"scope"`
	Actor            string            `json:"actor"`
	SourceEventID    string            `json:"source_event_id"`
	Reason           string            `json:"reason"`
	BaseSnapshotHash string            `json:"base_snapshot_hash"`
	NodeGuards       []NodeGuard       `json:"node_guards"`
	ChildSetGuards   []ChildSetGuard   `json:"child_set_guards"`
	LocatorSetGuards []LocatorSetGuard `json:"locator_set_guards"`
	Creates          []NodeCreate      `json:"creates"`
	Moves            []NodeMove        `json:"moves"`
	MembershipMoves  []MembershipMove  `json:"membership_moves"`
	Deletes          []NodeDelete      `json:"deletes"`
	Impact           Impact            `json:"impact"`
	Hash             string            `json:"hash"`
}

type Receipt struct {
	Version             string    `json:"version"`
	PlanID              string    `json:"plan_id"`
	PlanHash            string    `json:"plan_hash"`
	Operation           Operation `json:"operation"`
	Status              string    `json:"status"`
	ChangeSequence      uint64    `json:"change_sequence"`
	CreatedNodes        int       `json:"created_nodes"`
	UpdatedNodes        int       `json:"updated_nodes"`
	DeletedNodes        int       `json:"deleted_nodes"`
	MembershipRevisions int       `json:"membership_revisions"`
	Verified            bool      `json:"verified"`
}

type Source interface {
	ListRouterNodes(context.Context) ([]router.Node, error)
	ListRouterLeafPage(context.Context, string, string, int) ([]router.Locator, router.ReadPage, error)
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return "Route mutation plan: " + err.Message }
func (err *Error) StableCode() string { return string(err.Code) }
