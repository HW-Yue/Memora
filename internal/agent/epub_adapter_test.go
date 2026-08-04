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

func TestEPUBAdapterPreservesSpineTOCTableFootnoteAndResources(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestEPUB(t, validEPUB3Entries())
	putSource(t, store, "job-epub", "source-epub", payload)
	adapter, err := agent.NewEPUBAdapter(store, agent.DefaultEPUBAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	document, receipt, err := adapter.Parse(context.Background(), "job-epub", "source-epub")
	if err != nil {
		t.Fatal(err)
	}
	if document.Validate() != nil || receipt.Validate() != nil || receipt.IRSHA256 != document.SHA256 ||
		receipt.OPFPath != "OPS/package.opf" || receipt.SpineItems != 2 || receipt.Resources != 4 ||
		receipt.Footnotes != 1 {
		t.Fatalf("document Validate=%v receipt=%#v Validate=%v", document.Validate(), receipt, receipt.Validate())
	}
	var chapters []string
	kindCounts := make(map[agent.DocumentNodeKind]int)
	for _, node := range document.Nodes {
		kindCounts[node.Kind]++
		if node.Kind == agent.DocumentNodeChapter {
			chapters = append(chapters, node.Label)
		}
		resource := findDocumentResource(t, document, node.Anchor.ResourceID)
		if node.Anchor.End > resource.Bytes || node.Anchor.Start >= node.Anchor.End {
			t.Fatalf("node anchor = %#v resource = %#v", node.Anchor, resource)
		}
	}
	if !reflect.DeepEqual(chapters, []string{"Second Chapter", "First Chapter"}) {
		t.Fatalf("chapter order = %v", chapters)
	}
	if kindCounts[agent.DocumentNodeTable] != 1 || kindCounts[agent.DocumentNodeTableRow] != 1 ||
		kindCounts[agent.DocumentNodeTableCell] != 2 || kindCounts[agent.DocumentNodeFootnote] != 1 ||
		kindCounts[agent.DocumentNodeImage] != 1 {
		t.Fatalf("kind counts = %#v", kindCounts)
	}
	if len(document.References) != 1 || document.References[0].Kind != agent.DocumentReferenceFootnote {
		t.Fatalf("references = %#v", document.References)
	}
	if !documentReadingContainsText(document, "Second heading", "Before middle after 1 tail", "Metric", "42",
		"Footnote detail", "First heading", "First item", "Second item") {
		t.Fatalf("reading order/text = %#v", document.ReadingOrder)
	}
	second, secondReceipt, err := adapter.Parse(context.Background(), "job-epub", "source-epub")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, document) || !reflect.DeepEqual(secondReceipt, receipt) {
		t.Fatalf("second parse differs: %#v/%#v", second, secondReceipt)
	}
}

func TestEPUBAdapterUsesNCXFallbackWithoutChangingSpine(t *testing.T) {
	t.Parallel()

	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestEPUB(t, validEPUB2Entries())
	putSource(t, store, "job-epub2", "source-epub2", payload)
	adapter, err := agent.NewEPUBAdapter(store, agent.DefaultEPUBAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	document, receipt, err := adapter.Parse(context.Background(), "job-epub2", "source-epub2")
	if err != nil {
		t.Fatal(err)
	}
	var chapterLabels []string
	for _, node := range document.Nodes {
		if node.Kind == agent.DocumentNodeChapter {
			chapterLabels = append(chapterLabels, node.Label)
		}
	}
	if !reflect.DeepEqual(chapterLabels, []string{"Legacy Chapter"}) || receipt.SpineItems != 1 ||
		receipt.Resources != 2 {
		t.Fatalf("labels=%v receipt=%#v", chapterLabels, receipt)
	}
}

func TestEPUBAdapterRejectsMalformedAndBudgetedCorpus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func([]epubTestEntry) []epubTestEntry
		config func() agent.EPUBAdapterConfig
		want   error
	}{
		{name: "wrong mimetype", mutate: func(entries []epubTestEntry) []epubTestEntry {
			entries[0].body = "application/zip"
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "compressed mimetype", mutate: func(entries []epubTestEntry) []epubTestEntry {
			entries[0].method = zip.Deflate
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "missing container", mutate: func(entries []epubTestEntry) []epubTestEntry {
			return append(entries[:1], entries[2:]...)
		}, want: agent.ErrEPUBInvalid},
		{name: "manifest traversal", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/package.opf", `href="ch1.xhtml"`, `href="../ch1.xhtml"`)
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "navigation traversal", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/nav.xhtml", `href="ch1.xhtml"`, `href="../ch1.xhtml"`)
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "unsupported package version", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/package.opf", `version="3.0"`, `version="9.0"`)
			return entries
		}, want: agent.ErrEPUBUnsupported},
		{name: "duplicate manifest id", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/package.opf", `id="c2"`, `id="c1"`)
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "missing spine item", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/package.opf", `idref="c2"`, `idref="missing"`)
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "malformed xhtml", mutate: func(entries []epubTestEntry) []epubTestEntry {
			replaceEPUBEntry(t, entries, "OPS/ch2.xhtml", `</body>`, `<p>unclosed</body>`)
			return entries
		}, want: agent.ErrEPUBInvalid},
		{name: "entry budget", mutate: func(entries []epubTestEntry) []epubTestEntry { return entries }, config: func() agent.EPUBAdapterConfig {
			config := agent.DefaultEPUBAdapterConfig()
			config.MaxEntries = 3
			return config
		}, want: agent.ErrEPUBBudget},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			entries := test.mutate(cloneEPUBEntries(validEPUB3Entries()))
			payload := buildTestEPUB(t, entries)
			config := agent.DefaultEPUBAdapterConfig()
			if test.config != nil {
				config = test.config()
			}
			store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
			if err != nil {
				t.Fatal(err)
			}
			putSource(t, store, "job-invalid", "source-invalid", payload)
			adapter, err := agent.NewEPUBAdapter(store, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := adapter.Parse(context.Background(), "job-invalid", "source-invalid"); !errors.Is(err, test.want) {
				t.Fatalf("Parse error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEPUBAdapterHonorsCancellationAndSourceCorruption(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestEPUB(t, validEPUB3Entries())
	putSource(t, store, "job-cancel", "source-cancel", payload)
	adapter, err := agent.NewEPUBAdapter(store, agent.DefaultEPUBAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := adapter.Parse(ctx, "job-cancel", "source-cancel"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Parse error = %v", err)
	}
	if err := os.WriteFile(onlySourceObjectPath(t, root), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-cancel", "source-cancel"); !errors.Is(err, agent.ErrSourceStoreCorrupt) {
		t.Fatalf("corrupt Source Parse error = %v", err)
	}
}

func TestEPUBAdapterClosesSourceAndPropagatesCloseFailure(t *testing.T) {
	t.Parallel()

	payload := buildTestEPUB(t, validEPUB3Entries())
	closeFailure := errors.New("close source failed")
	random := &trackingEPUBRandomAccess{Reader: bytes.NewReader(payload), closeErr: closeFailure}
	source := &trackingEPUBSource{
		reference: agent.SourceReference{
			Version: agent.SourceReferenceVersion, JobID: "job-close", SourceID: "source-close",
			SHA256: sourceDigest(payload), Bytes: uint64(len(payload)),
			MediaType: "application/epub+zip", FileName: "book.epub",
		},
		reader: random,
	}
	adapter, err := agent.NewEPUBAdapter(source, agent.DefaultEPUBAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-close", "source-close"); !errors.Is(err, closeFailure) {
		t.Fatalf("Parse close error = %v", err)
	}
	if !random.closed {
		t.Fatal("EPUB source was not closed")
	}
}

type epubTestEntry struct {
	name   string
	body   string
	method uint16
}

type trackingEPUBSource struct {
	reference agent.SourceReference
	reader    agent.SourceRandomAccess
}

func (source *trackingEPUBSource) OpenRandomAccess(
	jobID string,
	sourceID string,
) (agent.SourceReference, agent.SourceRandomAccess, error) {
	return source.reference, source.reader, nil
}

type trackingEPUBRandomAccess struct {
	*bytes.Reader
	closed   bool
	closeErr error
}

func (reader *trackingEPUBRandomAccess) Close() error {
	reader.closed = true
	return reader.closeErr
}

func validEPUB3Entries() []epubTestEntry {
	return []epubTestEntry{
		{name: "mimetype", body: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", body: `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>
<rootfile full-path="OPS/package.opf" media-type="application/oebps-package+xml"/>
</rootfiles></container>`, method: zip.Deflate},
		{name: "OPS/package.opf", body: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Structured EPUB</dc:title><dc:language>en</dc:language></metadata>
<manifest>
<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
<item id="c1" href="ch1.xhtml" media-type="application/xhtml+xml"/>
<item id="c2" href="ch2.xhtml" media-type="application/xhtml+xml"/>
<item id="img" href="image.png" media-type="image/png"/>
</manifest><spine><itemref idref="c2"/><itemref idref="c1"/></spine></package>`, method: zip.Deflate},
		{name: "OPS/ch1.xhtml", body: `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>First heading</h1>
<ul><li>First item</li><li>Second item</li></ul><img src="image.png" alt="Diagram"/>
</body></html>`, method: zip.Deflate},
		{name: "OPS/image.png", body: "PNG-fixture-bytes", method: zip.Deflate},
		{name: "OPS/nav.xhtml", body: `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body>
<nav epub:type="toc"><ol><li><a href="ch2.xhtml">Second Chapter</a></li><li><a href="ch1.xhtml">First Chapter</a></li></ol></nav>
</body></html>`, method: zip.Deflate},
		{name: "OPS/ch2.xhtml", body: `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><body>
<h1 id="second">Second heading</h1><p>Before <em>middle</em> after <a epub:type="noteref" href="#fn1">1</a> tail</p>
<table><tr><td>Metric</td><td>42</td></tr></table>
<aside epub:type="footnote" id="fn1"><p>Footnote detail</p></aside>
</body></html>`, method: zip.Deflate},
	}
}

func validEPUB2Entries() []epubTestEntry {
	return []epubTestEntry{
		{name: "mimetype", body: "application/epub+zip", method: zip.Store},
		{name: "META-INF/container.xml", body: `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles>
<rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/>
</rootfiles></container>`, method: zip.Deflate},
		{name: "OEBPS/content.opf", body: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="2.0">
<metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Legacy EPUB</dc:title><dc:language>en</dc:language></metadata>
<manifest><item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>
<item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest>
<spine toc="ncx"><itemref idref="chapter"/></spine></package>`, method: zip.Deflate},
		{name: "OEBPS/chapter.xhtml", body: `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml"><body><h1>Legacy heading</h1><p>Legacy paragraph</p></body></html>`, method: zip.Deflate},
		{name: "OEBPS/toc.ncx", body: `<?xml version="1.0"?>
<ncx xmlns="http://www.daisy.org/z3986/2005/ncx/"><navMap><navPoint>
<navLabel><text>Legacy Chapter</text></navLabel><content src="chapter.xhtml"/>
</navPoint></navMap></ncx>`, method: zip.Deflate},
	}
}

func buildTestEPUB(t *testing.T, entries []epubTestEntry) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: entry.method}
		target, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := target.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func cloneEPUBEntries(entries []epubTestEntry) []epubTestEntry {
	return append([]epubTestEntry(nil), entries...)
}

func replaceEPUBEntry(t *testing.T, entries []epubTestEntry, name, old, replacement string) {
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
	t.Fatalf("missing EPUB entry %s", name)
}

func findDocumentResource(t *testing.T, document agent.DocumentIR, resourceID string) agent.DocumentResource {
	t.Helper()
	for _, resource := range document.Resources {
		if resource.ID == resourceID {
			return resource
		}
	}
	t.Fatalf("missing resource %s", resourceID)
	return agent.DocumentResource{}
}

func documentReadingContainsText(document agent.DocumentIR, values ...string) bool {
	nodes := make(map[string]agent.DocumentNode, len(document.Nodes))
	for _, node := range document.Nodes {
		nodes[node.ID] = node
	}
	var text []string
	for _, nodeID := range document.ReadingOrder {
		text = append(text, nodes[nodeID].Text)
	}
	joined := strings.Join(text, " | ")
	for _, value := range values {
		if !strings.Contains(joined, value) {
			return false
		}
	}
	return true
}
