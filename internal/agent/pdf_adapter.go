package agent

// This adapter deliberately implements a small, deterministic PDF text layer.
// It is not an OCR engine and does not attempt to infer tables from visual
// layout.  The accepted subset is intentionally explicit so that a parse can
// be replayed and audited without a model or a third-party runtime.

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const PDFParseReceiptVersion = "memora.pdf-parse-receipt/v1"

var (
	ErrPDFInvalid           = errors.New("PDF is invalid")
	ErrPDFUnsupported       = errors.New("PDF feature is unsupported")
	ErrPDFBudget            = errors.New("PDF exceeds parsing budget")
	ErrPDFTextLayerRequired = errors.New("PDF text layer is required")
)

type PDFAdapterConfig struct {
	MaxFileBytes         uint64
	MaxObjects           uint64
	MaxPages             uint64
	MaxPageTreeDepth     uint64
	MaxStreamBytes       uint64
	MaxDecompressedBytes uint64
	MaxContentTokens     uint64
	MaxTextBytes         uint64
}

func DefaultPDFAdapterConfig() PDFAdapterConfig {
	return PDFAdapterConfig{
		MaxFileBytes: 128 << 20, MaxObjects: 100_000, MaxPages: 10_000,
		MaxPageTreeDepth: 64, MaxStreamBytes: 64 << 20, MaxDecompressedBytes: 512 << 20,
		MaxContentTokens: 2_000_000, MaxTextBytes: 64 << 20,
	}
}

type PDFSource interface {
	OpenRandomAccess(jobID, sourceID string) (SourceReference, SourceRandomAccess, error)
}

type PDFParseReceipt struct {
	Version      string `json:"version"`
	SourceSHA256 string `json:"source_sha256"`
	Pages        uint64 `json:"pages"`
	TextPages    uint64 `json:"text_pages"`
	Paragraphs   uint64 `json:"paragraphs"`
	Glyphs       uint64 `json:"glyphs"`
	IRSHA256     string `json:"ir_sha256"`
}

func (receipt PDFParseReceipt) Validate() error {
	if receipt.Version != PDFParseReceiptVersion || !validSourceSHA256(receipt.SourceSHA256) ||
		receipt.Pages == 0 || receipt.TextPages == 0 || receipt.Paragraphs == 0 ||
		!validSourceSHA256(receipt.IRSHA256) {
		return ErrPDFInvalid
	}
	return nil
}

type PDFAdapter struct {
	source PDFSource
	config PDFAdapterConfig
}

func NewPDFAdapter(source PDFSource, config PDFAdapterConfig) (*PDFAdapter, error) {
	if source == nil || !validPDFAdapterConfig(config) {
		return nil, ErrPDFInvalid
	}
	return &PDFAdapter{source: source, config: config}, nil
}

func validPDFAdapterConfig(config PDFAdapterConfig) bool {
	return config.MaxFileBytes > 0 && config.MaxObjects > 0 && config.MaxPages > 0 &&
		config.MaxPageTreeDepth > 0 && config.MaxStreamBytes > 0 && config.MaxDecompressedBytes > 0 &&
		config.MaxContentTokens > 0 && config.MaxTextBytes > 0
}

func (adapter *PDFAdapter) Parse(ctx context.Context, jobID, sourceID string) (
	document DocumentIR, receipt PDFParseReceipt, resultErr error,
) {
	if adapter == nil || ctx == nil || !validSourceIntakeID(jobID, 128) || !validSourceIntakeID(sourceID, 128) {
		return DocumentIR{}, PDFParseReceipt{}, ErrPDFInvalid
	}
	if err := ctx.Err(); err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	reference, reader, err := adapter.source.OpenRandomAccess(jobID, sourceID)
	if err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	defer func() {
		if closeErr := reader.Close(); resultErr == nil && closeErr != nil {
			document, receipt = DocumentIR{}, PDFParseReceipt{}
			resultErr = fmt.Errorf("close PDF Source: %w", closeErr)
		}
	}()
	if reference.MediaType != "application/pdf" {
		return DocumentIR{}, PDFParseReceipt{}, ErrPDFUnsupported
	}
	if reference.Bytes > adapter.config.MaxFileBytes || reference.Bytes > uint64(^uint(0)>>1) {
		return DocumentIR{}, PDFParseReceipt{}, ErrPDFBudget
	}
	data, err := readPDFSource(ctx, reader, reference.Bytes, adapter.config.MaxFileBytes)
	if err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	digest := sha256.Sum256(data)
	if "sha256:"+fmt.Sprintf("%x", digest[:]) != reference.SHA256 {
		return DocumentIR{}, PDFParseReceipt{}, ErrSourceStoreDigest
	}
	pdf, err := parsePDF(data, adapter.config)
	if err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	document, stats, err := pdf.buildIR(ctx, reference, data)
	if err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	receipt = PDFParseReceipt{Version: PDFParseReceiptVersion, SourceSHA256: reference.SHA256,
		Pages: uint64(len(pdf.pages)), TextPages: stats.textPages, Paragraphs: stats.paragraphs,
		Glyphs: stats.glyphs, IRSHA256: document.SHA256}
	if err := receipt.Validate(); err != nil {
		return DocumentIR{}, PDFParseReceipt{}, err
	}
	return document, receipt, nil
}

func readPDFSource(ctx context.Context, reader io.Reader, size, limit uint64) ([]byte, error) {
	if size > limit || size > uint64(^uint(0)>>1) {
		return nil, ErrPDFBudget
	}
	limited := io.LimitReader(&sourceContextReader{ctx: ctx, reader: reader}, int64(size)+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, err
	}
	if uint64(len(data)) != size {
		return nil, ErrPDFInvalid
	}
	return data, nil
}

type pdfRef struct{ obj, gen int }

type pdfValue struct {
	kind byte
	name string
	text string
	num  float64
	int  int64
	arr  []pdfValue
	dict map[string]pdfValue
	ref  *pdfRef
}

const (
	pdfNull byte = iota
	pdfBool
	pdfNumber
	pdfName
	pdfString
	pdfHex
	pdfArray
	pdfDict
	pdfRefKind
)

type pdfObject struct {
	number, generation int
	value              pdfValue
	stream             []byte
	start, end         uint64
}

type pdfDocument struct {
	data    []byte
	objects map[int]pdfObject
	root    pdfRef
	info    *pdfRef
	pages   []pdfPage
	config  PDFAdapterConfig
}

type pdfPage struct {
	object    pdfObject
	resources pdfValue
	contents  []pdfRef
}

type pdfParseStats struct{ textPages, paragraphs, glyphs uint64 }

func parsePDF(data []byte, config PDFAdapterConfig) (*pdfDocument, error) {
	if len(data) < 12 || !bytes.HasPrefix(data, []byte("%PDF-")) || data[5] < '1' || data[5] > '2' ||
		data[6] != '.' || data[7] < '4' || data[7] > '7' || !bytes.Contains(data, []byte("%%EOF")) {
		return nil, ErrPDFInvalid
	}
	start, err := pdfStartXRef(data)
	if err != nil {
		return nil, err
	}
	if start < 0 || start >= len(data) || !bytes.HasPrefix(bytes.TrimSpace(data[start:]), []byte("xref")) {
		return nil, ErrPDFUnsupported
	}
	xref, trailer, err := parsePDFXRef(data, start, config)
	if err != nil {
		return nil, err
	}
	if _, ok := trailer.dict["Encrypt"]; ok {
		return nil, ErrPDFUnsupported
	}
	root := trailerRef(trailer, "Root")
	if root == nil {
		return nil, ErrPDFInvalid
	}
	doc := &pdfDocument{data: data, objects: make(map[int]pdfObject), root: *root, config: config}
	if info := trailerRef(trailer, "Info"); info != nil {
		doc.info = info
	}
	if uint64(len(xref)) > config.MaxObjects {
		return nil, ErrPDFBudget
	}
	for number, offset := range xref {
		object, err := parsePDFObject(data, number, offset)
		if err != nil {
			return nil, err
		}
		doc.objects[number] = object
	}
	seen := make(map[int]bool)
	rootObject, err := doc.resolve(*root)
	if err != nil {
		return nil, err
	}
	pagesValue, ok := dictGet(rootObject.value, "Pages")
	if !ok {
		return nil, ErrPDFInvalid
	}
	pagesRef := valueRef(pagesValue)
	if pagesRef == nil {
		return nil, ErrPDFInvalid
	}
	if err := doc.walkPages(*pagesRef, 0, seen, pdfValue{}, pdfValue{}); err != nil {
		return nil, err
	}
	if len(doc.pages) == 0 {
		return nil, ErrPDFInvalid
	}
	return doc, nil
}

func pdfStartXRef(data []byte) (int, error) {
	idx := bytes.LastIndex(data, []byte("startxref"))
	if idx < 0 {
		return 0, ErrPDFInvalid
	}
	pos := idx + len("startxref")
	for pos < len(data) && isPDFSpace(data[pos]) {
		pos++
	}
	start := pos
	for pos < len(data) && data[pos] >= '0' && data[pos] <= '9' {
		pos++
	}
	if start == pos {
		return 0, ErrPDFInvalid
	}
	n, err := strconv.Atoi(string(data[start:pos]))
	if err != nil {
		return 0, ErrPDFInvalid
	}
	return n, nil
}

func parsePDFXRef(data []byte, offset int, config PDFAdapterConfig) (map[int]int, pdfValue, error) {
	pos := offset
	pos = skipPDFSpace(data, pos)
	if !bytes.HasPrefix(data[pos:], []byte("xref")) {
		return nil, pdfValue{}, ErrPDFUnsupported
	}
	pos += 4
	xref := make(map[int]int)
	for {
		pos = skipPDFSpace(data, pos)
		if bytes.HasPrefix(data[pos:], []byte("trailer")) {
			pos += len("trailer")
			break
		}
		first, next, ok := pdfLineInts(data, pos)
		if !ok || len(first) != 2 {
			return nil, pdfValue{}, ErrPDFInvalid
		}
		pos = next
		for i := 0; i < first[1]; i++ {
			lineStart := pos
			for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
				pos++
			}
			fields := strings.Fields(string(data[lineStart:pos]))
			pos = skipPDFSpace(data, pos)
			if len(fields) < 3 {
				return nil, pdfValue{}, ErrPDFInvalid
			}
			if fields[2] == "n" {
				off, err := strconv.Atoi(fields[0])
				if err != nil || off < 0 || off >= len(data) {
					return nil, pdfValue{}, ErrPDFInvalid
				}
				xref[first[0]+i] = off
			}
		}
		if uint64(len(xref)) > config.MaxObjects {
			return nil, pdfValue{}, ErrPDFBudget
		}
	}
	pos = skipPDFSpace(data, pos)
	parser := pdfValueParser{data: data, pos: pos}
	trailer, err := parser.parse()
	if err != nil || trailer.kind != pdfDict {
		return nil, pdfValue{}, ErrPDFInvalid
	}
	if _, ok := trailer.dict["Prev"]; ok {
		return nil, pdfValue{}, ErrPDFUnsupported
	}
	return xref, trailer, nil
}

func pdfLineInts(data []byte, pos int) ([]int, int, bool) {
	start := pos
	for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
		pos++
	}
	fields := strings.Fields(string(data[start:pos]))
	values := make([]int, len(fields))
	for i, field := range fields {
		n, err := strconv.Atoi(field)
		if err != nil {
			return nil, pos, false
		}
		values[i] = n
	}
	return values, skipPDFSpace(data, pos), true
}

func parsePDFObject(data []byte, number, offset int) (pdfObject, error) {
	if offset < 0 || offset >= len(data) {
		return pdfObject{}, ErrPDFInvalid
	}
	p := pdfValueParser{data: data, pos: offset}
	_, err := p.parseNumberOnly()
	if err != nil {
		return pdfObject{}, ErrPDFInvalid
	}
	g, err := p.parseNumberOnly()
	if err != nil {
		return pdfObject{}, ErrPDFInvalid
	}
	p.skip()
	if !p.consumeWord("obj") {
		return pdfObject{}, ErrPDFInvalid
	}
	start := offset
	value, err := p.parse()
	if err != nil {
		return pdfObject{}, ErrPDFInvalid
	}
	p.skip()
	object := pdfObject{number: number, generation: int(g), value: value, start: uint64(start)}
	if bytes.HasPrefix(data[p.pos:], []byte("stream")) {
		p.pos += len("stream")
		if p.pos < len(data) && data[p.pos] == '\r' {
			p.pos++
			if p.pos < len(data) && data[p.pos] == '\n' {
				p.pos++
			}
		} else if p.pos < len(data) && data[p.pos] == '\n' {
			p.pos++
		}
		length, ok := dictInt(value, "Length")
		if !ok || length < 0 || length > int64(len(data)-p.pos) {
			return pdfObject{}, ErrPDFInvalid
		}
		object.stream = append([]byte(nil), data[p.pos:p.pos+int(length)]...)
		p.pos += int(length)
		p.skip()
		if !p.consumeWord("endstream") {
			return pdfObject{}, ErrPDFInvalid
		}
		p.skip()
	}
	end := bytes.Index(data[p.pos:], []byte("endobj"))
	if end < 0 {
		return pdfObject{}, ErrPDFInvalid
	}
	object.end = uint64(p.pos + end + len("endobj"))
	return object, nil
}

func (doc *pdfDocument) resolve(ref pdfRef) (pdfObject, error) {
	object, ok := doc.objects[ref.obj]
	if !ok || object.generation != ref.gen {
		return pdfObject{}, ErrPDFInvalid
	}
	return object, nil
}

func (doc *pdfDocument) walkPages(ref pdfRef, depth uint64, seen map[int]bool, inherited pdfValue, inheritedBox pdfValue) error {
	if depth > doc.config.MaxPageTreeDepth {
		return ErrPDFBudget
	}
	if seen[ref.obj] {
		return ErrPDFInvalid
	}
	seen[ref.obj] = true
	defer delete(seen, ref.obj)
	object, err := doc.resolve(ref)
	if err != nil {
		return err
	}
	typeName, _ := dictName(object.value, "Type")
	if typeName == "Pages" {
		resources, ok := dictGet(object.value, "Resources")
		if !ok {
			resources = inherited
		}
		box, ok := dictGet(object.value, "MediaBox")
		if !ok {
			box = inheritedBox
		}
		kids, ok := dictGet(object.value, "Kids")
		if !ok || kids.kind != pdfArray || len(kids.arr) == 0 {
			return ErrPDFInvalid
		}
		for _, kid := range kids.arr {
			kidRef := valueRef(kid)
			if kidRef == nil {
				return ErrPDFInvalid
			}
			if err := doc.walkPages(*kidRef, depth+1, seen, resources, box); err != nil {
				return err
			}
		}
		return nil
	}
	if typeName != "Page" {
		return ErrPDFInvalid
	}
	if uint64(len(doc.pages)) >= doc.config.MaxPages {
		return ErrPDFBudget
	}
	resources, ok := dictGet(object.value, "Resources")
	if !ok {
		resources = inherited
	}
	if resources.kind == pdfRefKind {
		resourceObject, resolveErr := doc.resolve(*resources.ref)
		if resolveErr != nil {
			return resolveErr
		}
		resources = resourceObject.value
	}
	if resources.kind == 0 {
		return ErrPDFInvalid
	}
	contents, ok := dictGet(object.value, "Contents")
	if !ok {
		return ErrPDFInvalid
	}
	var refs []pdfRef
	if r := valueRef(contents); r != nil {
		refs = []pdfRef{*r}
	} else if contents.kind == pdfArray {
		for _, item := range contents.arr {
			r := valueRef(item)
			if r == nil {
				return ErrPDFInvalid
			}
			refs = append(refs, *r)
		}
	} else {
		return ErrPDFInvalid
	}
	if len(refs) == 0 {
		return ErrPDFInvalid
	}
	doc.pages = append(doc.pages, pdfPage{object: object, resources: resources, contents: refs})
	return nil
}

type pdfValueParser struct {
	data []byte
	pos  int
}

func (p *pdfValueParser) skip() { p.pos = skipPDFSpace(p.data, p.pos) }

func (p *pdfValueParser) parse() (pdfValue, error) {
	p.skip()
	if p.pos >= len(p.data) {
		return pdfValue{}, ErrPDFInvalid
	}
	switch p.data[p.pos] {
	case '<':
		if p.pos+1 < len(p.data) && p.data[p.pos+1] == '<' {
			return p.parseDict()
		}
		return p.parseHex()
	case '[':
		return p.parseArray()
	case '(':
		return p.parseLiteral()
	case '/':
		name, err := p.parseName()
		return pdfValue{kind: pdfName, name: name}, err
	default:
		return p.parseWordOrNumber()
	}
}

func (p *pdfValueParser) parseDict() (pdfValue, error) {
	p.pos += 2
	value := pdfValue{kind: pdfDict, dict: make(map[string]pdfValue)}
	for {
		p.skip()
		if p.pos+1 < len(p.data) && p.data[p.pos] == '>' && p.data[p.pos+1] == '>' {
			p.pos += 2
			return value, nil
		}
		if p.pos >= len(p.data) || p.data[p.pos] != '/' {
			return pdfValue{}, ErrPDFInvalid
		}
		name, err := p.parseName()
		if err != nil {
			return pdfValue{}, err
		}
		item, err := p.parse()
		if err != nil {
			return pdfValue{}, err
		}
		value.dict[name] = item
	}
}

func (p *pdfValueParser) parseArray() (pdfValue, error) {
	p.pos++
	value := pdfValue{kind: pdfArray}
	for {
		p.skip()
		if p.pos >= len(p.data) {
			return pdfValue{}, ErrPDFInvalid
		}
		if p.data[p.pos] == ']' {
			p.pos++
			return value, nil
		}
		item, err := p.parse()
		if err != nil {
			return pdfValue{}, err
		}
		value.arr = append(value.arr, item)
		if len(value.arr) > 100_000 {
			return pdfValue{}, ErrPDFBudget
		}
	}
}

func (p *pdfValueParser) parseName() (string, error) {
	if p.pos >= len(p.data) || p.data[p.pos] != '/' {
		return "", ErrPDFInvalid
	}
	p.pos++
	start := p.pos
	for p.pos < len(p.data) && !isPDFDelimiter(p.data[p.pos]) {
		p.pos++
	}
	if start == p.pos {
		return "", ErrPDFInvalid
	}
	return string(p.data[start:p.pos]), nil
}

func (p *pdfValueParser) parseLiteral() (pdfValue, error) {
	p.pos++
	var output bytes.Buffer
	depth := 1
	for p.pos < len(p.data) {
		ch := p.data[p.pos]
		p.pos++
		switch ch {
		case '(':
			depth++
			output.WriteByte(ch)
		case ')':
			depth--
			if depth == 0 {
				return pdfValue{kind: pdfString, text: output.String()}, nil
			}
			output.WriteByte(ch)
		case '\\':
			if p.pos >= len(p.data) {
				return pdfValue{}, ErrPDFInvalid
			}
			escaped := p.data[p.pos]
			p.pos++
			switch escaped {
			case 'n':
				output.WriteByte('\n')
			case 'r':
				output.WriteByte('\r')
			case 't':
				output.WriteByte('\t')
			case 'b':
				output.WriteByte('\b')
			case 'f':
				output.WriteByte('\f')
			case '\r':
				if p.pos < len(p.data) && p.data[p.pos] == '\n' {
					p.pos++
				}
			case '\n':
			default:
				output.WriteByte(escaped)
			}
		default:
			output.WriteByte(ch)
		}
	}
	return pdfValue{}, ErrPDFInvalid
}

func (p *pdfValueParser) parseHex() (pdfValue, error) {
	p.pos++
	start := p.pos
	for p.pos < len(p.data) && p.data[p.pos] != '>' {
		p.pos++
	}
	if p.pos >= len(p.data) {
		return pdfValue{}, ErrPDFInvalid
	}
	encoded := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\r' || r == '\t' {
			return -1
		}
		return r
	}, string(p.data[start:p.pos]))
	p.pos++
	if len(encoded)%2 != 0 {
		encoded += "0"
	}
	decoded := make([]byte, len(encoded)/2)
	for i := range decoded {
		hi, ok1 := pdfHexNibble(encoded[i*2])
		lo, ok2 := pdfHexNibble(encoded[i*2+1])
		if !ok1 || !ok2 {
			return pdfValue{}, ErrPDFInvalid
		}
		decoded[i] = hi<<4 | lo
	}
	return pdfValue{kind: pdfHex, text: string(decoded)}, nil
}

func pdfHexNibble(ch byte) (byte, bool) {
	switch {
	case ch >= '0' && ch <= '9':
		return ch - '0', true
	case ch >= 'a' && ch <= 'f':
		return ch - 'a' + 10, true
	case ch >= 'A' && ch <= 'F':
		return ch - 'A' + 10, true
	}
	return 0, false
}

func (p *pdfValueParser) parseWordOrNumber() (pdfValue, error) {
	start := p.pos
	for p.pos < len(p.data) && !isPDFDelimiter(p.data[p.pos]) {
		p.pos++
	}
	word := string(p.data[start:p.pos])
	if word == "true" {
		return pdfValue{kind: pdfBool, int: 1}, nil
	}
	if word == "false" {
		return pdfValue{kind: pdfBool}, nil
	}
	if word == "null" {
		return pdfValue{kind: pdfNull}, nil
	}
	num, err := strconv.ParseFloat(word, 64)
	if err != nil || word == "" {
		return pdfValue{}, ErrPDFInvalid
	}
	integer, intErr := strconv.ParseInt(word, 10, 64)
	if intErr != nil {
		integer = int64(num)
	}
	value := pdfValue{kind: pdfNumber, num: num, int: integer}
	// Indirect references are only recognized when the two following tokens are
	// integral generation + the literal r.
	checkpoint := p.pos
	p.skip()
	genStart := p.pos
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	if genStart != p.pos {
		gen, genErr := strconv.Atoi(string(p.data[genStart:p.pos]))
		p.skip()
		if genErr == nil && p.pos < len(p.data) && p.data[p.pos] == 'R' {
			p.pos++
			return pdfValue{kind: pdfRefKind, ref: &pdfRef{obj: int(integer), gen: gen}}, nil
		}
	}
	p.pos = checkpoint
	return value, nil
}

func (p *pdfValueParser) parseNumberOnly() (int64, error) {
	p.skip()
	start := p.pos
	if p.pos < len(p.data) && (p.data[p.pos] == '+' || p.data[p.pos] == '-') {
		p.pos++
	}
	for p.pos < len(p.data) && p.data[p.pos] >= '0' && p.data[p.pos] <= '9' {
		p.pos++
	}
	if start == p.pos {
		return 0, ErrPDFInvalid
	}
	return strconv.ParseInt(string(p.data[start:p.pos]), 10, 64)
}

func skipPDFSpace(data []byte, pos int) int {
	for pos < len(data) {
		if isPDFSpace(data[pos]) {
			pos++
			continue
		}
		if data[pos] == '%' {
			for pos < len(data) && data[pos] != '\n' && data[pos] != '\r' {
				pos++
			}
			continue
		}
		break
	}
	return pos
}

func isPDFSpace(ch byte) bool {
	return ch == 0 || ch == '\t' || ch == '\n' || ch == '\r' || ch == '\f' || ch == ' '
}
func isPDFDelimiter(ch byte) bool {
	return isPDFSpace(ch) || ch == '(' || ch == ')' || ch == '<' || ch == '>' || ch == '[' || ch == ']' || ch == '/' || ch == '%'
}

func (p *pdfValueParser) consumeWord(word string) bool {
	p.skip()
	if !bytes.HasPrefix(p.data[p.pos:], []byte(word)) {
		return false
	}
	p.pos += len(word)
	return true
}

func dictGet(value pdfValue, key string) (pdfValue, bool) {
	if value.kind != pdfDict {
		return pdfValue{}, false
	}
	result, ok := value.dict[key]
	return result, ok
}
func dictName(value pdfValue, key string) (string, bool) {
	item, ok := dictGet(value, key)
	return item.name, ok && item.kind == pdfName
}
func dictInt(value pdfValue, key string) (int64, bool) {
	item, ok := dictGet(value, key)
	return item.int, ok && item.kind == pdfNumber
}
func valueRef(value pdfValue) *pdfRef {
	if value.kind != pdfRefKind || value.ref == nil {
		return nil
	}
	ref := *value.ref
	return &ref
}
func trailerRef(value pdfValue, key string) *pdfRef {
	item, ok := dictGet(value, key)
	if !ok {
		return nil
	}
	return valueRef(item)
}

type pdfFont struct {
	type0    bool
	encoding string
	cmap     pdfCMap
}

type pdfCMap struct {
	codeLength int
	values     map[string]string
}

func (doc *pdfDocument) buildIR(ctx context.Context, reference SourceReference, data []byte) (DocumentIR, pdfParseStats, error) {
	documentID, err := StableDocumentID(reference.SHA256)
	if err != nil {
		return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
	}
	resourceID, err := StableDocumentResourceID(reference.SHA256, reference.FileName)
	if err != nil {
		return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
	}
	resource := DocumentResource{ID: resourceID, Locator: reference.FileName, MediaType: "application/pdf", Bytes: uint64(len(data)), SHA256: reference.SHA256}
	title := strings.TrimSpace(reference.FileName)
	if doc.info != nil {
		if info, e := doc.resolve(*doc.info); e == nil {
			if value, ok := dictGet(info.value, "Title"); ok && (value.kind == pdfString || value.kind == pdfHex) {
				if decoded := string(value.text); strings.TrimSpace(decoded) != "" {
					title = decoded
				}
			}
		}
	}
	title = strings.TrimSuffix(title, ".pdf")
	language := "und"
	if root, e := doc.resolve(doc.root); e == nil {
		if value, ok := dictGet(root.value, "Lang"); ok && (value.kind == pdfString || value.kind == pdfName) && strings.TrimSpace(value.text+value.name) != "" {
			if value.kind == pdfString {
				language = strings.TrimSpace(value.text)
			} else {
				language = strings.TrimSpace(value.name)
			}
		}
	}
	draft := DocumentIR{Version: DocumentIRVersion, DocumentID: documentID, Source: reference, Title: title, Language: language, Resources: []DocumentResource{resource}}
	rootID, err := StableDocumentNodeID(documentID, "", DocumentNodeDocument, 1)
	if err != nil {
		return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
	}
	draft.Nodes = append(draft.Nodes, DocumentNode{ID: rootID, Kind: DocumentNodeDocument, Ordinal: 1, Label: title, Anchor: DocumentAnchor{ResourceID: resourceID, Start: 0, End: uint64(len(data))}})
	stats := pdfParseStats{}
	for pageIndex, page := range doc.pages {
		if err := ctx.Err(); err != nil {
			return DocumentIR{}, pdfParseStats{}, err
		}
		lines, err := doc.pageText(page, pageIndex+1)
		if err != nil {
			return DocumentIR{}, pdfParseStats{}, err
		}
		if len(lines) == 0 {
			continue
		}
		stats.textPages++
		pageOrdinal := uint64(pageIndex + 1)
		pageID, e := StableDocumentNodeID(documentID, rootID, DocumentNodePart, pageOrdinal)
		if e != nil {
			return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
		}
		draft.Nodes = append(draft.Nodes, DocumentNode{ID: pageID, ParentID: rootID, Kind: DocumentNodePart, Ordinal: pageOrdinal, Label: fmt.Sprintf("Page %d", pageIndex+1), Anchor: DocumentAnchor{ResourceID: resourceID, Start: page.object.start, End: page.object.end, Selector: fmt.Sprintf("pdf:page=%d", pageIndex+1)}})
		for lineIndex, line := range lines {
			ordinal := uint64(lineIndex + 1)
			paragraphID, e := StableDocumentNodeID(documentID, pageID, DocumentNodeParagraph, ordinal)
			if e != nil {
				return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
			}
			selector := fmt.Sprintf("pdf:page=%d;stream=%d %d R;line=%d", pageIndex+1, line.ref.obj, line.ref.gen, line.line)
			start, end := line.streamStart, line.streamEnd
			if start >= end || end > uint64(len(data)) {
				return DocumentIR{}, pdfParseStats{}, ErrPDFInvalid
			}
			draft.Nodes = append(draft.Nodes, DocumentNode{ID: paragraphID, ParentID: pageID, Kind: DocumentNodeParagraph, Ordinal: ordinal, Text: line.text, Anchor: DocumentAnchor{ResourceID: resourceID, Start: start, End: end, Selector: selector}})
			draft.ReadingOrder = append(draft.ReadingOrder, paragraphID)
			stats.paragraphs++
			stats.glyphs += uint64(utf8.RuneCountInString(line.text))
		}
	}
	if stats.textPages == 0 || stats.paragraphs == 0 {
		return DocumentIR{}, pdfParseStats{}, ErrPDFTextLayerRequired
	}
	sealed, err := SealDocumentIR(draft)
	if err != nil {
		return DocumentIR{}, pdfParseStats{}, err
	}
	return sealed, stats, nil
}

type pdfTextLine struct {
	text                   string
	ref                    pdfRef
	line                   uint64
	streamStart, streamEnd uint64
}

func (doc *pdfDocument) pageText(page pdfPage, pageNumber int) ([]pdfTextLine, error) {
	fonts, err := doc.pageFonts(page.resources)
	if err != nil {
		return nil, err
	}
	var lines []pdfTextLine
	for _, ref := range page.contents {
		object, err := doc.resolve(ref)
		if err != nil {
			return nil, err
		}
		if len(object.stream) == 0 {
			return nil, ErrPDFInvalid
		}
		decoded, err := pdfDecodeStream(object, doc.config)
		if err != nil {
			return nil, err
		}
		parsed, err := pdfContentText(decoded, fonts, ref, object.start, object.end, doc.config)
		if err != nil {
			return nil, fmt.Errorf("page %d: %w", pageNumber, err)
		}
		lines = append(lines, parsed...)
	}
	return lines, nil
}

func pdfDecodeStream(object pdfObject, config PDFAdapterConfig) ([]byte, error) {
	if uint64(len(object.stream)) > config.MaxStreamBytes {
		return nil, ErrPDFBudget
	}
	filter, ok := dictGet(object.value, "Filter")
	if !ok {
		return append([]byte(nil), object.stream...), nil
	}
	if filter.kind != pdfName || filter.name != "FlateDecode" {
		return nil, ErrPDFUnsupported
	}
	reader, err := zlib.NewReader(bytes.NewReader(object.stream))
	if err != nil {
		return nil, ErrPDFInvalid
	}
	defer func() { _ = reader.Close() }()
	decoded, err := io.ReadAll(io.LimitReader(reader, int64(config.MaxDecompressedBytes)+1))
	if err != nil {
		return nil, ErrPDFInvalid
	}
	if uint64(len(decoded)) > config.MaxDecompressedBytes {
		return nil, ErrPDFBudget
	}
	return decoded, nil
}

func (doc *pdfDocument) pageFonts(resources pdfValue) (map[string]pdfFont, error) {
	fontsValue, ok := dictGet(resources, "Font")
	if !ok {
		return nil, ErrPDFInvalid
	}
	if fontsValue.kind == pdfRefKind {
		object, err := doc.resolve(*fontsValue.ref)
		if err != nil {
			return nil, err
		}
		fontsValue = object.value
	}
	if fontsValue.kind != pdfDict {
		return nil, ErrPDFInvalid
	}
	fonts := make(map[string]pdfFont, len(fontsValue.dict))
	for name, value := range fontsValue.dict {
		ref := valueRef(value)
		if ref == nil {
			return nil, ErrPDFInvalid
		}
		object, err := doc.resolve(*ref)
		if err != nil {
			return nil, err
		}
		subtype, _ := dictName(object.value, "Subtype")
		font := pdfFont{type0: subtype == "Type0", encoding: "WinAnsiEncoding"}
		if !font.type0 {
			if encoding, ok := dictName(object.value, "Encoding"); ok {
				font.encoding = encoding
			}
		} else {
			toUnicode, ok := dictGet(object.value, "ToUnicode")
			if !ok {
				return nil, ErrPDFUnsupported
			}
			unicodeRef := valueRef(toUnicode)
			if unicodeRef == nil {
				return nil, ErrPDFUnsupported
			}
			unicodeObject, err := doc.resolve(*unicodeRef)
			if err != nil {
				return nil, err
			}
			decoded, err := pdfDecodeStream(unicodeObject, doc.config)
			if err != nil {
				return nil, err
			}
			font.cmap, err = parsePDFCMap(decoded)
			if err != nil {
				return nil, err
			}
		}
		fonts[name] = font
	}
	return fonts, nil
}

func pdfContentText(data []byte, fonts map[string]pdfFont, ref pdfRef, start, end uint64, config PDFAdapterConfig) ([]pdfTextLine, error) {
	parser := pdfValueParser{data: data}
	operands := make([]pdfValue, 0, 8)
	insideText := false
	fontName := ""
	current := strings.Builder{}
	lineNumber := uint64(0)
	lines := make([]pdfTextLine, 0)
	flush := func() {
		text := strings.TrimSpace(current.String())
		current.Reset()
		if text != "" {
			lineNumber++
			lines = append(lines, pdfTextLine{text: text, ref: ref, line: lineNumber, streamStart: start, streamEnd: end})
		}
	}
	tokens := uint64(0)
	for {
		parser.skip()
		if parser.pos >= len(data) {
			break
		}
		if tokens >= config.MaxContentTokens {
			return nil, ErrPDFBudget
		}
		tokens++
		ch := data[parser.pos]
		if isPDFOperatorStart(ch) {
			operator := readPDFOperator(data, &parser.pos)
			switch operator {
			case "BT":
				if insideText {
					return nil, ErrPDFInvalid
				}
				insideText = true
			case "ET":
				if !insideText {
					return nil, ErrPDFInvalid
				}
				flush()
				insideText = false
				fontName = ""
			case "Tf":
				if insideText && len(operands) >= 2 && operands[len(operands)-2].kind == pdfName {
					fontName = operands[len(operands)-2].name
				}
			case "Td", "TD", "Tm", "T*":
				if insideText {
					flush()
				}
			case "Tj", "'":
				if !insideText || len(operands) == 0 {
					return nil, ErrPDFInvalid
				}
				text, err := pdfDecodeTextValue(operands[len(operands)-1], fonts[fontName])
				if err != nil {
					return nil, err
				}
				current.WriteString(text)
				if operator == "'" {
					flush()
				}
			case "\"":
				if !insideText || len(operands) == 0 {
					return nil, ErrPDFInvalid
				}
				text, err := pdfDecodeTextValue(operands[len(operands)-1], fonts[fontName])
				if err != nil {
					return nil, err
				}
				current.WriteString(text)
				flush()
			case "TJ":
				if !insideText || len(operands) == 0 || operands[len(operands)-1].kind != pdfArray {
					return nil, ErrPDFInvalid
				}
				for _, item := range operands[len(operands)-1].arr {
					if item.kind == pdfNumber {
						if item.num < -100 && current.Len() > 0 {
							current.WriteByte(' ')
						}
						continue
					}
					text, err := pdfDecodeTextValue(item, fonts[fontName])
					if err != nil {
						return nil, err
					}
					current.WriteString(text)
				}
			default:
				if insideText {
					return nil, ErrPDFUnsupported
				}
			}
			operands = operands[:0]
			continue
		}
		value, err := parser.parse()
		if err != nil {
			return nil, ErrPDFUnsupported
		}
		operands = append(operands, value)
		if len(operands) > 64 {
			return nil, ErrPDFBudget
		}
	}
	if insideText {
		return nil, ErrPDFInvalid
	}
	flush()
	return lines, nil
}

func isPDFOperatorStart(ch byte) bool {
	return (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') || ch == '\'' || ch == '"'
}

func readPDFOperator(data []byte, pos *int) string {
	start := *pos
	if data[*pos] == '\'' || data[*pos] == '"' {
		*pos++
		return string(data[start:*pos])
	}
	for *pos < len(data) && data[*pos] >= 'A' && data[*pos] <= 'z' {
		*pos++
	}
	return string(data[start:*pos])
}

func pdfDecodeTextValue(value pdfValue, font pdfFont) (string, error) {
	if value.kind != pdfString && value.kind != pdfHex {
		return "", ErrPDFInvalid
	}
	data := []byte(value.text)
	if font.type0 {
		if font.cmap.codeLength == 0 || len(data)%font.cmap.codeLength != 0 {
			return "", ErrPDFUnsupported
		}
		var out strings.Builder
		for pos := 0; pos < len(data); pos += font.cmap.codeLength {
			text, ok := font.cmap.values[string(data[pos:pos+font.cmap.codeLength])]
			if !ok {
				return "", ErrPDFUnsupported
			}
			out.WriteString(text)
		}
		return out.String(), nil
	}
	if font.encoding != "" && font.encoding != "WinAnsiEncoding" && font.encoding != "StandardEncoding" {
		return "", ErrPDFUnsupported
	}
	var out strings.Builder
	for _, ch := range data {
		out.WriteRune(winAnsiRune(ch))
	}
	return out.String(), nil
}

func winAnsiRune(ch byte) rune {
	if ch >= 0x80 && ch <= 0x9f {
		const table = "€‚ƒ„…†‡ˆ‰Š‹ŒŽ‘‘“”•–—˜™š›œžŸ"
		runes := []rune(table)
		if int(ch-0x80) < len(runes) {
			return runes[ch-0x80]
		}
	}
	return rune(ch)
}

func parsePDFCMap(data []byte) (pdfCMap, error) {
	text := string(data)
	cmap := pdfCMap{values: make(map[string]string)}
	fields := strings.Fields(text)
	for index := 0; index < len(fields); index++ {
		if fields[index] == "beginbfchar" {
			for index++; index < len(fields) && fields[index] != "endbfchar"; index += 2 {
				if index+1 >= len(fields) {
					return pdfCMap{}, ErrPDFInvalid
				}
				source, ok := cmapHex(fields[index])
				if !ok {
					return pdfCMap{}, ErrPDFInvalid
				}
				destination, ok := cmapHex(fields[index+1])
				if !ok {
					return pdfCMap{}, ErrPDFInvalid
				}
				if cmap.codeLength == 0 {
					cmap.codeLength = len(source)
				}
				if len(source) != cmap.codeLength {
					return pdfCMap{}, ErrPDFUnsupported
				}
				cmap.values[string(source)] = cmapUTF16(destination)
			}
		}
		if fields[index] == "beginbfrange" {
			for index++; index < len(fields) && fields[index] != "endbfrange"; index += 3 {
				if index+2 >= len(fields) {
					return pdfCMap{}, ErrPDFInvalid
				}
				from, ok1 := cmapHex(fields[index])
				to, ok2 := cmapHex(fields[index+1])
				dest, ok3 := cmapHex(fields[index+2])
				if !ok1 || !ok2 || !ok3 || len(from) != len(to) {
					return pdfCMap{}, ErrPDFInvalid
				}
				if cmap.codeLength == 0 {
					cmap.codeLength = len(from)
				}
				if len(from) != cmap.codeLength {
					return pdfCMap{}, ErrPDFUnsupported
				}
				start := binary.BigEndian.Uint16(padCMapCode(from))
				stop := binary.BigEndian.Uint16(padCMapCode(to))
				if stop < start {
					return pdfCMap{}, ErrPDFInvalid
				}
				base := binary.BigEndian.Uint16(padCMapCode(dest))
				for code := start; code <= stop; code++ {
					source := make([]byte, len(from))
					if len(source) == 1 {
						source[0] = byte(code)
					} else {
						binary.BigEndian.PutUint16(source, code)
					}
					destination := make([]byte, len(dest))
					if len(destination) == 1 {
						destination[0] = byte(base + code - start)
					} else {
						binary.BigEndian.PutUint16(destination, base+code-start)
					}
					cmap.values[string(source)] = cmapUTF16(destination)
				}
			}
		}
	}
	if cmap.codeLength == 0 || len(cmap.values) == 0 {
		return pdfCMap{}, ErrPDFUnsupported
	}
	return cmap, nil
}

func cmapHex(value string) ([]byte, bool) {
	if len(value) < 2 || value[0] != '<' || value[len(value)-1] != '>' {
		return nil, false
	}
	encoded := value[1 : len(value)-1]
	if len(encoded)%2 != 0 {
		encoded += "0"
	}
	decoded := make([]byte, len(encoded)/2)
	for i := range decoded {
		hi, ok1 := pdfHexNibble(encoded[i*2])
		lo, ok2 := pdfHexNibble(encoded[i*2+1])
		if !ok1 || !ok2 {
			return nil, false
		}
		decoded[i] = hi<<4 | lo
	}
	return decoded, true
}

func padCMapCode(value []byte) []byte {
	if len(value) == 1 {
		return []byte{0, value[0]}
	}
	return value
}

func cmapUTF16(value []byte) string {
	if len(value)%2 != 0 {
		return string(value)
	}
	units := make([]uint16, len(value)/2)
	for index := range units {
		units[index] = binary.BigEndian.Uint16(value[index*2:])
	}
	return string(utf16.Decode(units))
}
