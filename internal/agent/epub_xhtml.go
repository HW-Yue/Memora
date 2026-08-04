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

type epubElement struct {
	name     string
	attrs    []xml.Attr
	start    uint64
	end      uint64
	content  []epubContentPart
	children []*epubElement
}

type epubContentPart struct {
	text  string
	child *epubElement
}

type epubPendingReference struct {
	fromNodeID string
	locator    string
	href       string
	label      string
	noteref    bool
}

type epubIRBuilder struct {
	documentID string
	source     SourceReference
	title      string
	language   string
	resources  []DocumentResource
	nodes      []DocumentNode
	reading    []string
	references []DocumentReference
	children   map[string]uint64
	targets    map[string]string
	pending    []epubPendingReference
	footnotes  int
}

func newEPUBIRBuilder(
	documentID string,
	source SourceReference,
	title string,
	language string,
	resources []DocumentResource,
) *epubIRBuilder {
	return &epubIRBuilder{
		documentID: documentID, source: source, title: title, language: language,
		resources: append([]DocumentResource(nil), resources...),
		children:  make(map[string]uint64), targets: make(map[string]string),
	}
}

func (builder *epubIRBuilder) build(
	ctx context.Context,
	archive *epubArchive,
	spine []epubManifestItem,
	labels map[string]string,
	resourceByLocator map[string]DocumentResource,
	config EPUBAdapterConfig,
) error {
	if len(spine) == 0 {
		return ErrEPUBInvalid
	}
	firstResource, exists := resourceByLocator[spine[0].Locator]
	if !exists {
		return ErrEPUBInvalid
	}
	root, err := builder.addNode("", DocumentNodeDocument, builder.title, "", DocumentAnchor{
		ResourceID: firstResource.ID, Start: 0, End: firstResource.Bytes,
	})
	if err != nil {
		return err
	}
	for _, item := range spine {
		if err := ctx.Err(); err != nil {
			return err
		}
		resource, exists := resourceByLocator[item.Locator]
		if !exists {
			return ErrEPUBInvalid
		}
		data, err := archive.read(ctx, item.Locator)
		if err != nil {
			return err
		}
		dom, err := parseEPUBDOM(data, config)
		if err != nil {
			return err
		}
		body := findEPUBElement(dom, "body")
		if body == nil {
			return ErrEPUBInvalid
		}
		label := strings.TrimSpace(labels[item.Locator])
		if label == "" {
			label = firstEPUBHeading(body)
		}
		if label == "" {
			label = item.ID
		}
		chapter, err := builder.addNode(root.ID, DocumentNodeChapter, label, "", DocumentAnchor{
			ResourceID: resource.ID, Start: 0, End: uint64(len(data)),
		})
		if err != nil {
			return err
		}
		before := len(builder.nodes)
		for _, child := range body.children {
			if err := builder.renderElement(ctx, chapter.ID, item.Locator, resource, child); err != nil {
				return err
			}
		}
		if len(builder.nodes) == before {
			return ErrEPUBInvalid
		}
	}
	return builder.resolveReferences()
}

func (builder *epubIRBuilder) seal() (DocumentIR, error) {
	document, err := SealDocumentIR(DocumentIR{
		Version: DocumentIRVersion, DocumentID: builder.documentID, Source: builder.source,
		Title: builder.title, Language: builder.language,
		Resources: builder.resources, Nodes: builder.nodes, ReadingOrder: builder.reading,
		References: builder.references,
	})
	if err != nil {
		if errors.Is(err, ErrDocumentIRBudget) {
			return DocumentIR{}, ErrEPUBBudget
		}
		return DocumentIR{}, ErrEPUBInvalid
	}
	return document, nil
}

func (builder *epubIRBuilder) renderElement(
	ctx context.Context,
	parentID string,
	locator string,
	resource DocumentResource,
	element *epubElement,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := strings.ToLower(element.name)
	switch name {
	case "nav", "script", "style", "head", "title", "meta", "link":
		return nil
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return builder.addLeaf(locator, parentID, DocumentNodeHeading, "", epubElementText(element), resource, element)
	case "p":
		return builder.addLeaf(locator, parentID, DocumentNodeParagraph, "", epubElementText(element), resource, element)
	case "ul", "ol":
		return builder.renderList(ctx, parentID, locator, resource, element)
	case "table":
		return builder.renderTable(ctx, parentID, locator, resource, element)
	case "aside":
		if epubElementHasType(element, "footnote") || epubElementHasType(element, "endnote") {
			builder.footnotes++
			return builder.addLeaf(
				locator, parentID, DocumentNodeFootnote, epubElementAttr(element, "id"),
				epubElementText(element), resource, element,
			)
		}
		return builder.renderContainer(ctx, parentID, locator, resource, element)
	case "section", "article", "div", "main":
		return builder.renderContainer(ctx, parentID, locator, resource, element)
	case "blockquote":
		return builder.addLeaf(locator, parentID, DocumentNodeQuote, "", epubElementText(element), resource, element)
	case "pre":
		return builder.addLeaf(locator, parentID, DocumentNodeCode, "", epubElementText(element), resource, element)
	case "img":
		label := epubElementAttr(element, "alt")
		if strings.TrimSpace(label) == "" {
			label = epubElementAttr(element, "title")
		}
		if strings.TrimSpace(label) == "" {
			return nil
		}
		node, err := builder.addNode(parentID, DocumentNodeImage, label, "", epubAnchor(resource, element))
		if err != nil {
			return err
		}
		builder.reading = append(builder.reading, node.ID)
		return builder.registerElementTarget(locator, element, node.ID)
	default:
		if epubHasBlockChildren(element) {
			for _, child := range element.children {
				if err := builder.renderElement(ctx, parentID, locator, resource, child); err != nil {
					return err
				}
			}
			return nil
		}
		text := epubElementText(element)
		if text == "" {
			return nil
		}
		return builder.addLeaf(locator, parentID, DocumentNodeParagraph, "", text, resource, element)
	}
}

func (builder *epubIRBuilder) renderContainer(
	ctx context.Context,
	parentID string,
	locator string,
	resource DocumentResource,
	element *epubElement,
) error {
	if !epubHasRenderableChildren(element) {
		text := epubElementText(element)
		if text == "" {
			return nil
		}
		return builder.addLeaf(locator, parentID, DocumentNodeParagraph, "", text, resource, element)
	}
	container, err := builder.addNode(
		parentID, DocumentNodeSection, epubElementAttr(element, "id"), "", epubAnchor(resource, element),
	)
	if err != nil {
		return err
	}
	if err := builder.registerElementTarget(locator, element, container.ID); err != nil {
		return err
	}
	before := len(builder.nodes)
	for _, child := range element.children {
		if err := builder.renderElement(ctx, container.ID, locator, resource, child); err != nil {
			return err
		}
	}
	if len(builder.nodes) == before {
		return ErrEPUBInvalid
	}
	return nil
}

func (builder *epubIRBuilder) renderList(
	ctx context.Context,
	parentID string,
	locator string,
	resource DocumentResource,
	element *epubElement,
) error {
	var items []*epubElement
	for _, child := range element.children {
		if strings.EqualFold(child.name, "li") {
			items = append(items, child)
		}
	}
	if len(items) == 0 {
		return ErrEPUBInvalid
	}
	list, err := builder.addNode(parentID, DocumentNodeList, "", "", epubAnchor(resource, element))
	if err != nil {
		return err
	}
	for _, item := range items {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := builder.addLeaf(
			locator, list.ID, DocumentNodeListItem, "", epubElementText(item), resource, item,
		); err != nil {
			return err
		}
	}
	return nil
}

func (builder *epubIRBuilder) renderTable(
	ctx context.Context,
	parentID string,
	locator string,
	resource DocumentResource,
	element *epubElement,
) error {
	rows := epubTableRows(element)
	if len(rows) == 0 {
		return ErrEPUBInvalid
	}
	table, err := builder.addNode(
		parentID, DocumentNodeTable, epubElementAttr(element, "summary"), "", epubAnchor(resource, element),
	)
	if err != nil {
		return err
	}
	for _, rowElement := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		cells := epubTableCells(rowElement)
		if len(cells) == 0 {
			return ErrEPUBInvalid
		}
		row, err := builder.addNode(table.ID, DocumentNodeTableRow, "", "", epubAnchor(resource, rowElement))
		if err != nil {
			return err
		}
		for _, cell := range cells {
			if err := builder.addLeaf(
				locator, row.ID, DocumentNodeTableCell, "", epubElementText(cell), resource, cell,
			); err != nil {
				return err
			}
		}
	}
	return nil
}

func (builder *epubIRBuilder) addLeaf(
	locator string,
	parentID string,
	kind DocumentNodeKind,
	label string,
	text string,
	resource DocumentResource,
	element *epubElement,
) error {
	text = normalizeEPUBText(text)
	if text == "" {
		return nil
	}
	node, err := builder.addNode(parentID, kind, label, text, epubAnchor(resource, element))
	if err != nil {
		return err
	}
	builder.reading = append(builder.reading, node.ID)
	if err := builder.registerElementTarget(locator, element, node.ID); err != nil {
		return err
	}
	for _, link := range epubElementLinks(element) {
		builder.pending = append(builder.pending, epubPendingReference{
			fromNodeID: node.ID, locator: locator, href: link.href,
			label: link.label, noteref: link.noteref,
		})
	}
	return nil
}

func (builder *epubIRBuilder) addNode(
	parentID string,
	kind DocumentNodeKind,
	label string,
	text string,
	anchor DocumentAnchor,
) (DocumentNode, error) {
	ordinal := builder.children[parentID] + 1
	id, err := StableDocumentNodeID(builder.documentID, parentID, kind, ordinal)
	if err != nil {
		return DocumentNode{}, ErrEPUBInvalid
	}
	node := DocumentNode{
		ID: id, ParentID: parentID, Kind: kind, Ordinal: ordinal,
		Label: normalizeEPUBText(label), Text: text, Anchor: anchor,
	}
	builder.nodes = append(builder.nodes, node)
	builder.children[parentID] = ordinal
	return node, nil
}

func (builder *epubIRBuilder) registerElementTarget(locator string, element *epubElement, nodeID string) error {
	id := epubElementAttr(element, "id")
	if id == "" {
		return nil
	}
	key := locator + "#" + id
	if _, duplicate := builder.targets[key]; duplicate {
		return ErrEPUBInvalid
	}
	builder.targets[key] = nodeID
	return nil
}

func (builder *epubIRBuilder) resolveReferences() error {
	nodeByID := make(map[string]DocumentNode, len(builder.nodes))
	for _, node := range builder.nodes {
		nodeByID[node.ID] = node
	}
	seen := make(map[string]struct{})
	for _, pending := range builder.pending {
		targetKey, internal, err := resolveEPUBLink(pending.locator, pending.href)
		if err != nil {
			return err
		}
		if !internal {
			if pending.noteref {
				return ErrEPUBInvalid
			}
			continue
		}
		targetID, exists := builder.targets[targetKey]
		if !exists {
			return ErrEPUBInvalid
		}
		target := nodeByID[targetID]
		kind := DocumentReferenceInternal
		if pending.noteref || target.Kind == DocumentNodeFootnote {
			if target.Kind != DocumentNodeFootnote {
				return ErrEPUBInvalid
			}
			kind = DocumentReferenceFootnote
		}
		key := pending.fromNodeID + "\x00" + string(kind) + "\x00" + targetID
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		builder.references = append(builder.references, DocumentReference{
			FromNodeID: pending.fromNodeID, Kind: kind, TargetNodeID: targetID,
			Label: normalizeEPUBText(pending.label),
		})
	}
	return nil
}

func parseEPUBDOM(data []byte, config EPUBAdapterConfig) (*epubElement, error) {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	root := &epubElement{name: "__root", start: 0, end: uint64(len(data))}
	stack := []*epubElement{root}
	var tokens uint64
	for {
		before := decoder.InputOffset()
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, ErrEPUBInvalid
		}
		tokens++
		if tokens > config.MaxXMLTokens {
			return nil, ErrEPUBBudget
		}
		switch value := token.(type) {
		case xml.StartElement:
			if uint64(len(stack)) > config.MaxXMLDepth {
				return nil, ErrEPUBBudget
			}
			element := &epubElement{
				name: value.Name.Local, attrs: append([]xml.Attr(nil), value.Attr...), start: uint64(before),
			}
			parent := stack[len(stack)-1]
			parent.children = append(parent.children, element)
			parent.content = append(parent.content, epubContentPart{child: element})
			stack = append(stack, element)
		case xml.EndElement:
			if len(stack) <= 1 {
				return nil, ErrEPUBInvalid
			}
			element := stack[len(stack)-1]
			element.end = uint64(decoder.InputOffset())
			stack = stack[:len(stack)-1]
		case xml.CharData:
			stack[len(stack)-1].content = append(
				stack[len(stack)-1].content, epubContentPart{text: string(value)},
			)
		case xml.Directive:
			return nil, ErrEPUBUnsupported
		case xml.ProcInst:
			if strings.ToLower(value.Target) != "xml" {
				return nil, ErrEPUBUnsupported
			}
		}
	}
	if len(stack) != 1 || len(root.children) != 1 || !strings.EqualFold(root.children[0].name, "html") {
		return nil, ErrEPUBInvalid
	}
	return root.children[0], nil
}

func parseEPUBNavigation(data []byte, locator string, config EPUBAdapterConfig) (map[string]string, error) {
	dom, err := parseEPUBDOM(data, config)
	if err != nil {
		return nil, err
	}
	var navigation *epubElement
	visitEPUBElements(dom, func(element *epubElement) {
		if navigation == nil && strings.EqualFold(element.name, "nav") &&
			(epubElementHasType(element, "toc") || epubElementAttr(element, "type") == "") {
			navigation = element
		}
	})
	if navigation == nil {
		return nil, ErrEPUBInvalid
	}
	labels := make(map[string]string)
	var parseErr error
	visitEPUBElements(navigation, func(element *epubElement) {
		if parseErr != nil || !strings.EqualFold(element.name, "a") {
			return
		}
		href := epubElementAttr(element, "href")
		resolved, resolveErr := resolveEPUBHref(path.Dir(locator), href)
		label := epubElementText(element)
		if resolveErr != nil || label == "" {
			parseErr = ErrEPUBInvalid
			return
		}
		if previous, duplicate := labels[resolved]; duplicate && previous != label {
			parseErr = ErrEPUBInvalid
			return
		}
		labels[resolved] = label
	})
	if parseErr != nil || len(labels) == 0 {
		return nil, ErrEPUBInvalid
	}
	return labels, nil
}

type epubNCXDocument struct {
	NavPoints []epubNCXNavPoint `xml:"navMap>navPoint"`
}

type epubNCXNavPoint struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Source string `xml:"src,attr"`
	} `xml:"content"`
	Children []epubNCXNavPoint `xml:"navPoint"`
}

func parseEPUBNCX(data []byte, locator string, config EPUBAdapterConfig) (map[string]string, error) {
	var document epubNCXDocument
	if err := decodeEPUBXML(data, config, &document); err != nil {
		return nil, err
	}
	labels := make(map[string]string)
	var walk func([]epubNCXNavPoint) error
	walk = func(points []epubNCXNavPoint) error {
		for _, point := range points {
			resolved, err := resolveEPUBHref(path.Dir(locator), point.Content.Source)
			if err != nil || normalizeEPUBText(point.Label.Text) == "" {
				return ErrEPUBInvalid
			}
			labels[resolved] = normalizeEPUBText(point.Label.Text)
			if err := walk(point.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(document.NavPoints); err != nil || len(labels) == 0 {
		return nil, ErrEPUBInvalid
	}
	return labels, nil
}

func resolveEPUBLink(locator, href string) (string, bool, error) {
	href = strings.TrimSpace(href)
	if href == "" {
		return "", false, nil
	}
	if strings.Contains(href, "://") || strings.HasPrefix(href, "mailto:") {
		return "", false, nil
	}
	parts := strings.SplitN(href, "#", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", false, nil
	}
	targetLocator := locator
	if parts[0] != "" {
		resolved, err := resolveEPUBHref(path.Dir(locator), parts[0])
		if err != nil {
			return "", false, err
		}
		targetLocator = resolved
	}
	if strings.ContainsAny(parts[1], "\r\n\t") || len(parts[1]) > 512 {
		return "", false, ErrEPUBInvalid
	}
	return targetLocator + "#" + parts[1], true, nil
}

type epubLink struct {
	href    string
	label   string
	noteref bool
}

func epubElementLinks(element *epubElement) []epubLink {
	var links []epubLink
	visitEPUBElements(element, func(candidate *epubElement) {
		if strings.EqualFold(candidate.name, "a") && epubElementAttr(candidate, "href") != "" {
			links = append(links, epubLink{
				href: epubElementAttr(candidate, "href"), label: epubElementText(candidate),
				noteref: epubElementHasType(candidate, "noteref"),
			})
		}
	})
	return links
}

func epubElementText(element *epubElement) string {
	var pieces []string
	var collect func(*epubElement)
	collect = func(current *epubElement) {
		for _, part := range current.content {
			if part.child == nil {
				pieces = append(pieces, part.text)
				continue
			}
			if strings.EqualFold(part.child.name, "script") || strings.EqualFold(part.child.name, "style") {
				continue
			}
			collect(part.child)
		}
	}
	collect(element)
	return normalizeEPUBText(strings.Join(pieces, " "))
}

func normalizeEPUBText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func epubElementAttr(element *epubElement, local string) string {
	for _, attribute := range element.attrs {
		if strings.EqualFold(attribute.Name.Local, local) {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}

func epubElementHasType(element *epubElement, target string) bool {
	for _, value := range strings.Fields(epubElementAttr(element, "type")) {
		if value == target || strings.HasSuffix(value, ":"+target) {
			return true
		}
	}
	return false
}

func epubAnchor(resource DocumentResource, element *epubElement) DocumentAnchor {
	selector := ""
	if id := epubElementAttr(element, "id"); id != "" {
		selector = "#" + id
	}
	return DocumentAnchor{
		ResourceID: resource.ID, Start: element.start, End: element.end, Selector: selector,
	}
}

func findEPUBElement(root *epubElement, name string) *epubElement {
	if strings.EqualFold(root.name, name) {
		return root
	}
	for _, child := range root.children {
		if found := findEPUBElement(child, name); found != nil {
			return found
		}
	}
	return nil
}

func visitEPUBElements(root *epubElement, visit func(*epubElement)) {
	visit(root)
	for _, child := range root.children {
		visitEPUBElements(child, visit)
	}
}

func firstEPUBHeading(root *epubElement) string {
	var result string
	visitEPUBElements(root, func(element *epubElement) {
		if result != "" {
			return
		}
		name := strings.ToLower(element.name)
		if len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6' {
			result = epubElementText(element)
		}
	})
	return result
}

func epubHasRenderableChildren(element *epubElement) bool {
	for _, child := range element.children {
		name := strings.ToLower(child.name)
		switch name {
		case "h1", "h2", "h3", "h4", "h5", "h6", "p", "ul", "ol", "table", "aside",
			"section", "article", "div", "main", "blockquote", "pre", "img":
			return true
		}
	}
	return false
}

func epubHasBlockChildren(element *epubElement) bool {
	return epubHasRenderableChildren(element)
}

func epubTableRows(table *epubElement) []*epubElement {
	var rows []*epubElement
	for _, child := range table.children {
		name := strings.ToLower(child.name)
		if name == "tr" {
			rows = append(rows, child)
			continue
		}
		if name == "thead" || name == "tbody" || name == "tfoot" {
			for _, row := range child.children {
				if strings.EqualFold(row.name, "tr") {
					rows = append(rows, row)
				}
			}
		}
	}
	return rows
}

func epubTableCells(row *epubElement) []*epubElement {
	var cells []*epubElement
	for _, child := range row.children {
		if strings.EqualFold(child.name, "td") || strings.EqualFold(child.name, "th") {
			cells = append(cells, child)
		}
	}
	return cells
}
