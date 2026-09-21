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

// VectorIdentity is the (model, dimensions) pair a Database commits to the first
// time it accepts an embedding. Vectors from another model are not lower
// quality, they are incomparable, and recall returns paths with no scores, so a
// mixed index would be silently wrong rather than visibly bad.
type VectorIdentity struct {
	Model      string
	Dimensions int
	LockedAt   string
}

// VectorRecord is one embedding a host computed, offered for one unit. The
// content hash is the handshake: it is the hash of the text the host embedded,
// and the engine recomputes it from what the unit actually holds.
type VectorRecord struct {
	UnitNo      int64
	ContentHash string
	Model       string
	Dimensions  int
	Vector      []float32
}

// PendingUnit is one unit a host still has to embed: the unit, the Table it
// belongs to, the hash the vector must be computed for, and the text itself.
//
// Handing the text over is the point — the host cannot embed what it cannot
// read — and it is not a hole in the recall contract: recall still answers with
// positions and no content. This is the work list, not an answer.
type PendingUnit struct {
	UnitNo      int64
	Table       string
	ContentHash string
	Payload     string
}

// UnitVectorState is what happened to one written Row's vector: whether the Row
// has a recallable unit at all, and whether that unit holds a vector the vector
// path can answer with. It is per Row on purpose — a write that attached a
// vector must be able to say what became of *that* Row's vector, not how many
// units somewhere in the scope are missing one.
type UnitVectorState struct {
	HasUnit     bool
	UnitNo      int64
	Ready       bool
	ContentHash string
	Model       string
	Dimensions  int
}
