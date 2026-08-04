package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const (
	DocumentIRVersion            = "memora.document-ir/v1"
	MaxDocumentNodeTextBytes     = 1 << 20
	maxDocumentNodes             = 100_000
	maxDocumentResources         = 10_000
	maxDocumentReferences        = 200_000
	maxDocumentTotalTextBytes    = 64 << 20
	maxDocumentIRJSONBytes       = 96 << 20
	documentNodeDigestPrefix     = "node:sha256:"
	documentResourceDigestPrefix = "resource:sha256:"
	documentIDDigestPrefix       = "document:sha256:"
)

var (
	ErrDocumentIRInvalid = errors.New("Document IR is invalid")
	ErrDocumentIRBudget  = errors.New("Document IR exceeds its budget")
	ErrDocumentIRDigest  = errors.New("Document IR digest does not match")
)

type DocumentIR struct {
	Version      string              `json:"version"`
	DocumentID   string              `json:"document_id"`
	Source       SourceReference     `json:"source"`
	Title        string              `json:"title"`
	Language     string              `json:"language"`
	Resources    []DocumentResource  `json:"resources"`
	Nodes        []DocumentNode      `json:"nodes"`
	ReadingOrder []string            `json:"reading_order"`
	References   []DocumentReference `json:"references,omitempty"`
	SHA256       string              `json:"sha256"`
}

type DocumentResource struct {
	ID        string `json:"id"`
	Locator   string `json:"locator"`
	MediaType string `json:"media_type"`
	Bytes     uint64 `json:"bytes"`
	SHA256    string `json:"sha256"`
}

type DocumentAnchor struct {
	ResourceID string `json:"resource_id"`
	Start      uint64 `json:"start"`
	End        uint64 `json:"end"`
	Selector   string `json:"selector,omitempty"`
}

type DocumentNodeKind string

const (
	DocumentNodeDocument  DocumentNodeKind = "document"
	DocumentNodePart      DocumentNodeKind = "part"
	DocumentNodeChapter   DocumentNodeKind = "chapter"
	DocumentNodeSection   DocumentNodeKind = "section"
	DocumentNodeHeading   DocumentNodeKind = "heading"
	DocumentNodeParagraph DocumentNodeKind = "paragraph"
	DocumentNodeList      DocumentNodeKind = "list"
	DocumentNodeListItem  DocumentNodeKind = "list_item"
	DocumentNodeTable     DocumentNodeKind = "table"
	DocumentNodeTableRow  DocumentNodeKind = "table_row"
	DocumentNodeTableCell DocumentNodeKind = "table_cell"
	DocumentNodeFootnote  DocumentNodeKind = "footnote"
	DocumentNodeQuote     DocumentNodeKind = "quote"
	DocumentNodeCode      DocumentNodeKind = "code"
	DocumentNodeImage     DocumentNodeKind = "image"
)

type DocumentNode struct {
	ID       string           `json:"id"`
	ParentID string           `json:"parent_id,omitempty"`
	Kind     DocumentNodeKind `json:"kind"`
	Ordinal  uint64           `json:"ordinal"`
	Label    string           `json:"label,omitempty"`
	Text     string           `json:"text,omitempty"`
	Anchor   DocumentAnchor   `json:"anchor"`
}

type DocumentReferenceKind string

const (
	DocumentReferenceFootnote DocumentReferenceKind = "footnote"
	DocumentReferenceCitation DocumentReferenceKind = "citation"
	DocumentReferenceCaption  DocumentReferenceKind = "caption"
	DocumentReferenceInternal DocumentReferenceKind = "internal"
)

type DocumentReference struct {
	FromNodeID   string                `json:"from_node_id"`
	Kind         DocumentReferenceKind `json:"kind"`
	TargetNodeID string                `json:"target_node_id"`
	Label        string                `json:"label,omitempty"`
}

func SealDocumentIR(draft DocumentIR) (DocumentIR, error) {
	if draft.SHA256 != "" {
		return DocumentIR{}, ErrDocumentIRInvalid
	}
	sealed := CloneDocumentIR(draft)
	if err := validateDocumentIRStructure(sealed); err != nil {
		return DocumentIR{}, err
	}
	digest, err := documentIRDigest(sealed)
	if err != nil {
		return DocumentIR{}, ErrDocumentIRInvalid
	}
	sealed.SHA256 = digest
	return sealed, nil
}

func (document DocumentIR) Validate() error {
	if err := validateDocumentIRStructure(document); err != nil {
		return err
	}
	if !validSourceSHA256(document.SHA256) {
		return ErrDocumentIRDigest
	}
	digest, err := documentIRDigest(document)
	if err != nil || digest != document.SHA256 {
		return ErrDocumentIRDigest
	}
	return nil
}

func DecodeDocumentIR(encoded []byte) (DocumentIR, error) {
	if len(encoded) == 0 || len(encoded) > maxDocumentIRJSONBytes {
		return DocumentIR{}, ErrDocumentIRInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var document DocumentIR
	if err := decoder.Decode(&document); err != nil {
		return DocumentIR{}, ErrDocumentIRInvalid
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return DocumentIR{}, ErrDocumentIRInvalid
	}
	if err := document.Validate(); err != nil {
		return DocumentIR{}, err
	}
	return CloneDocumentIR(document), nil
}

func CloneDocumentIR(document DocumentIR) DocumentIR {
	document.Resources = append([]DocumentResource(nil), document.Resources...)
	document.Nodes = append([]DocumentNode(nil), document.Nodes...)
	document.ReadingOrder = append([]string(nil), document.ReadingOrder...)
	document.References = append([]DocumentReference(nil), document.References...)
	return document
}

func StableDocumentResourceID(sourceSHA256, locator string) (string, error) {
	if !validSourceSHA256(sourceSHA256) || !validDocumentLocator(locator) {
		return "", ErrDocumentIRInvalid
	}
	return documentResourceDigestPrefix + stableDocumentDigest(
		"memora.document-resource/v1", sourceSHA256, locator,
	), nil
}

func StableDocumentID(sourceSHA256 string) (string, error) {
	if !validSourceSHA256(sourceSHA256) {
		return "", ErrDocumentIRInvalid
	}
	return documentIDDigestPrefix + stableDocumentDigest(
		"memora.document-id/v1", sourceSHA256,
	), nil
}

func StableDocumentNodeID(
	documentID string,
	parentID string,
	kind DocumentNodeKind,
	ordinal uint64,
) (string, error) {
	if !validPrefixedDocumentDigest(documentID, documentIDDigestPrefix) ||
		(parentID != "" && !validPrefixedDocumentDigest(parentID, documentNodeDigestPrefix)) ||
		!validDocumentNodeKind(kind) || ordinal == 0 {
		return "", ErrDocumentIRInvalid
	}
	return documentNodeDigestPrefix + stableDocumentDigest(
		"memora.document-node/v1", documentID, parentID, string(kind), fmt.Sprintf("%d", ordinal),
	), nil
}

func validateDocumentIRStructure(document DocumentIR) error {
	wantDocumentID, documentIDErr := StableDocumentID(document.Source.SHA256)
	if document.Version != DocumentIRVersion || documentIDErr != nil || document.DocumentID != wantDocumentID ||
		document.Source.Validate() != nil || !validDocumentText(document.Title, 1000, true) ||
		!validSourceIntakeID(document.Language, 35) || len(document.Resources) == 0 ||
		len(document.Nodes) == 0 || len(document.ReadingOrder) == 0 {
		return ErrDocumentIRInvalid
	}
	if len(document.Resources) > maxDocumentResources || len(document.Nodes) > maxDocumentNodes ||
		len(document.References) > maxDocumentReferences {
		return ErrDocumentIRBudget
	}
	resources := make(map[string]DocumentResource, len(document.Resources))
	locators := make(map[string]struct{}, len(document.Resources))
	for _, resource := range document.Resources {
		if !validDocumentResource(document.Source.SHA256, resource) {
			return ErrDocumentIRInvalid
		}
		if _, duplicate := resources[resource.ID]; duplicate {
			return ErrDocumentIRInvalid
		}
		if _, duplicate := locators[resource.Locator]; duplicate {
			return ErrDocumentIRInvalid
		}
		resources[resource.ID] = resource
		locators[resource.Locator] = struct{}{}
	}
	nodes, children, err := validateDocumentNodes(document, resources)
	if err != nil {
		return err
	}
	if err := validateDocumentReadingOrder(document.ReadingOrder, nodes, children); err != nil {
		return err
	}
	if err := validateDocumentReferences(document.References, nodes); err != nil {
		return err
	}
	return nil
}

func validateDocumentNodes(
	document DocumentIR,
	resources map[string]DocumentResource,
) (map[string]DocumentNode, map[string]uint64, error) {
	nodes := make(map[string]DocumentNode, len(document.Nodes))
	children := make(map[string]uint64, len(document.Nodes))
	ordinals := make(map[string]map[uint64]struct{}, len(document.Nodes))
	var totalText uint64
	for index, node := range document.Nodes {
		if !validDocumentNodeKind(node.Kind) || node.Ordinal == 0 ||
			!validDocumentText(node.Label, 1000, false) || !utf8.ValidString(node.Text) {
			return nil, nil, ErrDocumentIRInvalid
		}
		if len(node.Text) > MaxDocumentNodeTextBytes {
			return nil, nil, ErrDocumentIRBudget
		}
		totalText += uint64(len(node.Text))
		if totalText > maxDocumentTotalTextBytes {
			return nil, nil, ErrDocumentIRBudget
		}
		wantID, err := StableDocumentNodeID(document.DocumentID, node.ParentID, node.Kind, node.Ordinal)
		if err != nil || node.ID != wantID {
			return nil, nil, ErrDocumentIRInvalid
		}
		if _, duplicate := nodes[node.ID]; duplicate {
			return nil, nil, ErrDocumentIRInvalid
		}
		resource, exists := resources[node.Anchor.ResourceID]
		if !exists || !validDocumentAnchor(node.Anchor, resource) {
			return nil, nil, ErrDocumentIRInvalid
		}
		if index == 0 {
			if node.Kind != DocumentNodeDocument || node.ParentID != "" || node.Ordinal != 1 {
				return nil, nil, ErrDocumentIRInvalid
			}
		} else {
			parent, exists := nodes[node.ParentID]
			if !exists || !documentNodeContainer(parent.Kind) || !validDocumentTableParent(parent.Kind, node.Kind) {
				return nil, nil, ErrDocumentIRInvalid
			}
			children[node.ParentID]++
		}
		if !documentNodeContainer(node.Kind) {
			if node.Text == "" && !(node.Kind == DocumentNodeImage && node.Label != "") {
				return nil, nil, ErrDocumentIRInvalid
			}
		} else if node.Text != "" {
			return nil, nil, ErrDocumentIRInvalid
		}
		key := node.ParentID
		if ordinals[key] == nil {
			ordinals[key] = make(map[uint64]struct{})
		}
		if _, duplicate := ordinals[key][node.Ordinal]; duplicate {
			return nil, nil, ErrDocumentIRInvalid
		}
		ordinals[key][node.Ordinal] = struct{}{}
		nodes[node.ID] = node
	}
	for _, node := range document.Nodes {
		childCount := children[node.ID]
		if documentNodeContainer(node.Kind) != (childCount > 0) {
			return nil, nil, ErrDocumentIRInvalid
		}
		if node.Kind == DocumentNodeTableRow && childCount == 0 {
			return nil, nil, ErrDocumentIRInvalid
		}
	}
	for _, siblingOrdinals := range ordinals {
		for ordinal := uint64(1); ordinal <= uint64(len(siblingOrdinals)); ordinal++ {
			if _, exists := siblingOrdinals[ordinal]; !exists {
				return nil, nil, ErrDocumentIRInvalid
			}
		}
	}
	return nodes, children, nil
}

func validateDocumentReadingOrder(
	readingOrder []string,
	nodes map[string]DocumentNode,
	children map[string]uint64,
) error {
	wantLeaves := 0
	for id := range nodes {
		if children[id] == 0 {
			wantLeaves++
		}
	}
	if len(readingOrder) != wantLeaves {
		return ErrDocumentIRInvalid
	}
	seen := make(map[string]struct{}, len(readingOrder))
	for _, nodeID := range readingOrder {
		if _, duplicate := seen[nodeID]; duplicate {
			return ErrDocumentIRInvalid
		}
		if _, exists := nodes[nodeID]; !exists || children[nodeID] != 0 {
			return ErrDocumentIRInvalid
		}
		seen[nodeID] = struct{}{}
	}
	return nil
}

func validateDocumentReferences(references []DocumentReference, nodes map[string]DocumentNode) error {
	seen := make(map[string]struct{}, len(references))
	for _, reference := range references {
		from, fromExists := nodes[reference.FromNodeID]
		target, targetExists := nodes[reference.TargetNodeID]
		if !fromExists || !targetExists || from.ID == target.ID ||
			!validDocumentReferenceKind(reference.Kind) || !validDocumentText(reference.Label, 256, false) {
			return ErrDocumentIRInvalid
		}
		if reference.Kind == DocumentReferenceFootnote && target.Kind != DocumentNodeFootnote {
			return ErrDocumentIRInvalid
		}
		if reference.Kind == DocumentReferenceCaption &&
			target.Kind != DocumentNodeImage && target.Kind != DocumentNodeTable {
			return ErrDocumentIRInvalid
		}
		key := reference.FromNodeID + "\x00" + string(reference.Kind) + "\x00" + reference.TargetNodeID
		if _, duplicate := seen[key]; duplicate {
			return ErrDocumentIRInvalid
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validDocumentResource(sourceSHA256 string, resource DocumentResource) bool {
	wantID, err := StableDocumentResourceID(sourceSHA256, resource.Locator)
	return err == nil && resource.ID == wantID && validSourceMediaType(resource.MediaType) &&
		resource.Bytes > 0 && validSourceSHA256(resource.SHA256)
}

func validDocumentAnchor(anchor DocumentAnchor, resource DocumentResource) bool {
	return anchor.ResourceID == resource.ID && anchor.End > anchor.Start &&
		anchor.End <= resource.Bytes && validDocumentText(anchor.Selector, 512, false)
}

func validDocumentLocator(value string) bool {
	if !validDocumentText(value, 2048, true) || strings.HasPrefix(value, "/") ||
		strings.Contains(value, "\\") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validDocumentText(value string, limit int, required bool) bool {
	if !utf8.ValidString(value) || len(value) > limit {
		return false
	}
	if value == "" {
		return !required
	}
	return strings.TrimSpace(value) != ""
}

func validDocumentNodeKind(kind DocumentNodeKind) bool {
	switch kind {
	case DocumentNodeDocument, DocumentNodePart, DocumentNodeChapter, DocumentNodeSection,
		DocumentNodeHeading, DocumentNodeParagraph, DocumentNodeList, DocumentNodeListItem,
		DocumentNodeTable, DocumentNodeTableRow, DocumentNodeTableCell, DocumentNodeFootnote,
		DocumentNodeQuote, DocumentNodeCode, DocumentNodeImage:
		return true
	default:
		return false
	}
}

func documentNodeContainer(kind DocumentNodeKind) bool {
	switch kind {
	case DocumentNodeDocument, DocumentNodePart, DocumentNodeChapter, DocumentNodeSection,
		DocumentNodeList, DocumentNodeTable, DocumentNodeTableRow:
		return true
	default:
		return false
	}
}

func validDocumentTableParent(parent, child DocumentNodeKind) bool {
	switch parent {
	case DocumentNodeTable:
		return child == DocumentNodeTableRow
	case DocumentNodeTableRow:
		return child == DocumentNodeTableCell
	default:
		return child != DocumentNodeTableRow && child != DocumentNodeTableCell
	}
}

func validDocumentReferenceKind(kind DocumentReferenceKind) bool {
	switch kind {
	case DocumentReferenceFootnote, DocumentReferenceCitation,
		DocumentReferenceCaption, DocumentReferenceInternal:
		return true
	default:
		return false
	}
}

func validPrefixedDocumentDigest(value, prefix string) bool {
	if len(value) != len(prefix)+sha256.Size*2 || !strings.HasPrefix(value, prefix) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, prefix))
	return err == nil
}

func stableDocumentDigest(parts ...string) string {
	digest := sha256.New()
	for _, part := range parts {
		_, _ = digest.Write([]byte(part))
		_, _ = digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil))
}

func documentIRDigest(document DocumentIR) (string, error) {
	payload := struct {
		Version      string              `json:"version"`
		DocumentID   string              `json:"document_id"`
		Source       SourceReference     `json:"source"`
		Title        string              `json:"title"`
		Language     string              `json:"language"`
		Resources    []DocumentResource  `json:"resources"`
		Nodes        []DocumentNode      `json:"nodes"`
		ReadingOrder []string            `json:"reading_order"`
		References   []DocumentReference `json:"references,omitempty"`
	}{
		Version: document.Version, DocumentID: document.DocumentID, Source: document.Source,
		Title: document.Title, Language: document.Language,
		Resources:    append([]DocumentResource(nil), document.Resources...),
		Nodes:        append([]DocumentNode(nil), document.Nodes...),
		ReadingOrder: append([]string(nil), document.ReadingOrder...),
		References:   append([]DocumentReference(nil), document.References...),
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}
