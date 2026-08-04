package agent

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
)

const EPUBParseReceiptVersion = "memora.epub-parse-receipt/v1"

var (
	ErrEPUBInvalid     = errors.New("EPUB is invalid")
	ErrEPUBUnsupported = errors.New("EPUB feature is unsupported")
	ErrEPUBBudget      = errors.New("EPUB exceeds parsing budget")
)

type EPUBAdapterConfig struct {
	MaxEntries                uint64
	MaxEntryUncompressedBytes uint64
	MaxTotalUncompressedBytes uint64
	MaxXMLTokens              uint64
	MaxXMLDepth               uint64
}

func DefaultEPUBAdapterConfig() EPUBAdapterConfig {
	return EPUBAdapterConfig{
		MaxEntries: 10_000, MaxEntryUncompressedBytes: 64 << 20,
		MaxTotalUncompressedBytes: 512 << 20, MaxXMLTokens: 2_000_000, MaxXMLDepth: 128,
	}
}

type EPUBSource interface {
	OpenRandomAccess(jobID, sourceID string) (SourceReference, SourceRandomAccess, error)
}

type EPUBParseReceipt struct {
	Version      string `json:"version"`
	SourceSHA256 string `json:"source_sha256"`
	OPFPath      string `json:"opf_path"`
	SpineItems   uint64 `json:"spine_items"`
	Resources    uint64 `json:"resources"`
	Nodes        uint64 `json:"nodes"`
	Footnotes    uint64 `json:"footnotes"`
	IRSHA256     string `json:"ir_sha256"`
}

func (receipt EPUBParseReceipt) Validate() error {
	if receipt.Version != EPUBParseReceiptVersion || !validSourceSHA256(receipt.SourceSHA256) ||
		!validDocumentLocator(receipt.OPFPath) || receipt.SpineItems == 0 || receipt.Resources == 0 ||
		receipt.Nodes < receipt.SpineItems+1 || !validSourceSHA256(receipt.IRSHA256) {
		return ErrEPUBInvalid
	}
	return nil
}

type EPUBAdapter struct {
	source EPUBSource
	config EPUBAdapterConfig
}

func NewEPUBAdapter(source EPUBSource, config EPUBAdapterConfig) (*EPUBAdapter, error) {
	if source == nil || !validEPUBAdapterConfig(config) {
		return nil, ErrEPUBInvalid
	}
	return &EPUBAdapter{source: source, config: config}, nil
}

type epubArchive struct {
	config EPUBAdapterConfig
	files  map[string]*zip.File
	cache  map[string][]byte
}

type epubContainerDocument struct {
	XMLName   xml.Name `xml:"container"`
	RootFiles []struct {
		FullPath string `xml:"full-path,attr"`
		Media    string `xml:"media-type,attr"`
	} `xml:"rootfiles>rootfile"`
}

type epubPackageDocument struct {
	XMLName  xml.Name `xml:"package"`
	Version  string   `xml:"version,attr"`
	Metadata struct {
		Title    string `xml:"title"`
		Language string `xml:"language"`
	} `xml:"metadata"`
	Manifest struct {
		Items []epubManifestXMLItem `xml:"item"`
	} `xml:"manifest"`
	Spine struct {
		TOC   string `xml:"toc,attr"`
		Items []struct {
			IDRef  string `xml:"idref,attr"`
			Linear string `xml:"linear,attr"`
		} `xml:"itemref"`
	} `xml:"spine"`
}

type epubManifestXMLItem struct {
	ID         string `xml:"id,attr"`
	Href       string `xml:"href,attr"`
	MediaType  string `xml:"media-type,attr"`
	Properties string `xml:"properties,attr"`
}

type epubManifestItem struct {
	ID         string
	Locator    string
	MediaType  string
	Properties []string
}

type epubPackage struct {
	Title      string
	Language   string
	OPFPath    string
	Manifest   []epubManifestItem
	ByID       map[string]epubManifestItem
	Spine      []epubManifestItem
	TOCID      string
	Navigation *epubManifestItem
}

func (adapter *EPUBAdapter) Parse(
	ctx context.Context,
	jobID string,
	sourceID string,
) (document DocumentIR, receipt EPUBParseReceipt, resultErr error) {
	if adapter == nil || ctx == nil || !validSourceIntakeID(jobID, 128) ||
		!validSourceIntakeID(sourceID, 128) {
		return DocumentIR{}, EPUBParseReceipt{}, ErrEPUBInvalid
	}
	if err := ctx.Err(); err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	reference, reader, err := adapter.source.OpenRandomAccess(jobID, sourceID)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	defer func() {
		if closeErr := reader.Close(); resultErr == nil && closeErr != nil {
			document = DocumentIR{}
			receipt = EPUBParseReceipt{}
			resultErr = fmt.Errorf("close EPUB Source: %w", closeErr)
		}
	}()
	if reference.MediaType != "application/epub+zip" || reference.Bytes > uint64(^uint64(0)>>1) {
		return DocumentIR{}, EPUBParseReceipt{}, ErrEPUBUnsupported
	}
	zipReader, err := zip.NewReader(reader, int64(reference.Bytes))
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, fmt.Errorf("%w: ZIP: %v", ErrEPUBInvalid, err)
	}
	archive, err := newEPUBArchive(ctx, zipReader, adapter.config)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	containerBytes, err := archive.read(ctx, "META-INF/container.xml")
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	var container epubContainerDocument
	if err := decodeEPUBXML(containerBytes, adapter.config, &container); err != nil ||
		container.XMLName.Local != "container" || len(container.RootFiles) != 1 ||
		container.RootFiles[0].Media != "application/oebps-package+xml" ||
		!validDocumentLocator(container.RootFiles[0].FullPath) {
		return DocumentIR{}, EPUBParseReceipt{}, ErrEPUBInvalid
	}
	packageDocument, err := archive.readPackage(ctx, container.RootFiles[0].FullPath)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	resources, resourceByLocator, err := archive.documentResources(ctx, reference.SHA256, packageDocument.Manifest)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	labels, err := archive.navigationLabels(ctx, packageDocument)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	documentID, err := StableDocumentID(reference.SHA256)
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, ErrEPUBInvalid
	}
	builder := newEPUBIRBuilder(documentID, reference, packageDocument.Title, packageDocument.Language, resources)
	if err := builder.build(ctx, archive, packageDocument.Spine, labels, resourceByLocator, adapter.config); err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	document, err = builder.seal()
	if err != nil {
		return DocumentIR{}, EPUBParseReceipt{}, err
	}
	receipt = EPUBParseReceipt{
		Version: EPUBParseReceiptVersion, SourceSHA256: reference.SHA256,
		OPFPath: packageDocument.OPFPath, SpineItems: uint64(len(packageDocument.Spine)),
		Resources: uint64(len(resources)), Nodes: uint64(len(document.Nodes)),
		Footnotes: uint64(builder.footnotes), IRSHA256: document.SHA256,
	}
	if receipt.Validate() != nil {
		return DocumentIR{}, EPUBParseReceipt{}, ErrEPUBInvalid
	}
	return document, receipt, nil
}

func newEPUBArchive(
	ctx context.Context,
	reader *zip.Reader,
	config EPUBAdapterConfig,
) (*epubArchive, error) {
	if uint64(len(reader.File)) > config.MaxEntries || len(reader.File) == 0 {
		return nil, ErrEPUBBudget
	}
	if reader.File[0].Name != "mimetype" || reader.File[0].Method != zip.Store {
		return nil, ErrEPUBInvalid
	}
	archive := &epubArchive{config: config, files: make(map[string]*zip.File), cache: make(map[string][]byte)}
	var total uint64
	for _, file := range reader.File {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if file.Flags&0x1 != 0 {
			return nil, ErrEPUBUnsupported
		}
		name := strings.TrimSuffix(file.Name, "/")
		if name == "" || (name != "mimetype" && !validDocumentLocator(name)) {
			return nil, ErrEPUBInvalid
		}
		if file.FileInfo().IsDir() {
			continue
		}
		if _, duplicate := archive.files[name]; duplicate {
			return nil, ErrEPUBInvalid
		}
		size := file.UncompressedSize64
		if size > config.MaxEntryUncompressedBytes || total > config.MaxTotalUncompressedBytes-size {
			return nil, ErrEPUBBudget
		}
		total += size
		archive.files[name] = file
	}
	mimetype, err := archive.read(ctx, "mimetype")
	if err != nil || string(mimetype) != "application/epub+zip" {
		return nil, ErrEPUBInvalid
	}
	return archive, nil
}

func (archive *epubArchive) read(ctx context.Context, locator string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := archive.cache[locator]; ok {
		return append([]byte(nil), cached...), nil
	}
	file := archive.files[locator]
	if file == nil {
		return nil, ErrEPUBInvalid
	}
	reader, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("%w: open %s: %v", ErrEPUBInvalid, locator, err)
	}
	limited := io.LimitReader(&sourceContextReader{ctx: ctx, reader: reader}, int64(archive.config.MaxEntryUncompressedBytes)+1)
	data, readErr := io.ReadAll(limited)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return nil, fmt.Errorf("%w: read %s", ErrEPUBInvalid, locator)
	}
	if uint64(len(data)) != file.UncompressedSize64 || uint64(len(data)) > archive.config.MaxEntryUncompressedBytes {
		return nil, ErrEPUBBudget
	}
	archive.cache[locator] = append([]byte(nil), data...)
	return data, nil
}

func (archive *epubArchive) readPackage(ctx context.Context, opfPath string) (epubPackage, error) {
	encoded, err := archive.read(ctx, opfPath)
	if err != nil {
		return epubPackage{}, err
	}
	var parsed epubPackageDocument
	if err := decodeEPUBXML(encoded, archive.config, &parsed); err != nil {
		return epubPackage{}, ErrEPUBInvalid
	}
	if parsed.XMLName.Local != "package" {
		return epubPackage{}, ErrEPUBInvalid
	}
	if parsed.Version != "2.0" && parsed.Version != "3.0" {
		return epubPackage{}, ErrEPUBUnsupported
	}
	result := epubPackage{
		Title: strings.TrimSpace(parsed.Metadata.Title), Language: strings.TrimSpace(parsed.Metadata.Language),
		OPFPath: opfPath, ByID: make(map[string]epubManifestItem), TOCID: strings.TrimSpace(parsed.Spine.TOC),
	}
	if !validDocumentText(result.Title, 1000, true) || !validSourceIntakeID(result.Language, 35) ||
		len(parsed.Manifest.Items) == 0 || len(parsed.Spine.Items) == 0 {
		return epubPackage{}, ErrEPUBInvalid
	}
	base := path.Dir(opfPath)
	locators := make(map[string]struct{}, len(parsed.Manifest.Items))
	for _, item := range parsed.Manifest.Items {
		if !validSourceIntakeID(item.ID, 160) || !validSourceMediaType(item.MediaType) {
			return epubPackage{}, ErrEPUBInvalid
		}
		locator, err := resolveEPUBHref(base, item.Href)
		if err != nil || archive.files[locator] == nil {
			return epubPackage{}, ErrEPUBInvalid
		}
		if _, duplicate := result.ByID[item.ID]; duplicate {
			return epubPackage{}, ErrEPUBInvalid
		}
		if _, duplicate := locators[locator]; duplicate {
			return epubPackage{}, ErrEPUBInvalid
		}
		manifestItem := epubManifestItem{
			ID: item.ID, Locator: locator, MediaType: item.MediaType,
			Properties: strings.Fields(item.Properties),
		}
		result.Manifest = append(result.Manifest, manifestItem)
		result.ByID[item.ID] = manifestItem
		locators[locator] = struct{}{}
		if containsEPUBProperty(manifestItem.Properties, "nav") {
			if result.Navigation != nil {
				return epubPackage{}, ErrEPUBInvalid
			}
			copy := manifestItem
			result.Navigation = &copy
		}
	}
	for _, itemRef := range parsed.Spine.Items {
		item, exists := result.ByID[strings.TrimSpace(itemRef.IDRef)]
		if !exists || item.MediaType != "application/xhtml+xml" {
			return epubPackage{}, ErrEPUBInvalid
		}
		result.Spine = append(result.Spine, item)
	}
	return result, nil
}

func (archive *epubArchive) documentResources(
	ctx context.Context,
	sourceSHA256 string,
	manifest []epubManifestItem,
) ([]DocumentResource, map[string]DocumentResource, error) {
	resources := make([]DocumentResource, 0, len(manifest))
	byLocator := make(map[string]DocumentResource, len(manifest))
	for _, item := range manifest {
		data, err := archive.read(ctx, item.Locator)
		if err != nil || len(data) == 0 {
			return nil, nil, ErrEPUBInvalid
		}
		id, err := StableDocumentResourceID(sourceSHA256, item.Locator)
		if err != nil {
			return nil, nil, ErrEPUBInvalid
		}
		digest := sha256.Sum256(data)
		resource := DocumentResource{
			ID: id, Locator: item.Locator, MediaType: item.MediaType, Bytes: uint64(len(data)),
			SHA256: "sha256:" + hex.EncodeToString(digest[:]),
		}
		resources = append(resources, resource)
		byLocator[item.Locator] = resource
	}
	return resources, byLocator, nil
}

func (archive *epubArchive) navigationLabels(
	ctx context.Context,
	packageDocument epubPackage,
) (map[string]string, error) {
	if packageDocument.Navigation != nil {
		data, err := archive.read(ctx, packageDocument.Navigation.Locator)
		if err != nil {
			return nil, err
		}
		return parseEPUBNavigation(data, packageDocument.Navigation.Locator, archive.config)
	}
	if packageDocument.TOCID == "" {
		return map[string]string{}, nil
	}
	item, exists := packageDocument.ByID[packageDocument.TOCID]
	if !exists || item.MediaType != "application/x-dtbncx+xml" {
		return nil, ErrEPUBInvalid
	}
	data, err := archive.read(ctx, item.Locator)
	if err != nil {
		return nil, err
	}
	return parseEPUBNCX(data, item.Locator, archive.config)
}

func decodeEPUBXML(data []byte, config EPUBAdapterConfig, target any) error {
	if len(data) == 0 || uint64(len(data)) > config.MaxEntryUncompressedBytes {
		return ErrEPUBBudget
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	var tokens, depth uint64
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return ErrEPUBInvalid
		}
		tokens++
		if tokens > config.MaxXMLTokens {
			return ErrEPUBBudget
		}
		switch value := token.(type) {
		case xml.StartElement:
			depth++
			if depth > config.MaxXMLDepth {
				return ErrEPUBBudget
			}
		case xml.EndElement:
			if depth == 0 {
				return ErrEPUBInvalid
			}
			depth--
		case xml.Directive:
			return ErrEPUBUnsupported
		case xml.ProcInst:
			if strings.ToLower(value.Target) != "xml" {
				return ErrEPUBUnsupported
			}
		}
	}
	if depth != 0 {
		return ErrEPUBInvalid
	}
	decoder = xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	if err := decoder.Decode(target); err != nil {
		return ErrEPUBInvalid
	}
	return nil
}

func resolveEPUBHref(base, href string) (string, error) {
	href = strings.TrimSpace(href)
	if href == "" || strings.Contains(href, "\\") || strings.HasPrefix(href, "/") ||
		strings.Contains(href, "://") || strings.Contains(href, "?") {
		return "", ErrEPUBInvalid
	}
	withoutFragment := strings.SplitN(href, "#", 2)[0]
	for _, part := range strings.Split(withoutFragment, "/") {
		if part == ".." || part == "." || part == "" {
			return "", ErrEPUBInvalid
		}
	}
	locator := path.Join(base, withoutFragment)
	if !validDocumentLocator(locator) {
		return "", ErrEPUBInvalid
	}
	return locator, nil
}

func containsEPUBProperty(properties []string, target string) bool {
	for _, property := range properties {
		if property == target {
			return true
		}
	}
	return false
}

func validEPUBAdapterConfig(config EPUBAdapterConfig) bool {
	return config.MaxEntries > 0 && config.MaxEntries <= 100_000 &&
		config.MaxEntryUncompressedBytes > 0 && config.MaxEntryUncompressedBytes <= 1<<30 &&
		config.MaxTotalUncompressedBytes >= config.MaxEntryUncompressedBytes &&
		config.MaxTotalUncompressedBytes <= 1<<34 && config.MaxXMLTokens > 0 &&
		config.MaxXMLTokens <= 20_000_000 && config.MaxXMLDepth > 0 && config.MaxXMLDepth <= 1024
}
