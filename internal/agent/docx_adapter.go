package agent

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
)

const (
	DOCXParseReceiptVersion = "memora.docx-parse-receipt/v1"
	docxDocumentMediaType   = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	docxMainContentType     = "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
)

var (
	ErrDOCXInvalid     = errors.New("DOCX is invalid")
	ErrDOCXUnsupported = errors.New("DOCX feature is unsupported")
	ErrDOCXBudget      = errors.New("DOCX exceeds parsing budget")
)

type DOCXAdapterConfig struct {
	MaxEntries                uint64
	MaxEntryUncompressedBytes uint64
	MaxTotalUncompressedBytes uint64
	MaxXMLTokens              uint64
	MaxXMLDepth               uint64
}

func DefaultDOCXAdapterConfig() DOCXAdapterConfig {
	return DOCXAdapterConfig{
		MaxEntries: 10_000, MaxEntryUncompressedBytes: 64 << 20,
		MaxTotalUncompressedBytes: 512 << 20, MaxXMLTokens: 2_000_000, MaxXMLDepth: 128,
	}
}

type DOCXSource interface {
	OpenRandomAccess(jobID, sourceID string) (SourceReference, SourceRandomAccess, error)
}

type DOCXParseReceipt struct {
	Version      string `json:"version"`
	SourceSHA256 string `json:"source_sha256"`
	MainPart     string `json:"main_part"`
	Resources    uint64 `json:"resources"`
	Nodes        uint64 `json:"nodes"`
	Paragraphs   uint64 `json:"paragraphs"`
	Tables       uint64 `json:"tables"`
	Footnotes    uint64 `json:"footnotes"`
	IRSHA256     string `json:"ir_sha256"`
}

func (receipt DOCXParseReceipt) Validate() error {
	if receipt.Version != DOCXParseReceiptVersion || !validSourceSHA256(receipt.SourceSHA256) ||
		!validDocumentLocator(receipt.MainPart) || receipt.Resources == 0 || receipt.Nodes < 2 ||
		!validSourceSHA256(receipt.IRSHA256) {
		return ErrDOCXInvalid
	}
	return nil
}

type DOCXAdapter struct {
	source DOCXSource
	config DOCXAdapterConfig
}

func NewDOCXAdapter(source DOCXSource, config DOCXAdapterConfig) (*DOCXAdapter, error) {
	if source == nil || !validDOCXAdapterConfig(config) {
		return nil, ErrDOCXInvalid
	}
	return &DOCXAdapter{source: source, config: config}, nil
}

type docxArchive struct {
	config DOCXAdapterConfig
	files  map[string]*zip.File
	cache  map[string][]byte
	types  map[string]string
}

type docxRelationship struct {
	ID       string
	Type     string
	Target   string
	External bool
}

func (adapter *DOCXAdapter) Parse(
	ctx context.Context,
	jobID string,
	sourceID string,
) (document DocumentIR, receipt DOCXParseReceipt, resultErr error) {
	if adapter == nil || ctx == nil || !validSourceIntakeID(jobID, 128) ||
		!validSourceIntakeID(sourceID, 128) {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXInvalid
	}
	if err := ctx.Err(); err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	reference, reader, err := adapter.source.OpenRandomAccess(jobID, sourceID)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	defer func() {
		if closeErr := reader.Close(); resultErr == nil && closeErr != nil {
			document, receipt = DocumentIR{}, DOCXParseReceipt{}
			resultErr = fmt.Errorf("close DOCX Source: %w", closeErr)
		}
	}()
	if reference.MediaType != docxDocumentMediaType || reference.Bytes > uint64(^uint64(0)>>1) {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXUnsupported
	}
	zipReader, err := zip.NewReader(reader, int64(reference.Bytes))
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, fmt.Errorf("%w: ZIP: %v", ErrDOCXInvalid, err)
	}
	archive, err := newDOCXArchive(ctx, zipReader, adapter.config)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	rootRelationships, err := archive.relationships(ctx, "_rels/.rels", "")
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	mainPart, corePart, err := docxRootParts(rootRelationships)
	if err != nil || archive.types[mainPart] != docxMainContentType {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXInvalid
	}
	mainBytes, err := archive.read(ctx, mainPart)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	mainDOM, err := parseDOCXDOM(mainBytes, adapter.config)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	if mainDOM.name != "document" || findDOCXChild(mainDOM, "body") == nil {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXInvalid
	}
	if findDOCXElement(mainDOM, "altChunk") != nil {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXUnsupported
	}
	documentRelationships, err := archive.documentRelationships(ctx, mainPart)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	if docxRelationshipsContainActiveContent(documentRelationships) {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXUnsupported
	}
	styles, footnotes, err := archive.supportingParts(ctx, documentRelationships)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	title, language, err := archive.metadata(ctx, corePart, reference.FileName)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	resources, resourceByLocator, err := archive.documentResources(ctx, reference.SHA256)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	documentID, err := StableDocumentID(reference.SHA256)
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXInvalid
	}
	builder := newDOCXIRBuilder(documentID, reference, title, language, resources, resourceByLocator)
	if err := builder.build(ctx, mainPart, mainBytes, mainDOM, styles, footnotes, documentRelationships); err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	document, err = builder.seal()
	if err != nil {
		return DocumentIR{}, DOCXParseReceipt{}, err
	}
	receipt = DOCXParseReceipt{
		Version: DOCXParseReceiptVersion, SourceSHA256: reference.SHA256, MainPart: mainPart,
		Resources: uint64(len(resources)), Nodes: uint64(len(document.Nodes)),
		Paragraphs: uint64(builder.paragraphs), Tables: uint64(builder.tables),
		Footnotes: uint64(builder.footnotes), IRSHA256: document.SHA256,
	}
	if receipt.Validate() != nil {
		return DocumentIR{}, DOCXParseReceipt{}, ErrDOCXInvalid
	}
	return document, receipt, nil
}

func newDOCXArchive(ctx context.Context, reader *zip.Reader, config DOCXAdapterConfig) (*docxArchive, error) {
	if uint64(len(reader.File)) > config.MaxEntries || len(reader.File) == 0 {
		return nil, ErrDOCXBudget
	}
	archive := &docxArchive{config: config, files: make(map[string]*zip.File), cache: make(map[string][]byte)}
	var total uint64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.Flags&0x1 != 0 {
			return nil, ErrDOCXUnsupported
		}
		name := strings.TrimSuffix(file.Name, "/")
		if name == "" || !validDocumentLocator(name) {
			return nil, ErrDOCXInvalid
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if _, duplicate := archive.files[name]; duplicate {
			return nil, ErrDOCXInvalid
		}
		size := file.UncompressedSize64
		if size > config.MaxEntryUncompressedBytes || total > config.MaxTotalUncompressedBytes-size {
			return nil, ErrDOCXBudget
		}
		total += size
		archive.files[name] = file
	}
	if archive.files["[Content_Types].xml"] == nil || archive.files["_rels/.rels"] == nil {
		return nil, ErrDOCXInvalid
	}
	types, err := archive.contentTypes(ctx)
	if err != nil {
		return nil, err
	}
	archive.types = types
	return archive, nil
}

func (archive *docxArchive) read(ctx context.Context, locator string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := archive.cache[locator]; ok {
		return append([]byte(nil), cached...), nil
	}
	file := archive.files[locator]
	if file == nil {
		return nil, ErrDOCXInvalid
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open %s", ErrDOCXInvalid, locator)
	}
	limited := io.LimitReader(&sourceContextReader{ctx: ctx, reader: reader}, int64(archive.config.MaxEntryUncompressedBytes)+1)
	data, readErr := io.ReadAll(limited)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil || uint64(len(data)) != file.UncompressedSize64 {
		return nil, fmt.Errorf("%w: read %s", ErrDOCXInvalid, locator)
	}
	if uint64(len(data)) > archive.config.MaxEntryUncompressedBytes {
		return nil, ErrDOCXBudget
	}
	archive.cache[locator] = append([]byte(nil), data...)
	return data, nil
}

func (archive *docxArchive) contentTypes(ctx context.Context) (map[string]string, error) {
	encoded, err := archive.read(ctx, "[Content_Types].xml")
	if err != nil {
		return nil, err
	}
	root, err := parseDOCXDOM(encoded, archive.config)
	if err != nil {
		return nil, err
	}
	if root.name != "Types" {
		return nil, ErrDOCXInvalid
	}
	defaults, overrides := make(map[string]string), make(map[string]string)
	for _, child := range root.children {
		switch child.name {
		case "Default":
			extension := strings.ToLower(strings.TrimSpace(docxAttr(child, "Extension")))
			media := strings.TrimSpace(docxAttr(child, "ContentType"))
			if extension == "" || strings.ContainsAny(extension, "/\\.") || !validSourceMediaType(media) {
				return nil, ErrDOCXInvalid
			}
			if _, duplicate := defaults[extension]; duplicate {
				return nil, ErrDOCXInvalid
			}
			defaults[extension] = media
		case "Override":
			part := strings.TrimPrefix(strings.TrimSpace(docxAttr(child, "PartName")), "/")
			media := strings.TrimSpace(docxAttr(child, "ContentType"))
			if !strings.HasPrefix(strings.TrimSpace(docxAttr(child, "PartName")), "/") ||
				!validDocumentLocator(part) || !validSourceMediaType(media) {
				return nil, ErrDOCXInvalid
			}
			if _, duplicate := overrides[part]; duplicate {
				return nil, ErrDOCXInvalid
			}
			overrides[part] = media
		default:
			return nil, ErrDOCXUnsupported
		}
	}
	result := make(map[string]string, len(archive.files))
	for locator := range archive.files {
		media := overrides[locator]
		if media == "" {
			extension := strings.ToLower(strings.TrimPrefix(path.Ext(locator), "."))
			media = defaults[extension]
		}
		if media == "" {
			return nil, ErrDOCXInvalid
		}
		result[locator] = media
	}
	return result, nil
}

func (archive *docxArchive) relationships(
	ctx context.Context,
	relationshipPart string,
	ownerPart string,
) ([]docxRelationship, error) {
	encoded, err := archive.read(ctx, relationshipPart)
	if err != nil {
		return nil, err
	}
	root, err := parseDOCXDOM(encoded, archive.config)
	if err != nil {
		return nil, err
	}
	if root.name != "Relationships" {
		return nil, ErrDOCXInvalid
	}
	result := make([]docxRelationship, 0, len(root.children))
	ids := make(map[string]struct{}, len(root.children))
	for _, child := range root.children {
		if child.name != "Relationship" {
			return nil, ErrDOCXUnsupported
		}
		id, relationshipType := strings.TrimSpace(docxAttr(child, "Id")), strings.TrimSpace(docxAttr(child, "Type"))
		target, mode := strings.TrimSpace(docxAttr(child, "Target")), strings.TrimSpace(docxAttr(child, "TargetMode"))
		if !validSourceIntakeID(id, 160) || !validDocumentText(relationshipType, 1000, true) || target == "" {
			return nil, ErrDOCXInvalid
		}
		if _, duplicate := ids[id]; duplicate {
			return nil, ErrDOCXInvalid
		}
		ids[id] = struct{}{}
		relationship := docxRelationship{ID: id, Type: relationshipType, External: mode == "External"}
		if mode != "" && mode != "Internal" && mode != "External" {
			return nil, ErrDOCXInvalid
		}
		if relationship.External {
			if !validDocumentText(target, 2048, true) {
				return nil, ErrDOCXInvalid
			}
			relationship.Target = target
		} else {
			resolved, err := resolveDOCXTarget(ownerPart, target)
			if err != nil || archive.files[resolved] == nil {
				return nil, ErrDOCXInvalid
			}
			relationship.Target = resolved
		}
		result = append(result, relationship)
	}
	return result, nil
}

func (archive *docxArchive) documentRelationships(ctx context.Context, mainPart string) (map[string]docxRelationship, error) {
	relationshipPart := path.Join(path.Dir(mainPart), "_rels", path.Base(mainPart)+".rels")
	if archive.files[relationshipPart] == nil {
		return map[string]docxRelationship{}, nil
	}
	values, err := archive.relationships(ctx, relationshipPart, mainPart)
	if err != nil {
		return nil, err
	}
	result := make(map[string]docxRelationship, len(values))
	for _, value := range values {
		result[value.ID] = value
	}
	return result, nil
}

func (archive *docxArchive) supportingParts(
	ctx context.Context,
	relationships map[string]docxRelationship,
) (styles map[string]struct{}, footnotes *docxPart, err error) {
	styles = make(map[string]struct{})
	for _, relationship := range relationships {
		if relationship.External {
			continue
		}
		switch docxRelationshipKind(relationship.Type) {
		case "styles":
			encoded, readErr := archive.read(ctx, relationship.Target)
			if readErr != nil {
				return nil, nil, readErr
			}
			styles, err = parseDOCXHeadingStyles(encoded, archive.config)
			if err != nil {
				return nil, nil, err
			}
		case "footnotes":
			encoded, readErr := archive.read(ctx, relationship.Target)
			if readErr != nil {
				return nil, nil, readErr
			}
			dom, parseErr := parseDOCXDOM(encoded, archive.config)
			if parseErr != nil {
				return nil, nil, parseErr
			}
			if dom.name != "footnotes" {
				return nil, nil, ErrDOCXInvalid
			}
			footnotes = &docxPart{locator: relationship.Target, data: encoded, dom: dom}
		}
	}
	return styles, footnotes, nil
}

func (archive *docxArchive) metadata(ctx context.Context, corePart, fileName string) (string, string, error) {
	title := strings.TrimSuffix(fileName, path.Ext(fileName))
	language := "und"
	if corePart == "" {
		return title, language, nil
	}
	encoded, err := archive.read(ctx, corePart)
	if err != nil {
		return "", "", err
	}
	dom, err := parseDOCXDOM(encoded, archive.config)
	if err != nil {
		return "", "", err
	}
	if dom.name != "coreProperties" {
		return "", "", ErrDOCXInvalid
	}
	if value := docxElementText(findDOCXElement(dom, "title")); value != "" {
		title = value
	}
	if value := docxElementText(findDOCXElement(dom, "language")); value != "" {
		language = value
	}
	if !validDocumentText(title, 1000, true) || !validSourceIntakeID(language, 35) {
		return "", "", ErrDOCXInvalid
	}
	return title, language, nil
}

func (archive *docxArchive) documentResources(
	ctx context.Context,
	sourceSHA256 string,
) ([]DocumentResource, map[string]DocumentResource, error) {
	locators := make([]string, 0, len(archive.files))
	for locator := range archive.files {
		locators = append(locators, locator)
	}
	sort.Strings(locators)
	resources := make([]DocumentResource, 0, len(locators))
	byLocator := make(map[string]DocumentResource, len(locators))
	for _, locator := range locators {
		data, err := archive.read(ctx, locator)
		if err != nil || len(data) == 0 {
			return nil, nil, ErrDOCXInvalid
		}
		id, err := StableDocumentResourceID(sourceSHA256, locator)
		if err != nil {
			return nil, nil, ErrDOCXInvalid
		}
		digest := sha256.Sum256(data)
		resource := DocumentResource{
			ID: id, Locator: locator, MediaType: archive.types[locator], Bytes: uint64(len(data)),
			SHA256: "sha256:" + hex.EncodeToString(digest[:]),
		}
		resources = append(resources, resource)
		byLocator[locator] = resource
	}
	return resources, byLocator, nil
}

func docxRootParts(relationships []docxRelationship) (mainPart, corePart string, err error) {
	for _, relationship := range relationships {
		if relationship.External {
			continue
		}
		switch docxRelationshipKind(relationship.Type) {
		case "officeDocument":
			if mainPart != "" {
				return "", "", ErrDOCXInvalid
			}
			mainPart = relationship.Target
		case "core-properties":
			if corePart != "" {
				return "", "", ErrDOCXInvalid
			}
			corePart = relationship.Target
		}
	}
	if mainPart == "" {
		return "", "", ErrDOCXInvalid
	}
	return mainPart, corePart, nil
}

func resolveDOCXTarget(ownerPart, target string) (string, error) {
	target = strings.TrimSpace(target)
	if target == "" || strings.HasPrefix(target, "/") || strings.Contains(target, "\\") ||
		strings.Contains(target, "://") || strings.ContainsAny(target, "?#") {
		return "", ErrDOCXInvalid
	}
	for _, component := range strings.Split(target, "/") {
		if component == "" || component == "." || component == ".." {
			return "", ErrDOCXInvalid
		}
	}
	base := ""
	if ownerPart != "" {
		base = path.Dir(ownerPart)
	}
	resolved := path.Join(base, target)
	if !validDocumentLocator(resolved) {
		return "", ErrDOCXInvalid
	}
	return resolved, nil
}

func docxRelationshipKind(value string) string {
	value = strings.TrimSpace(value)
	for _, kind := range []string{
		"officeDocument", "core-properties", "styles", "footnotes", "image",
		"oleObject", "package", "aFChunk", "attachedTemplate", "vbaProject",
	} {
		if strings.HasSuffix(value, "/"+kind) {
			return kind
		}
	}
	return ""
}

func docxRelationshipsContainActiveContent(values map[string]docxRelationship) bool {
	for _, relationship := range values {
		switch docxRelationshipKind(relationship.Type) {
		case "oleObject", "package", "aFChunk", "attachedTemplate", "vbaProject":
			return true
		}
	}
	return false
}

func validDOCXAdapterConfig(config DOCXAdapterConfig) bool {
	return config.MaxEntries > 0 && config.MaxEntries <= 100_000 &&
		config.MaxEntryUncompressedBytes > 0 && config.MaxEntryUncompressedBytes <= 1<<30 &&
		config.MaxTotalUncompressedBytes >= config.MaxEntryUncompressedBytes &&
		config.MaxTotalUncompressedBytes <= 1<<34 && config.MaxXMLTokens > 0 &&
		config.MaxXMLTokens <= 20_000_000 && config.MaxXMLDepth > 0 && config.MaxXMLDepth <= 1024
}
