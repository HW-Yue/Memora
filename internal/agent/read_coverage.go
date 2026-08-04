package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

const (
	ReadExtentVersion             = "memora.read-extent/v1"
	ReadCoverageReceiptVersion    = "memora.read-coverage-receipt/v1"
	MaxReadExtentNodes            = 128
	MaxReadExtentUTF8Bytes        = 1 << 20
	readCoverageCheckpointVersion = "memora.read-coverage-checkpoint/v1"
	maxReadCoverageCheckpointSize = 16 << 10
)

var (
	ErrReadCoverageInvalid  = errors.New("read coverage is invalid")
	ErrReadCoverageBudget   = errors.New("read extent exceeds its budget")
	ErrReadCoverageConflict = errors.New("read coverage confirmation conflicts")
	ErrReadCoverageComplete = errors.New("read coverage is complete")
)

type ReadExtentLimits struct {
	MaxNodes     int `json:"max_nodes"`
	MaxUTF8Bytes int `json:"max_utf8_bytes"`
}

type ReadExtentContext struct {
	NodeID string           `json:"node_id"`
	Kind   DocumentNodeKind `json:"kind"`
	Label  string           `json:"label,omitempty"`
}

type ReadExtentNode struct {
	NodeID  string              `json:"node_id"`
	Kind    DocumentNodeKind    `json:"kind"`
	Label   string              `json:"label,omitempty"`
	Text    string              `json:"text,omitempty"`
	Anchor  DocumentAnchor      `json:"anchor"`
	Context []ReadExtentContext `json:"context"`
}

type ReadExtent struct {
	Version        string           `json:"version"`
	DocumentSHA256 string           `json:"document_sha256"`
	Sequence       uint64           `json:"sequence"`
	StartPosition  uint64           `json:"start_position"`
	EndPosition    uint64           `json:"end_position"`
	Nodes          []ReadExtentNode `json:"nodes"`
	SHA256         string           `json:"sha256"`
}

type ReadCoverageReceipt struct {
	Version        string `json:"version"`
	DocumentSHA256 string `json:"document_sha256"`
	Revision       uint64 `json:"revision"`
	CoveredNodes   uint64 `json:"covered_nodes"`
	TotalNodes     uint64 `json:"total_nodes"`
	Complete       bool   `json:"complete"`
	Replay         bool   `json:"replay"`
}

type readCoverageWindow struct {
	Sequence uint64 `json:"sequence"`
	Start    uint64 `json:"start"`
	End      uint64 `json:"end"`
	SHA256   string `json:"sha256"`
}

type readCoverageCheckpoint struct {
	Version        string              `json:"version"`
	DocumentSHA256 string              `json:"document_sha256"`
	Revision       uint64              `json:"revision"`
	CoveredNodes   uint64              `json:"covered_nodes"`
	NextSequence   uint64              `json:"next_sequence"`
	Pending        *readCoverageWindow `json:"pending,omitempty"`
	LastAck        *readCoverageWindow `json:"last_ack,omitempty"`
	SHA256         string              `json:"sha256"`
}

type ReadCoverage struct {
	mu           sync.Mutex
	document     DocumentIR
	nodes        map[string]DocumentNode
	revision     uint64
	coveredNodes uint64
	nextSequence uint64
	pending      *readCoverageWindow
	lastAck      *readCoverageWindow
}

func NewReadCoverage(document DocumentIR) (*ReadCoverage, error) {
	if err := document.Validate(); err != nil {
		return nil, ErrReadCoverageInvalid
	}
	return newReadCoverage(document), nil
}

func newReadCoverage(document DocumentIR) *ReadCoverage {
	document = CloneDocumentIR(document)
	nodes := make(map[string]DocumentNode, len(document.Nodes))
	for _, node := range document.Nodes {
		nodes[node.ID] = node
	}
	return &ReadCoverage{
		document: document, nodes: nodes, nextSequence: 1,
	}
}

func RestoreReadCoverage(document DocumentIR, encoded []byte) (*ReadCoverage, error) {
	if err := document.Validate(); err != nil || len(encoded) == 0 || len(encoded) > maxReadCoverageCheckpointSize {
		return nil, ErrReadCoverageInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var checkpoint readCoverageCheckpoint
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, ErrReadCoverageInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, ErrReadCoverageInvalid
	}
	if err := validateReadCoverageCheckpoint(document, checkpoint); err != nil {
		return nil, err
	}
	coverage := newReadCoverage(document)
	coverage.revision = checkpoint.Revision
	coverage.coveredNodes = checkpoint.CoveredNodes
	coverage.nextSequence = checkpoint.NextSequence
	coverage.pending = cloneReadCoverageWindow(checkpoint.Pending)
	coverage.lastAck = cloneReadCoverageWindow(checkpoint.LastAck)
	if coverage.lastAck != nil {
		extent, err := coverage.extentForWindow(*coverage.lastAck)
		if err != nil || extent.SHA256 != coverage.lastAck.SHA256 {
			return nil, ErrReadCoverageInvalid
		}
	}
	if coverage.pending != nil {
		extent, err := coverage.extentForWindow(*coverage.pending)
		if err != nil || extent.SHA256 != coverage.pending.SHA256 {
			return nil, ErrReadCoverageInvalid
		}
	}
	return coverage, nil
}

func (coverage *ReadCoverage) Next(limits ReadExtentLimits) (ReadExtent, error) {
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	if coverage.pending != nil {
		return coverage.extentForWindow(*coverage.pending)
	}
	if coverage.coveredNodes == uint64(len(coverage.document.ReadingOrder)) {
		return ReadExtent{}, ErrReadCoverageComplete
	}
	if limits.MaxNodes <= 0 || limits.MaxNodes > MaxReadExtentNodes ||
		limits.MaxUTF8Bytes <= 0 || limits.MaxUTF8Bytes > MaxReadExtentUTF8Bytes {
		return ReadExtent{}, ErrReadCoverageBudget
	}

	start := coverage.coveredNodes
	end := start
	usedBytes := 0
	for end < uint64(len(coverage.document.ReadingOrder)) && int(end-start) < limits.MaxNodes {
		node, ok := coverage.extentNode(coverage.document.ReadingOrder[end])
		if !ok {
			return ReadExtent{}, ErrReadCoverageInvalid
		}
		nodeBytes := readExtentNodeBytes(node)
		if nodeBytes > limits.MaxUTF8Bytes && end == start {
			return ReadExtent{}, ErrReadCoverageBudget
		}
		if usedBytes+nodeBytes > limits.MaxUTF8Bytes {
			break
		}
		usedBytes += nodeBytes
		end++
	}
	if end == start {
		return ReadExtent{}, ErrReadCoverageBudget
	}
	window := readCoverageWindow{
		Sequence: coverage.nextSequence, Start: start, End: end,
	}
	extent, err := coverage.extentForWindow(window)
	if err != nil {
		return ReadExtent{}, err
	}
	window.SHA256 = extent.SHA256
	coverage.pending = &window
	coverage.nextSequence++
	coverage.revision++
	return extent, nil
}

func (coverage *ReadCoverage) Acknowledge(sequence uint64, digest string) (ReadCoverageReceipt, error) {
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	if coverage.lastAck != nil && sequence == coverage.lastAck.Sequence && digest == coverage.lastAck.SHA256 {
		receipt := coverage.receiptLocked()
		receipt.Replay = true
		return receipt, nil
	}
	if coverage.pending == nil || sequence != coverage.pending.Sequence || digest != coverage.pending.SHA256 {
		return ReadCoverageReceipt{}, ErrReadCoverageConflict
	}
	coverage.coveredNodes = coverage.pending.End
	coverage.lastAck = cloneReadCoverageWindow(coverage.pending)
	coverage.pending = nil
	coverage.revision++
	return coverage.receiptLocked(), nil
}

func (coverage *ReadCoverage) Receipt() ReadCoverageReceipt {
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	return coverage.receiptLocked()
}

func (coverage *ReadCoverage) receiptLocked() ReadCoverageReceipt {
	return ReadCoverageReceipt{
		Version: ReadCoverageReceiptVersion, DocumentSHA256: coverage.document.SHA256,
		Revision: coverage.revision, CoveredNodes: coverage.coveredNodes,
		TotalNodes: uint64(len(coverage.document.ReadingOrder)),
		Complete:   coverage.pending == nil && coverage.coveredNodes == uint64(len(coverage.document.ReadingOrder)),
	}
}

func (coverage *ReadCoverage) Checkpoint() ([]byte, error) {
	coverage.mu.Lock()
	defer coverage.mu.Unlock()
	checkpoint := readCoverageCheckpoint{
		Version: readCoverageCheckpointVersion, DocumentSHA256: coverage.document.SHA256,
		Revision: coverage.revision, CoveredNodes: coverage.coveredNodes,
		NextSequence: coverage.nextSequence, Pending: cloneReadCoverageWindow(coverage.pending),
		LastAck: cloneReadCoverageWindow(coverage.lastAck),
	}
	digest, err := readCoverageCheckpointDigest(checkpoint)
	if err != nil {
		return nil, ErrReadCoverageInvalid
	}
	checkpoint.SHA256 = digest
	encoded, err := json.Marshal(checkpoint)
	if err != nil || len(encoded) > maxReadCoverageCheckpointSize {
		return nil, ErrReadCoverageInvalid
	}
	return encoded, nil
}

func (coverage *ReadCoverage) extentForWindow(window readCoverageWindow) (ReadExtent, error) {
	if window.Sequence == 0 || window.Start >= window.End || window.End > uint64(len(coverage.document.ReadingOrder)) {
		return ReadExtent{}, ErrReadCoverageInvalid
	}
	extent := ReadExtent{
		Version: ReadExtentVersion, DocumentSHA256: coverage.document.SHA256,
		Sequence: window.Sequence, StartPosition: window.Start, EndPosition: window.End,
		Nodes: make([]ReadExtentNode, 0, window.End-window.Start),
	}
	for position := window.Start; position < window.End; position++ {
		node, ok := coverage.extentNode(coverage.document.ReadingOrder[position])
		if !ok {
			return ReadExtent{}, ErrReadCoverageInvalid
		}
		extent.Nodes = append(extent.Nodes, node)
	}
	digest, err := readExtentDigest(extent)
	if err != nil {
		return ReadExtent{}, ErrReadCoverageInvalid
	}
	extent.SHA256 = digest
	return cloneReadExtent(extent), nil
}

func (coverage *ReadCoverage) extentNode(nodeID string) (ReadExtentNode, bool) {
	node, ok := coverage.nodes[nodeID]
	if !ok {
		return ReadExtentNode{}, false
	}
	var reversed []ReadExtentContext
	for parentID := node.ParentID; parentID != ""; {
		parent, exists := coverage.nodes[parentID]
		if !exists {
			return ReadExtentNode{}, false
		}
		reversed = append(reversed, ReadExtentContext{NodeID: parent.ID, Kind: parent.Kind, Label: parent.Label})
		parentID = parent.ParentID
	}
	context := make([]ReadExtentContext, len(reversed))
	for index := range reversed {
		context[len(reversed)-1-index] = reversed[index]
	}
	return ReadExtentNode{
		NodeID: node.ID, Kind: node.Kind, Label: node.Label, Text: node.Text,
		Anchor: node.Anchor, Context: context,
	}, true
}

func validateReadCoverageCheckpoint(document DocumentIR, checkpoint readCoverageCheckpoint) error {
	if checkpoint.Version != readCoverageCheckpointVersion || checkpoint.DocumentSHA256 != document.SHA256 ||
		checkpoint.NextSequence == 0 || checkpoint.CoveredNodes > uint64(len(document.ReadingOrder)) ||
		!validSourceSHA256(checkpoint.SHA256) {
		return ErrReadCoverageInvalid
	}
	digest, err := readCoverageCheckpointDigest(checkpoint)
	if err != nil || digest != checkpoint.SHA256 {
		return ErrReadCoverageInvalid
	}
	completed := uint64(0)
	if checkpoint.LastAck != nil {
		completed = checkpoint.LastAck.Sequence
		if completed == 0 || checkpoint.LastAck.Start >= checkpoint.LastAck.End ||
			checkpoint.LastAck.End != checkpoint.CoveredNodes || !validSourceSHA256(checkpoint.LastAck.SHA256) {
			return ErrReadCoverageInvalid
		}
	} else if checkpoint.CoveredNodes != 0 {
		return ErrReadCoverageInvalid
	}
	wantRevision := completed * 2
	wantNextSequence := completed + 1
	if checkpoint.Pending != nil {
		if checkpoint.Pending.Sequence != completed+1 || checkpoint.Pending.Start != checkpoint.CoveredNodes ||
			checkpoint.Pending.Start >= checkpoint.Pending.End ||
			checkpoint.Pending.End > uint64(len(document.ReadingOrder)) ||
			!validSourceSHA256(checkpoint.Pending.SHA256) {
			return ErrReadCoverageInvalid
		}
		wantRevision++
		wantNextSequence++
	}
	if checkpoint.Revision != wantRevision || checkpoint.NextSequence != wantNextSequence {
		return ErrReadCoverageInvalid
	}
	return nil
}

func readExtentNodeBytes(node ReadExtentNode) int {
	total := len(node.Label) + len(node.Text)
	for _, context := range node.Context {
		total += len(context.Label)
	}
	return total
}

func readExtentDigest(extent ReadExtent) (string, error) {
	extent.SHA256 = ""
	encoded, err := json.Marshal(extent)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func readCoverageCheckpointDigest(checkpoint readCoverageCheckpoint) (string, error) {
	checkpoint.SHA256 = ""
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func cloneReadCoverageWindow(window *readCoverageWindow) *readCoverageWindow {
	if window == nil {
		return nil
	}
	cloned := *window
	return &cloned
}

func cloneReadExtent(extent ReadExtent) ReadExtent {
	extent.Nodes = append([]ReadExtentNode(nil), extent.Nodes...)
	for index := range extent.Nodes {
		extent.Nodes[index].Context = append([]ReadExtentContext(nil), extent.Nodes[index].Context...)
	}
	return extent
}
