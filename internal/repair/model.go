// Package repair holds the vocabulary of the lazy-repair queue: why an endpoint
// was queued, and what one repair pass did. The queue itself is an ordinary
// table in the storage layer; these names travel to the executor without it.
package repair

// Reasons an endpoint is queued.
const (
	// StaleSummary: the summary stored for a link no longer matches the revision
	// it was taken from.
	StaleSummary = "stale_summary"
	// StaleReference: the Row a link points at has been superseded, so the link
	// has to follow its successors.
	StaleReference = "stale_reference"
)

// Receipt reports what one bounded repair pass did.
type Receipt struct {
	Repaired  int
	Discarded int
	Remaining int
}
