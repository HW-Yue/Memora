package agent_test

import (
	"bytes"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestPDFAdapterPreservesPageTreeTextOperatorsUnicodeAndAnchors(t *testing.T) {
	t.Parallel()
	store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestPDF(t, pdfFixtureOptions{})
	putPDFSource(t, store, "job-pdf", "source-pdf", payload)
	adapter, err := agent.NewPDFAdapter(store, agent.DefaultPDFAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	document, receipt, err := adapter.Parse(context.Background(), "job-pdf", "source-pdf")
	if err != nil {
		t.Fatal(err)
	}
	if document.Validate() != nil || receipt.Validate() != nil || receipt.IRSHA256 != document.SHA256 ||
		receipt.Pages != 2 || receipt.TextPages != 2 || receipt.Paragraphs != 4 || receipt.Glyphs == 0 ||
		len(document.Resources) != 1 || document.Resources[0].MediaType != "application/pdf" {
		t.Fatalf("document Validate=%v receipt=%#v Validate=%v", document.Validate(), receipt, receipt.Validate())
	}
	var pages []string
	for _, node := range document.Nodes {
		if node.Kind == agent.DocumentNodePart {
			pages = append(pages, node.Label)
		}
		resource := findDocumentResource(t, document, node.Anchor.ResourceID)
		if node.Anchor.Start >= node.Anchor.End || node.Anchor.End > resource.Bytes {
			t.Fatalf("node anchor = %#v resource = %#v", node.Anchor, resource)
		}
	}
	if !reflect.DeepEqual(pages, []string{"Page 1", "Page 2"}) ||
		!documentReadingContainsText(document, "Second page", "你好", "First page", "Metric 42") {
		t.Fatalf("pages=%v reading=%#v nodes=%#v", pages, document.ReadingOrder, document.Nodes)
	}
	for _, node := range document.Nodes {
		if node.Kind == agent.DocumentNodeParagraph && !strings.HasPrefix(node.Anchor.Selector, "pdf:page=") {
			t.Fatalf("paragraph selector = %q", node.Anchor.Selector)
		}
	}
	second, secondReceipt, err := adapter.Parse(context.Background(), "job-pdf", "source-pdf")
	if err != nil || !reflect.DeepEqual(second, document) || !reflect.DeepEqual(secondReceipt, receipt) {
		t.Fatalf("second parse = %#v / %#v / %v", second, secondReceipt, err)
	}
}

func TestPDFAdapterRejectsUnsupportedMalformedScannedAndBudgetedFiles(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		options pdfFixtureOptions
		payload func(*testing.T) []byte
		config  func() agent.PDFAdapterConfig
		want    error
	}{
		{name: "encrypted", options: pdfFixtureOptions{encrypted: true}, want: agent.ErrPDFUnsupported},
		{name: "incremental prev", options: pdfFixtureOptions{previousXRef: true}, want: agent.ErrPDFUnsupported},
		{name: "xref stream", payload: buildXRefStreamPDF, want: agent.ErrPDFUnsupported},
		{name: "missing pages ref", options: pdfFixtureOptions{missingPages: true}, want: agent.ErrPDFInvalid},
		{name: "page tree cycle", options: pdfFixtureOptions{pageCycle: true}, want: agent.ErrPDFInvalid},
		{name: "unsupported filter", options: pdfFixtureOptions{unsupportedFilter: true}, want: agent.ErrPDFUnsupported},
		{name: "Type0 without ToUnicode", options: pdfFixtureOptions{missingToUnicode: true}, want: agent.ErrPDFUnsupported},
		{name: "unknown text operator", options: pdfFixtureOptions{unknownTextOperator: true}, want: agent.ErrPDFUnsupported},
		{name: "scanned no text", options: pdfFixtureOptions{noText: true}, want: agent.ErrPDFTextLayerRequired},
		{name: "page budget", options: pdfFixtureOptions{}, config: func() agent.PDFAdapterConfig {
			config := agent.DefaultPDFAdapterConfig()
			config.MaxPages = 1
			return config
		}, want: agent.ErrPDFBudget},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			payload := buildTestPDF(t, test.options)
			if test.payload != nil {
				payload = test.payload(t)
			}
			store, err := agent.OpenSourceStore(t.TempDir(), testSourceStoreConfig())
			if err != nil {
				t.Fatal(err)
			}
			putPDFSource(t, store, "job-invalid-pdf", "source-invalid-pdf", payload)
			config := agent.DefaultPDFAdapterConfig()
			if test.config != nil {
				config = test.config()
			}
			adapter, err := agent.NewPDFAdapter(store, config)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := adapter.Parse(context.Background(), "job-invalid-pdf", "source-invalid-pdf"); !errors.Is(err, test.want) {
				t.Fatalf("Parse error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPDFAdapterPropagatesCancellationCorruptionAndCloseFailure(t *testing.T) {
	t.Parallel()
	payload := buildTestPDF(t, pdfFixtureOptions{})
	closeFailure := errors.New("close PDF source")
	random := &trackingEPUBRandomAccess{Reader: bytes.NewReader(payload), closeErr: closeFailure}
	source := &trackingEPUBSource{reference: agent.SourceReference{
		Version: agent.SourceReferenceVersion, JobID: "job-close-pdf", SourceID: "source-close-pdf",
		SHA256: sourceDigest(payload), Bytes: uint64(len(payload)), MediaType: "application/pdf", FileName: "book.pdf",
	}, reader: random}
	adapter, err := agent.NewPDFAdapter(source, agent.DefaultPDFAdapterConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-close-pdf", "source-close-pdf"); !errors.Is(err, closeFailure) || !random.closed {
		t.Fatalf("close error=%v closed=%v", err, random.closed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := adapter.Parse(ctx, "job-close-pdf", "source-close-pdf"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Parse error = %v", err)
	}

	root := t.TempDir()
	store, err := agent.OpenSourceStore(root, testSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	putPDFSource(t, store, "job-corrupt-pdf", "source-corrupt-pdf", payload)
	if err := os.WriteFile(onlySourceObjectPath(t, root), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	adapter, _ = agent.NewPDFAdapter(store, agent.DefaultPDFAdapterConfig())
	if _, _, err := adapter.Parse(context.Background(), "job-corrupt-pdf", "source-corrupt-pdf"); !errors.Is(err, agent.ErrSourceStoreCorrupt) {
		t.Fatalf("corrupt source error = %v", err)
	}
}

type pdfFixtureOptions struct {
	encrypted           bool
	previousXRef        bool
	missingPages        bool
	pageCycle           bool
	unsupportedFilter   bool
	missingToUnicode    bool
	unknownTextOperator bool
	noText              bool
}

func buildTestPDF(t *testing.T, options pdfFixtureOptions) []byte {
	t.Helper()
	secondContent := "BT /F1 12 Tf 72 720 Td (Second page) Tj ET"
	if options.unknownTextOperator {
		secondContent = "BT /F1 12 Tf 72 720 Td (Second page) ZZ ET"
	}
	if options.noText {
		secondContent = "q 1 0 0 1 0 0 cm Q"
	}
	filter := "/Filter /FlateDecode "
	secondBytes := deflatePDF(t, []byte(secondContent))
	if options.unsupportedFilter {
		filter = "/Filter /LZWDecode "
		secondBytes = []byte(secondContent)
	}
	type0 := "<< /Type /Font /Subtype /Type0 /BaseFont /FixtureCID /Encoding /Identity-H /DescendantFonts [11 0 R] /ToUnicode 12 0 R >>"
	if options.missingToUnicode {
		type0 = strings.Replace(type0, " /ToUnicode 12 0 R", "", 1)
	}
	objects := map[int][]byte{
		1:  []byte("<< /Type /Catalog /Pages 2 0 R /Lang (en) >>"),
		2:  []byte("<< /Type /Pages /Kids [4 0 R 3 0 R] /Count 2 >>"),
		3:  []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 7 0 R >> >> /Contents 5 0 R >>"),
		4:  []byte("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 7 0 R /F2 10 0 R >> >> /Contents [6 0 R 9 0 R] >>"),
		5:  pdfStreamObject(nil, []byte("BT /F1 12 Tf 72 720 Td (First page) Tj 0 -20 Td [(Metric) -200 (42)] TJ ET")),
		6:  pdfStreamObject([]byte(filter), secondBytes),
		7:  []byte("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"),
		8:  []byte("<< /Title (Structured PDF) >>"),
		9:  pdfStreamObject(nil, []byte("BT /F2 12 Tf 72 700 Td <00010002> Tj ET")),
		10: []byte(type0),
		11: []byte("<< /Type /Font /Subtype /CIDFontType2 /BaseFont /FixtureCID /CIDSystemInfo << /Registry (Adobe) /Ordering (Identity) /Supplement 0 >> >>"),
		12: pdfStreamObject(nil, []byte("/CIDInit /ProcSet findresource begin 12 dict begin begincmap 2 beginbfchar <0001> <4F60> <0002> <597D> endbfchar endcmap end end")),
	}
	if options.encrypted {
		objects[13] = []byte("<< /Filter /Standard >>")
	}
	if options.missingPages {
		objects[1] = []byte("<< /Type /Catalog /Pages 99 0 R /Lang (en) >>")
	}
	if options.pageCycle {
		objects[2] = []byte("<< /Type /Pages /Kids [2 0 R] /Count 1 >>")
	}
	return assembleClassicPDF(objects, []int{1, 7, 2, 8, 4, 6, 10, 11, 12, 9, 3, 5, 13}, options)
}

func assembleClassicPDF(objects map[int][]byte, order []int, options pdfFixtureOptions) []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n%\xE2\xE3\xCF\xD3\n")
	offsets := make(map[int]int)
	for _, number := range order {
		body, exists := objects[number]
		if !exists {
			continue
		}
		offsets[number] = output.Len()
		fmt.Fprintf(&output, "%d 0 obj\n", number)
		output.Write(body)
		output.WriteString("\nendobj\n")
	}
	xref := output.Len()
	maxObject := 0
	for number := range objects {
		if number > maxObject {
			maxObject = number
		}
	}
	fmt.Fprintf(&output, "xref\n0 %d\n0000000000 65535 f \n", maxObject+1)
	for number := 1; number <= maxObject; number++ {
		if offset, exists := offsets[number]; exists {
			fmt.Fprintf(&output, "%010d 00000 n \n", offset)
		} else {
			output.WriteString("0000000000 00000 f \n")
		}
	}
	fmt.Fprintf(&output, "trailer\n<< /Size %d /Root 1 0 R /Info 8 0 R", maxObject+1)
	if options.encrypted {
		output.WriteString(" /Encrypt 13 0 R")
	}
	if options.previousXRef {
		output.WriteString(" /Prev 1")
	}
	fmt.Fprintf(&output, " >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return output.Bytes()
}

func pdfStreamObject(prefix, content []byte) []byte {
	var output bytes.Buffer
	fmt.Fprintf(&output, "<< %s/Length %d >>\nstream\n", prefix, len(content))
	output.Write(content)
	output.WriteString("\nendstream")
	return output.Bytes()
}

func deflatePDF(t *testing.T, content []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	writer := zlib.NewWriter(&output)
	if _, err := writer.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func buildXRefStreamPDF(*testing.T) []byte {
	return []byte("%PDF-1.7\n1 0 obj\n<< /Type /XRef /Length 0 >>\nstream\n\nendstream\nendobj\nstartxref\n9\n%%EOF\n")
}

func putPDFSource(t *testing.T, store *agent.SourceStore, jobID, sourceID string, payload []byte) {
	t.Helper()
	_, err := store.Put(context.Background(), agent.SourcePutRequest{
		Version: agent.SourcePutRequestVersion, JobID: jobID, SourceID: sourceID,
		ExpectedSHA256: sourceDigest(payload), MediaType: "application/pdf", FileName: "book.pdf",
	}, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
}
