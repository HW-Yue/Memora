// Package fulltext holds the object kinds and posting shape shared by the
// lexical read paths. The postings themselves live in an ordinary SQLite table
// (ADR-0011); this package carries no index implementation.
package fulltext

// ObjectKind names what a posting points at.
type ObjectKind string

const (
	KindDatabase ObjectKind = "database"
	KindTable    ObjectKind = "table"
	KindColumn   ObjectKind = "column"
	KindRoute    ObjectKind = "route"
	KindRow      ObjectKind = "row"
)

// Posting is one term occurrence on one object field.
type Posting struct {
	Term       string
	Kind       ObjectKind
	DatabaseID string
	TableID    string
	ObjectID   string
	Revision   uint64
	FieldID    string
	Frequency  uint64
}
