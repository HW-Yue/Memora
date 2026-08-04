package agent_test

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestDOCXAdapterPreservesHeadingListTableFootnoteRelationshipsAndResources(t *testing.T) {
	t.Parallel()
	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestDOCX(t, validDOCXEntries())
	putDOCXSource(t, store, "job-docx", "source-docx", payload)
	adapter, err := agent.NewDOCXAdapter(store, agent.DefaultDOCXAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	document, receipt, err := adapter.Parse(context.Background(), "job-docx", "source-docx")
	if err != nil {
		t.Fatal(err)
	}
	if document.Validate() != nil || receipt.Validate() != nil || receipt.IRSHA256 != document.SHA256 ||
		receipt.MainPart != "word/document.xml" || receipt.Resources != 8 || receipt.Paragraphs != 4 ||
		receipt.Tables != 1 || receipt.Footnotes != 1 {
		t.Fatalf("document Validate=%v receipt=%#v Validate=%v", document.Validate(), receipt, receipt.Validate())
	}
	kinds := make(map[agent.DocumentNodeKind]int)
	for _, node := range document.Nodes {
		kinds[node.Kind]++
		resource := findDocumentResource(t, document, node.Anchor.ResourceID)
		if node.Anchor.Start >= node.Anchor.End || node.Anchor.End > resource.Bytes {
			t.Fatalf("node anchor = %#v resource = %#v", node.Anchor, resource)
		}
	}
	if kinds[agent.DocumentNodeHeading] != 1 || kinds[agent.DocumentNodeParagraph] != 1 ||
		kinds[agent.DocumentNodeList] != 1 || kinds[agent.DocumentNodeListItem] != 2 ||
		kinds[agent.DocumentNodeTable] != 1 || kinds[agent.DocumentNodeTableRow] != 1 ||
		kinds[agent.DocumentNodeTableCell] != 2 || kinds[agent.DocumentNodeFootnote] != 1 ||
		kinds[agent.DocumentNodeImage] != 1 {
		t.Fatalf("node kinds = %#v", kinds)
	}
	if len(document.References) != 1 || document.References[0].Kind != agent.DocumentReferenceFootnote ||
		!documentReadingContainsText(document, "Overview", "Before after", "First item", "Second item",
			"Metric", "42", "Footnote detail") {
		t.Fatalf("references=%#v reading=%#v", document.References, document.ReadingOrder)
	}
	if !hasDOCXResource(document, "word/media/image.png", "image/png") {
		t.Fatalf("image resource missing: %#v", document.Resources)
	}
	second, secondReceipt, err := adapter.Parse(context.Background(), "job-docx", "source-docx")
	if err != nil || !reflect.DeepEqual(second, document) || !reflect.DeepEqual(secondReceipt, receipt) {
		t.Fatalf("second parse = %#v / %#v / %v", second, secondReceipt, err)
	}
}

func TestDOCXAdapterRejectsMalformedUnsupportedAndBudgetedPackages(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func([]docxTestEntry) []docxTestEntry
		config func() agent.DOCXAdapterConfig
		want   error
	}{
		{name: "root relationship traversal", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "_rels/.rels", `Target="word/document.xml"`, `Target="../word/document.xml"`)
			return entries
		}, want: agent.ErrDOCXInvalid},
		{name: "dangling image relationship", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "word/_rels/document.xml.rels", `Target="media/image.png"`, `Target="media/missing.png"`)
			return entries
		}, want: agent.ErrDOCXInvalid},
		{name: "dangling footnote", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "word/document.xml", `w:id="2"`, `w:id="99"`)
			return entries
		}, want: agent.ErrDOCXInvalid},
		{name: "empty table cell", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "word/document.xml", `<w:t>42</w:t>`, ``)
			return entries
		}, want: agent.ErrDOCXUnsupported},
		{name: "active alt chunk", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "word/document.xml", `<w:sectPr/>`, `<w:altChunk r:id="rIdHtml"/><w:sectPr/>`)
			return entries
		}, want: agent.ErrDOCXUnsupported},
		{name: "malformed XML", mutate: func(entries []docxTestEntry) []docxTestEntry {
			replaceDOCXEntry(t, entries, "word/document.xml", `</w:body>`, `<w:p></w:body>`)
			return entries
		}, want: agent.ErrDOCXInvalid},
		{name: "entry budget", mutate: func(entries []docxTestEntry) []docxTestEntry { return entries }, config: func() agent.DOCXAdapterConfig {
			config := agent.DefaultDOCXAdapterConfig()
			config.MaxEntries = 3
			return config
		}, want: agent.ErrDOCXBudget},
		{name: "XML token budget", mutate: func(entries []docxTestEntry) []docxTestEntry { return entries }, config: func() agent.DOCXAdapterConfig {
			config := agent.DefaultDOCXAdapterConfig()
			config.MaxXMLTokens = 3
			return config
		}, want: agent.ErrDOCXBudget},
		{name: "duplicate part", mutate: func(entries []docxTestEntry) []docxTestEntry {
			return append(entries, entries[len(entries)-1])
		}, want: agent.ErrDOCXInvalid},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
			if err != nil {
				t.Fatal(err)
			}
			payload := buildTestDOCX(t, test.mutate(cloneDOCXEntries(validDOCXEntries())))
			putDOCXSource(t, store, "job-invalid-docx", "source-invalid-docx", payload)
			config := agent.DefaultDOCXAdapterConfig()
			if test.config != nil {
				config = test.config()
			}
			adapter, err := agent.NewDOCXAdapter(store, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := adapter.Parse(context.Background(), "job-invalid-docx", "source-invalid-docx"); !errors.Is(err, test.want) {
				t.Fatalf("Parse error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDOCXAdapterPropagatesSourceCorruption(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestDOCX(t, validDOCXEntries())
	putDOCXSource(t, store, "job-corrupt-docx", "source-corrupt-docx", payload)
	if err := os.WriteFile(onlySourceObjectPath(t, root), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, err := agent.NewDOCXAdapter(store, agent.DefaultDOCXAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-corrupt-docx", "source-corrupt-docx"); !errors.Is(err, agent.ErrSourceStoreCorrupt) {
		t.Fatalf("corrupt source error = %v", err)
	}
}

func TestDOCXAdapterHonorsCancellationAndCloseFailure(t *testing.T) {
	t.Parallel()
	payload := buildTestDOCX(t, validDOCXEntries())
	closeFailure := errors.New("close DOCX source")
	random := &trackingEPUBRandomAccess{Reader: bytes.NewReader(payload), closeErr: closeFailure}
	source := &trackingEPUBSource{reference: agent.SourceReference{
		Version: agent.SourceReferenceVersion, JobID: "job-close-docx", SourceID: "source-close-docx",
		SHA256: sourceDigest(payload), Bytes: uint64(len(payload)),
		MediaType: docxMediaType, FileName: "book.docx",
	}, reader: random}
	adapter, err := agent.NewDOCXAdapter(source, agent.DefaultDOCXAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-close-docx", "source-close-docx"); !errors.Is(err, closeFailure) || !random.closed {
		t.Fatalf("close error=%v closed=%v", err, random.closed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := adapter.Parse(ctx, "job-close-docx", "source-close-docx"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Parse error = %v", err)
	}
}

func TestDOCXAdapterRejectsEncryptedPackage(t *testing.T) {
	t.Parallel()
	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := markDOCXEncrypted(buildTestDOCX(t, validDOCXEntries()))
	putDOCXSource(t, store, "job-encrypted-docx", "source-encrypted-docx", payload)
	adapter, err := agent.NewDOCXAdapter(store, agent.DefaultDOCXAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-encrypted-docx", "source-encrypted-docx"); !errors.Is(err, agent.ErrDOCXUnsupported) {
		t.Fatalf("encrypted package error = %v", err)
	}
}

const docxMediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type docxTestEntry struct {
	name string
	body string
}

func validDOCXEntries() []docxTestEntry {
	return []docxTestEntry{
		{name: "word/media/image.png", body: "PNG-fixture-bytes"},
		{name: "[Content_Types].xml", body: `<?xml version="1.0"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/><Default Extension="png" ContentType="image/png"/>
<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/>
<Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/>
<Override PartName="/word/footnotes.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.footnotes+xml"/>
<Override PartName="/docProps/core.xml" ContentType="application/vnd.openxmlformats-package.core-properties+xml"/>
</Types>`},
		{name: "_rels/.rels", body: `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rIdDocument" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/>
<Relationship Id="rIdCore" Type="http://schemas.openxmlformats.org/package/2006/relationships/metadata/core-properties" Target="docProps/core.xml"/>
</Relationships>`},
		{name: "docProps/core.xml", body: `<?xml version="1.0"?><cp:coreProperties xmlns:cp="http://schemas.openxmlformats.org/package/2006/metadata/core-properties" xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Structured DOCX</dc:title><dc:language>en</dc:language></cp:coreProperties>`},
		{name: "word/styles.xml", body: `<?xml version="1.0"?><w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:pPr><w:outlineLvl w:val="0"/></w:pPr></w:style></w:styles>`},
		{name: "word/footnotes.xml", body: `<?xml version="1.0"?><w:footnotes xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:footnote w:id="-1" w:type="separator"><w:p><w:r><w:separator/></w:r></w:p></w:footnote><w:footnote w:id="2"><w:p><w:r><w:t>Footnote detail</w:t></w:r></w:p></w:footnote></w:footnotes>`},
		{name: "word/_rels/document.xml.rels", body: `<?xml version="1.0"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rIdImage" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/image" Target="media/image.png"/><Relationship Id="rIdFootnotes" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/footnotes" Target="footnotes.xml"/><Relationship Id="rIdStyles" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{name: "word/document.xml", body: `<?xml version="1.0"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships" xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><w:body>
<w:p><w:pPr><w:pStyle w:val="Heading1"/></w:pPr><w:r><w:t>Overview</w:t></w:r></w:p>
<w:p><w:r><w:t>Before </w:t></w:r><w:r><w:footnoteReference w:id="2"/></w:r><w:r><w:t> after</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>First item</w:t></w:r></w:p>
<w:p><w:pPr><w:numPr><w:ilvl w:val="0"/><w:numId w:val="1"/></w:numPr></w:pPr><w:r><w:t>Second item</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>Metric</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>42</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
<w:p><w:r><w:drawing><a:blip r:embed="rIdImage"/><w:docPr descr="Diagram"/></w:drawing></w:r></w:p><w:sectPr/>
</w:body></w:document>`},
	}
}

func buildTestDOCX(t *testing.T, entries []docxTestEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		part, err := writer.Create(entry.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func putDOCXSource(t *testing.T, store *agent.SourceStore, jobID, sourceID string, payload []byte) {
	t.Helper()
	_, err := store.Put(context.Background(), agent.SourcePutRequest{
		Version: agent.SourcePutRequestVersion, JobID: jobID, SourceID: sourceID,
		ExpectedSHA256: sourceDigest(payload), MediaType: docxMediaType, FileName: "book.docx",
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
}

func cloneDOCXEntries(entries []docxTestEntry) []docxTestEntry {
	return append([]docxTestEntry(nil), entries...)
}

func replaceDOCXEntry(t *testing.T, entries []docxTestEntry, name, old, replacement string) {
	t.Helper()
	for index := range entries {
		if entries[index].name != name {
			continue
		}
		updated := strings.Replace(entries[index].body, old, replacement, 1)
		if updated == entries[index].body {
			t.Fatalf("%s does not contain %q", name, old)
		}
		entries[index].body = updated
		return
	}
	t.Fatalf("missing DOCX entry %s", name)
}

func hasDOCXResource(document agent.DocumentIR, locator, mediaType string) bool {
	for _, resource := range document.Resources {
		if resource.Locator == locator && resource.MediaType == mediaType {
			return true
		}
	}
	return false
}

func markDOCXEncrypted(payload []byte) []byte {
	result := append([]byte(nil), payload...)
	for index := 0; index+10 <= len(result); index++ {
		switch string(result[index : index+4]) {
		case "PK\x03\x04":
			result[index+6] |= 0x1
		case "PK\x01\x02":
			result[index+8] |= 0x1
		}
	}
	return result
}
