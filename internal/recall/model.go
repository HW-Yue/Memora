// Package recall holds the shape a recall path answers with, shared by the
// storage layer and the executor. Recall answers one question — where a match
// sits in the semantic tree — so a hit is a path and nothing else: no score, no
// reason, no content. See docs/product/query-model.md §6.
package recall

// Segment is one step of a semantic path, root-first. Every segment carries the
// stable id the next navigation statement needs, so a recalled path can be
// re-entered layer by layer.
type Segment struct {
	Name    string `json:"name"`
	RouteID string `json:"route_id"`
}

// Hit is one recalled position, with the object it locates when that object is
// a Row. Kind is the terminal node's kind: only a leaf carries an ObjectID.
type Hit struct {
	Database string
	Table    string
	Path     []Segment
	Kind     string
	ObjectID string
}

// VectorStatus says how much of a scope a vector path can actually answer for.
// It is derived from the truth columns on the units, never stored: a count that
// had to be maintained would eventually disagree with the vectors themselves.
type VectorStatus struct {
	// NotReady counts units with no vector, a vector for text they no longer
	// hold, or a vector from another identity.
	NotReady int
	// IdentityLocked reports whether the Database has accepted a vector yet. It
	// separates "nobody configured embeddings" from "some units are stale",
	// which need different recovery.
	IdentityLocked bool
	Model          string
	Dimensions     int
}
