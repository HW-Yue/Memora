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

// Receipt reports what one bounded repair pass did. Repaired and Discarded are
// both decisions: the entry was applied, or it was judged no longer worth
// applying, and either way it leaves the queue. Failed is neither — the pass
// could not read what it needed, so it decided nothing and the entry is still
// queued. Failures says which endpoints those were, because a count that only
// goes up is not something an Agent can act on.
type Receipt struct {
	Repaired  int
	Discarded int
	Failed    int
	Remaining int
	Failures  []Failure
}

// Failure names one queued endpoint a pass could not finish, and why.
type Failure struct {
	RowID            string
	CounterpartRowID string
	Reason           string
	Message          string
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
