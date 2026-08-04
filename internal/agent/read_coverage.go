package agent

import "errors"

const (
	ReadExtentVersion          = "memora.read-extent/v1"
	ReadCoverageReceiptVersion = "memora.read-coverage-receipt/v1"
)

var (
	ErrReadCoverageInvalid  = errors.New("read coverage is invalid")
	ErrReadCoverageBudget   = errors.New("read extent exceeds its budget")
	ErrReadCoverageConflict = errors.New("read coverage confirmation conflicts")
	ErrReadCoverageComplete = errors.New("read coverage is complete")
)

type ReadExtentLimits struct {
	MaxNodes     int
	MaxUTF8Bytes int
}

type ReadExtentContext struct {
	NodeID string
	Kind   DocumentNodeKind
	Label  string
}

type ReadExtentNode struct {
	NodeID  string
	Kind    DocumentNodeKind
	Label   string
	Text    string
	Anchor  DocumentAnchor
	Context []ReadExtentContext
}

type ReadExtent struct {
	Version        string
	DocumentSHA256 string
	Sequence       uint64
	StartPosition  uint64
	EndPosition    uint64
	Nodes          []ReadExtentNode
	SHA256         string
}

type ReadCoverageReceipt struct {
	Version        string
	DocumentSHA256 string
	Revision       uint64
	CoveredNodes   uint64
	TotalNodes     uint64
	Complete       bool
	Replay         bool
}

type ReadCoverage struct{}

func NewReadCoverage(DocumentIR) (*ReadCoverage, error) {
	return nil, ErrReadCoverageInvalid
}

func RestoreReadCoverage(DocumentIR, []byte) (*ReadCoverage, error) {
	return nil, ErrReadCoverageInvalid
}

func (*ReadCoverage) Next(ReadExtentLimits) (ReadExtent, error) {
	return ReadExtent{}, ErrReadCoverageInvalid
}

func (*ReadCoverage) Acknowledge(uint64, string) (ReadCoverageReceipt, error) {
	return ReadCoverageReceipt{}, ErrReadCoverageInvalid
}

func (*ReadCoverage) Receipt() ReadCoverageReceipt {
	return ReadCoverageReceipt{}
}

func (*ReadCoverage) Checkpoint() ([]byte, error) {
	return nil, ErrReadCoverageInvalid
}
