package agent

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"strings"
)

type docxPart struct {
	locator string
	data    []byte
	dom     *docxElement
}

type docxElement struct {
	name     string
	attrs    []xml.Attr
	start    uint64
	end      uint64
	content  []docxContentPart
	children []*docxElement
}

type docxContentPart struct {
	text  string
	child *docxElement
}

type docxPendingFootnote struct {
	fromNodeID string
	footnoteID string
}

type docxIRBuilder struct {
	documentID string
	source     SourceReference
	title      string
	language   string
	resources  []DocumentResource
	byLocator  map[string]DocumentResource
	nodes      []DocumentNode
	reading    []string
	references []DocumentReference
	children   map[string]uint64
	pending    []docxPendingFootnote
	paragraphs int
	tables     int
	footnotes  int
}

func newDOCXIRBuilder(
	documentID string,
	source SourceReference,
	title string,
	language string,
	resources []DocumentResource,
	byLocator map[string]DocumentResource,
) *docxIRBuilder {
	return &docxIRBuilder{
		documentID: documentID, source: source, title: title, language: language,
		resources: append([]DocumentResource(nil), resources...), byLocator: byLocator,
		children: make(map[string]uint64),
	}
}

func (builder *docxIRBuilder) build(
	ctx context.Context,
	mainPart string,
	mainBytes []byte,
	document *docxElement,
	headingStyles map[string]struct{},
	footnotePart *docxPart,
	relationships map[string]docxRelationship,
) error {
	mainResource, exists := builder.byLocator[mainPart]
	if !exists || len(mainBytes) == 0 {
		return ErrDOCXInvalid
	}
	root, err := builder.addNode("", DocumentNodeDocument, builder.title, "", DocumentAnchor{
		ResourceID: mainResource.ID, Start: 0, End: uint64(len(mainBytes)),
	})
	if err != nil {
		return err
	}
	body := findDOCXChild(document, "body")
	activeListKey, activeListID := "", ""
	for _, element := range body.children {
		if err := ctx.Err(); err != nil {
			return err
		}
		switch element.name {
		case "p":
			listKey := docxParagraphListKey(element)
			if listKey == "" {
				activeListKey, activeListID = "", ""
			} else if listKey != activeListKey {
				list, err := builder.addNode(root.ID, DocumentNodeList, listKey, "", docxElementAnchor(mainResource, element))
				if err != nil {
					return err
				}
				activeListKey, activeListID = listKey, list.ID
			}
			if err := builder.renderParagraph(
				root.ID, activeListID, mainResource, element, headingStyles, relationships,
			); err != nil {
				return err
			}
		case "tbl":
			activeListKey, activeListID = "", ""
			if err := builder.renderTable(root.ID, mainResource, element); err != nil {
				return err
			}
		case "sectPr", "bookmarkStart", "bookmarkEnd", "proofErr", "permStart", "permEnd":
			activeListKey, activeListID = "", ""
		default:
			return ErrDOCXUnsupported
		}
	}
	if err := builder.renderFootnotes(root.ID, footnotePart); err != nil {
		return err
	}
	return builder.resolveFootnotes()
}

func (builder *docxIRBuilder) renderParagraph(
	rootID string,
	listID string,
	resource DocumentResource,
	element *docxElement,
	headingStyles map[string]struct{},
	relationships map[string]docxRelationship,
) error {
	text := docxElementText(element)
	images := findDOCXElements(element, "blip")
	if text == "" && len(images) == 0 {
		return nil
	}
	parentID, kind := rootID, DocumentNodeParagraph
	if listID != "" {
		parentID, kind = listID, DocumentNodeListItem
	} else if docxParagraphIsHeading(element, headingStyles) {
		kind = DocumentNodeHeading
	}
	var textNode DocumentNode
	if text != "" {
		node, err := builder.addNode(parentID, kind, "", text, docxElementAnchor(resource, element))
		if err != nil {
			return err
		}
		builder.reading = append(builder.reading, node.ID)
		textNode = node
		builder.paragraphs++
	}
	for _, footnote := range findDOCXElements(element, "footnoteReference") {
		id := strings.TrimSpace(docxAttr(footnote, "id"))
		if id == "" || textNode.ID == "" {
			return ErrDOCXInvalid
		}
		builder.pending = append(builder.pending, docxPendingFootnote{fromNodeID: textNode.ID, footnoteID: id})
	}
	for _, image := range images {
		relationshipID := strings.TrimSpace(docxAttr(image, "embed"))
		relationship, found := relationships[relationshipID]
		if !found || relationship.External || docxRelationshipKind(relationship.Type) != "image" {
			return ErrDOCXInvalid
		}
		if _, exists := builder.byLocator[relationship.Target]; !exists {
			return ErrDOCXInvalid
		}
		label := docxImageLabel(element)
		if label == "" {
			label = path.Base(relationship.Target)
		}
		node, err := builder.addNode(rootID, DocumentNodeImage, label, "", docxElementAnchor(resource, element))
		if err != nil {
			return err
		}
		builder.reading = append(builder.reading, node.ID)
	}
	return nil
}

func (builder *docxIRBuilder) renderTable(
	parentID string,
	resource DocumentResource,
	element *docxElement,
) error {
	table, err := builder.addNode(parentID, DocumentNodeTable, "", "", docxElementAnchor(resource, element))
	if err != nil {
		return err
	}
	rows := directDOCXChildren(element, "tr")
	if len(rows) == 0 {
		return ErrDOCXUnsupported
	}
	for _, rowElement := range rows {
		row, err := builder.addNode(table.ID, DocumentNodeTableRow, "", "", docxElementAnchor(resource, rowElement))
		if err != nil {
			return err
		}
		cells := directDOCXChildren(rowElement, "tc")
		if len(cells) == 0 {
			return ErrDOCXUnsupported
		}
		for _, cellElement := range cells {
			if len(findDOCXElements(cellElement, "blip")) != 0 {
				return ErrDOCXUnsupported
			}
			text := docxElementText(cellElement)
			if text == "" {
				return ErrDOCXUnsupported
			}
			cell, err := builder.addNode(
				row.ID, DocumentNodeTableCell, "", text, docxElementAnchor(resource, cellElement),
			)
			if err != nil {
				return err
			}
			builder.reading = append(builder.reading, cell.ID)
			for _, footnote := range findDOCXElements(cellElement, "footnoteReference") {
				id := strings.TrimSpace(docxAttr(footnote, "id"))
				if id == "" {
					return ErrDOCXInvalid
				}
				builder.pending = append(builder.pending, docxPendingFootnote{fromNodeID: cell.ID, footnoteID: id})
			}
		}
	}
	builder.tables++
	return nil
}

func (builder *docxIRBuilder) renderFootnotes(parentID string, part *docxPart) error {
	if part == nil {
		if len(builder.pending) != 0 {
			return ErrDOCXInvalid
		}
		return nil
	}
	resource, exists := builder.byLocator[part.locator]
	if !exists {
		return ErrDOCXInvalid
	}
	seen := make(map[string]struct{})
	for _, element := range directDOCXChildren(part.dom, "footnote") {
		id := strings.TrimSpace(docxAttr(element, "id"))
		kind := strings.TrimSpace(docxAttr(element, "type"))
		if id == "" {
			return ErrDOCXInvalid
		}
		if strings.HasPrefix(id, "-") || kind == "separator" || kind == "continuationSeparator" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			return ErrDOCXInvalid
		}
		seen[id] = struct{}{}
		text := docxElementText(element)
		if text == "" {
			return ErrDOCXUnsupported
		}
		node, err := builder.addNode(
			parentID, DocumentNodeFootnote, id, text, docxElementAnchor(resource, element),
		)
		if err != nil {
			return err
		}
		builder.reading = append(builder.reading, node.ID)
		builder.footnotes++
	}
	return nil
}

func (builder *docxIRBuilder) resolveFootnotes() error {
	footnotes := make(map[string]string)
	for _, node := range builder.nodes {
		if node.Kind == DocumentNodeFootnote {
			footnotes[node.Label] = node.ID
		}
	}
	seen := make(map[string]struct{})
	for _, pending := range builder.pending {
		target := footnotes[pending.footnoteID]
		if target == "" {
			return ErrDOCXInvalid
		}
		key := pending.fromNodeID + "\x00" + target
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		builder.references = append(builder.references, DocumentReference{
			FromNodeID: pending.fromNodeID, Kind: DocumentReferenceFootnote,
			TargetNodeID: target, Label: pending.footnoteID,
		})
	}
	return nil
}

func (builder *docxIRBuilder) addNode(
	parentID string,
	kind DocumentNodeKind,
	label string,
	text string,
	anchor DocumentAnchor,
) (DocumentNode, error) {
	ordinal := builder.children[parentID] + 1
	id, err := StableDocumentNodeID(builder.documentID, parentID, kind, ordinal)
	if err != nil {
		return DocumentNode{}, ErrDOCXInvalid
	}
	node := DocumentNode{
		ID: id, ParentID: parentID, Kind: kind, Ordinal: ordinal,
		Label: strings.TrimSpace(label), Text: strings.TrimSpace(text), Anchor: anchor,
	}
	builder.nodes = append(builder.nodes, node)
	builder.children[parentID] = ordinal
	return node, nil
}

func (builder *docxIRBuilder) seal() (DocumentIR, error) {
	document, err := SealDocumentIR(DocumentIR{
		Version: DocumentIRVersion, DocumentID: builder.documentID, Source: builder.source,
		Title: builder.title, Language: builder.language,
		Resources: builder.resources, Nodes: builder.nodes, ReadingOrder: builder.reading,
		References: builder.references,
	})
	if err != nil {
		if errors.Is(err, ErrDocumentIRBudget) {
			return DocumentIR{}, ErrDOCXBudget
		}
		return DocumentIR{}, ErrDOCXInvalid
	}
	return document, nil
}

func parseDOCXDOM(data []byte, config DOCXAdapterConfig) (*docxElement, error) {
	if len(data) == 0 || uint64(len(data)) > config.MaxEntryUncompressedBytes {
		return nil, ErrDOCXBudget
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	var root *docxElement
	stack := make([]*docxElement, 0)
	var tokens uint64
	for {
		startOffset := decoder.InputOffset()
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrDOCXInvalid
		}
		tokens++
		if tokens > config.MaxXMLTokens {
			return nil, ErrDOCXBudget
		}
		switch value := token.(type) {
		case xml.StartElement:
			if uint64(len(stack)+1) > config.MaxXMLDepth {
				return nil, ErrDOCXBudget
			}
			element := &docxElement{
				name: value.Name.Local, attrs: append([]xml.Attr(nil), value.Attr...), start: uint64(startOffset),
			}
			if len(stack) == 0 {
				if root != nil {
					return nil, ErrDOCXInvalid
				}
				root = element
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, element)
				parent.content = append(parent.content, docxContentPart{child: element})
			}
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != value.Name.Local {
				return nil, ErrDOCXInvalid
			}
			stack[len(stack)-1].end = uint64(decoder.InputOffset())
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) != 0 {
				stack[len(stack)-1].content = append(
					stack[len(stack)-1].content, docxContentPart{text: string(value)},
				)
			}
		case xml.Comment:
			continue
		case xml.Directive:
			return nil, ErrDOCXUnsupported
		case xml.ProcInst:
			if strings.ToLower(value.Target) != "xml" {
				return nil, ErrDOCXUnsupported
			}
		}
	}
	if root == nil || len(stack) != 0 || root.end == 0 || root.end > uint64(len(data)) {
		return nil, ErrDOCXInvalid
	}
	return root, nil
}

func parseDOCXHeadingStyles(data []byte, config DOCXAdapterConfig) (map[string]struct{}, error) {
	root, err := parseDOCXDOM(data, config)
	if err != nil {
		return nil, err
	}
	if root.name != "styles" {
		return nil, ErrDOCXInvalid
	}
	styles := make(map[string]struct{})
	for _, style := range directDOCXChildren(root, "style") {
		if docxAttr(style, "type") != "paragraph" {
			continue
		}
		id := strings.TrimSpace(docxAttr(style, "styleId"))
		name := strings.ToLower(strings.TrimSpace(docxAttr(findDOCXChild(style, "name"), "val")))
		outline := strings.TrimSpace(docxAttr(findDOCXElement(style, "outlineLvl"), "val"))
		if !validSourceIntakeID(id, 160) {
			return nil, ErrDOCXInvalid
		}
		if strings.HasPrefix(name, "heading") || (len(outline) == 1 && outline[0] >= '0' && outline[0] <= '8') {
			styles[id] = struct{}{}
		}
	}
	return styles, nil
}

func docxParagraphIsHeading(element *docxElement, styles map[string]struct{}) bool {
	style := strings.TrimSpace(docxAttr(findDOCXElement(element, "pStyle"), "val"))
	if _, found := styles[style]; found {
		return true
	}
	outline := strings.TrimSpace(docxAttr(findDOCXElement(element, "outlineLvl"), "val"))
	return len(outline) == 1 && outline[0] >= '0' && outline[0] <= '8'
}

func docxParagraphListKey(element *docxElement) string {
	numPr := findDOCXElement(element, "numPr")
	if numPr == nil {
		return ""
	}
	numID := strings.TrimSpace(docxAttr(findDOCXChild(numPr, "numId"), "val"))
	level := strings.TrimSpace(docxAttr(findDOCXChild(numPr, "ilvl"), "val"))
	if numID == "" {
		return ""
	}
	return "num:" + numID + ":level:" + level
}

func docxElementText(element *docxElement) string {
	if element == nil {
		return ""
	}
	var builder strings.Builder
	var render func(*docxElement)
	render = func(current *docxElement) {
		switch current.name {
		case "tab":
			builder.WriteByte(' ')
			return
		case "br", "cr":
			builder.WriteByte('\n')
			return
		case "delText", "instrText":
			return
		}
		for _, part := range current.content {
			if part.child != nil {
				render(part.child)
			} else if current.name == "t" {
				builder.WriteString(part.text)
			}
		}
	}
	render(element)
	return strings.Join(strings.Fields(builder.String()), " ")
}

func docxElementAnchor(resource DocumentResource, element *docxElement) DocumentAnchor {
	return DocumentAnchor{ResourceID: resource.ID, Start: element.start, End: element.end}
}

func docxAttr(element *docxElement, localName string) string {
	if element == nil {
		return ""
	}
	for _, attribute := range element.attrs {
		if attribute.Name.Local == localName {
			return attribute.Value
		}
	}
	return ""
}

func findDOCXChild(element *docxElement, name string) *docxElement {
	if element == nil {
		return nil
	}
	for _, child := range element.children {
		if child.name == name {
			return child
		}
	}
	return nil
}

func directDOCXChildren(element *docxElement, name string) []*docxElement {
	if element == nil {
		return nil
	}
	result := make([]*docxElement, 0)
	for _, child := range element.children {
		if child.name == name {
			result = append(result, child)
		}
	}
	return result
}

func findDOCXElement(element *docxElement, name string) *docxElement {
	if element == nil {
		return nil
	}
	if element.name == name {
		return element
	}
	for _, child := range element.children {
		if found := findDOCXElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func findDOCXElements(element *docxElement, name string) []*docxElement {
	result := make([]*docxElement, 0)
	var visit func(*docxElement)
	visit = func(current *docxElement) {
		if current == nil {
			return
		}
		if current.name == name {
			result = append(result, current)
		}
		for _, child := range current.children {
			visit(child)
		}
	}
	visit(element)
	return result
}

func docxImageLabel(paragraph *docxElement) string {
	properties := findDOCXElement(paragraph, "docPr")
	for _, attribute := range []string{"descr", "title", "name"} {
		if value := strings.TrimSpace(docxAttr(properties, attribute)); value != "" {
			return value
		}
	}
	return ""
}
