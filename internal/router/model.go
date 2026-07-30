package router

const Version = "memora.router/v1"

type Kind string

const (
	KindRoot   Kind = "root"
	KindBranch Kind = "branch"
	KindLeaf   Kind = "leaf"
)

type NodeDefinition struct {
	Name     string
	Kind     Kind
	Purpose  string
	Synopsis string
}

type Node struct {
	Version    string   `json:"version"`
	ID         string   `json:"route_id"`
	DatabaseID string   `json:"database_id"`
	TableID    string   `json:"table_id,omitempty"`
	ParentID   string   `json:"parent_id,omitempty"`
	Name       string   `json:"name"`
	Aliases    []string `json:"aliases"`
	Path       string   `json:"path"`
	Kind       Kind     `json:"kind"`
	Purpose    string   `json:"purpose"`
	Synopsis   string   `json:"synopsis,omitempty"`
	Revision   uint64   `json:"revision"`
	Deleted    bool     `json:"deleted"`
}

type Locator struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Revision   uint64 `json:"revision"`
}

type Membership struct {
	LeafID             string `json:"leaf_id"`
	MembershipRevision uint64 `json:"membership_revision,omitempty"`
	Deleted            bool   `json:"deleted,omitempty"`
	Locator
}

type stringIndex struct {
	Version string   `json:"version"`
	Values  []string `json:"values"`
}

type locatorIndex struct {
	Version  string    `json:"version"`
	Locators []Locator `json:"locators"`
}
