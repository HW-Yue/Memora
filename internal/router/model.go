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

// PathSegment is one step of an implicit route path: the Agent names it and
// gives its purpose, and the engine creates it only when it is missing. Both
// fields are required, and the kind is explicit rather than inferred from the
// position — the engine invents neither. See
// docs/query/implicit-route-path-v1.md.
type PathSegment struct {
	Name    string `json:"name"`
	Kind    Kind   `json:"kind"`
	Purpose string `json:"purpose"`
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
	RowID string `json:"row_id,omitempty"`
	// RowRevision is the version of the Row named by RowID — the fact's version,
	// not this node's. Revision below is the Route's own: it moves when the node
	// is renamed, re-purposed or re-mounted, and it does not move when the fact
	// under it is edited. Handing one where the other is expected is a revision
	// conflict on an object nobody touched, so the listing names them apart.
	//
	// It is never stored. The Row owns it, and a copy in this node's body could
	// only go stale — so it is filled by the read that resolves the mount, the
	// same resolution OPEN ROUTE performs.
	RowRevision uint64 `json:"-"`
	Revision    uint64 `json:"revision"`
	Deleted     bool   `json:"deleted"`
}

type Locator struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	Revision   uint64 `json:"revision"`
}
