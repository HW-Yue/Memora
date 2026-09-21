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

// VectorReceipt reports what one bounded vector-index repair pass did. There is
// no "discarded" here: the pass repairs derived rows, so nothing is ever judged
// no longer worth repairing.
type VectorReceipt struct {
	Repaired  int
	Remaining int
}

// RecallReceipt reports what one bounded recall-unit repair pass did. Dropped
// counts units whose Row is gone: they point at a position that is not there.
type RecallReceipt struct {
	Rebuilt   int
	Dropped   int
	Remaining int
}
