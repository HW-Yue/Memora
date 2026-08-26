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
	// RowID is the Row hanging under this leaf, empty when the leaf holds none.
	// It is always empty on a root or branch: only a leaf carries data.
	//
	// This replaces the separate Membership object. One field on the node says
	// what a whole object kind, its validation surface and three classes of
	// semantic-health problem used to say between them — and a field cannot go
	// stale against the node it lives on, which is what removes those problems
	// rather than detecting them. See docs/storage/leaf-rowid-v1.md.
	RowID    string `json:"row_id,omitempty"`
	Revision uint64 `json:"revision"`
	Deleted  bool   `json:"deleted"`
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
