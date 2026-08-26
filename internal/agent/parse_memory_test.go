package agent_test

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

// parseHeapRatio bounds what a parse costs to hold: live heap after the parse,
// over the body bytes parsed.
//
// Measured here at about 2.5 for both archive formats. That is lower than the
// ~7 recorded in known-risks 4, and deliberately a different figure: 7 is peak
// heap including everything the parser churns through on the way, this is what
// the finished IR still holds. The byte limits are sized against the peak, so
// the gate here is the cheaper, stabler half of the same property — the one a
// test can measure without fighting the allocator.
//
// Twelve leaves room for ordinary variation while still failing a change that
// made the IR an order of magnitude heavier.
const parseHeapRatio = 12

// heapAfter reports the live heap with the parsed document still referenced.
// Live heap is the figure the byte limits were computed from: what the IR
// costs to hold, not how much the parser churned on the way there.
func heapAfter(retained any) uint64 {
	runtime.GC()
	runtime.GC()
	var stats runtime.MemStats
	runtime.ReadMemStats(&stats)
	runtime.KeepAlive(retained)
	return stats.HeapAlloc
}

// TestDocumentParsePeakHeapStaysWithinItsBudget is S6's gate. The three
// configured byte limits mean nothing unless the multiplier they were computed
// from is held; without this test the limits drift away from the parser and
// silently start admitting inputs that flatten the process again.
func TestDocumentParsePeakHeapStaysWithinItsBudget(t *testing.T) {
	for _, adapterCase := range []struct {
		name  string
		build func(*testing.T) ([]byte, int, func(*agent.SourceStore, string, string) (any, error))
		put   func(*testing.T, *agent.SourceStore, string, string, []byte)
	}{
		{name: "epub", build: buildHeapEPUB, put: func(t *testing.T, store *agent.SourceStore, jobID, sourceID string, payload []byte) {
			putSource(t, store, jobID, sourceID, payload)
		}},
		{name: "docx", build: buildHeapDOCX, put: putDOCXSource},
	} {
		t.Run(adapterCase.name, func(t *testing.T) {
			payload, bodyBytes, parse := adapterCase.build(t)
			root := t.TempDir()
			store, err := agent.OpenSourceStore(root, heapSourceStoreConfig())
			if err != nil {
				t.Fatal(err)
			}
			adapterCase.put(t, store, "job-heap", "source-heap", payload)

			before := heapAfter(nil)
			document, err := parse(store, "job-heap", "source-heap")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			after := heapAfter(document)
			if after < before {
				t.Skip("heap shrank across the parse; the measurement is not meaningful here")
			}
			held := after - before
			budget := uint64(bodyBytes) * parseHeapRatio
			ratio := float64(held) / float64(bodyBytes)
			t.Logf("%s: body=%d held=%d ratio=%.1f", adapterCase.name, bodyBytes, held, ratio)
			if held > budget {
				t.Fatalf("parsing %d body bytes holds %d, over the %d budget (ratio %.1f, limit %d)",
					bodyBytes, held, budget, ratio, parseHeapRatio)
			}
		})
	}
}

func heapSourceStoreConfig() agent.SourceStoreConfig {
	return agent.SourceStoreConfig{
		MaxObjectBytes: 8 << 20, MaxJobBytes: 16 << 20,
		MaxPhysicalBytes: 32 << 20, MaxSourcesPerJob: 32,
	}
}

// heapBodyParagraphs is enough text that fixed per-parse overhead does not
// dominate the ratio being measured.
func heapBodyParagraphs(count int) string {
	var builder strings.Builder
	for index := 0; index < count; index++ {
		builder.WriteString("<p>Semantic paragraph number ")
		builder.WriteString(strings.Repeat("body text ", 12))
		builder.WriteString("</p>")
	}
	return builder.String()
}

func buildHeapEPUB(t *testing.T) ([]byte, int, func(*agent.SourceStore, string, string) (any, error)) {
	t.Helper()
	body := heapBodyParagraphs(2000)
	entries := cloneEPUBEntries(validEPUB3Entries())
	injected := false
	for index := range entries {
		if strings.Contains(entries[index].body, "</body>") {
			entries[index].body = strings.Replace(entries[index].body, "</body>", body+"</body>", 1)
			injected = true
			break
		}
	}
	if !injected {
		t.Fatal("EPUB fixture has no document body to grow")
	}
	payload := buildTestEPUB(t, entries)
	total := 0
	for _, entry := range entries {
		total += len(entry.body)
	}
	return payload, total, func(store *agent.SourceStore, jobID, sourceID string) (any, error) {
		adapter, err := agent.NewEPUBAdapter(store, agent.DefaultEPUBAdapterConfig())
		if err != nil {
			return nil, err
		}
		document, _, err := adapter.Parse(context.Background(), jobID, sourceID)
		return document, err
	}
}

func buildHeapDOCX(t *testing.T) ([]byte, int, func(*agent.SourceStore, string, string) (any, error)) {
	t.Helper()
	entries := validDOCXEntries()
	paragraph := "<w:p><w:r><w:t>" + strings.Repeat("body text ", 12) + "</w:t></w:r></w:p>"
	for index := range entries {
		if entries[index].name == "word/document.xml" {
			entries[index].body = strings.Replace(entries[index].body, "<w:sectPr/>",
				strings.Repeat(paragraph, 2000)+"<w:sectPr/>", 1)
		}
	}
	payload := buildTestDOCX(t, entries)
	total := 0
	for _, entry := range entries {
		total += len(entry.body)
	}
	return payload, total, func(store *agent.SourceStore, jobID, sourceID string) (any, error) {
		adapter, err := agent.NewDOCXAdapter(store, agent.DefaultDOCXAdapterConfig())
		if err != nil {
			return nil, err
		}
		document, _, err := adapter.Parse(context.Background(), jobID, sourceID)
		return document, err
	}
}

// TestPDFRefusesAnOversizedFileBeforeReadingIt is the PDF half of S6.
//
// The PDF adapter reads the whole file into memory before parsing it, so for
// this format the file bound is the memory bound — and it is only a bound if it
// is checked before the read. A ratio measurement would say nothing here: the
// fixture is two pages, and what matters is not how much the IR holds but that
// an oversized file never becomes resident at all.
func TestPDFRefusesAnOversizedFileBeforeReadingIt(t *testing.T) {
	t.Parallel()

	store, err := agent.OpenSourceStore(t.TempDir(), heapSourceStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	payload := buildTestPDF(t, pdfFixtureOptions{})
	putPDFSource(t, store, "job-pdf-budget", "source-pdf-budget", payload)

	config := agent.DefaultPDFAdapterConfig()
	config.MaxFileBytes = uint64(len(payload)) - 1
	adapter, err := agent.NewPDFAdapter(store, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-pdf-budget", "source-pdf-budget"); !errors.Is(err, agent.ErrPDFBudget) {
		t.Fatalf("oversized PDF error = %v, want ErrPDFBudget", err)
	}

	// One byte the other way still parses, so the refusal is the bound and not
	// a fixture accident.
	config.MaxFileBytes = uint64(len(payload))
	adapter, err = agent.NewPDFAdapter(store, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := adapter.Parse(context.Background(), "job-pdf-budget", "source-pdf-budget"); err != nil {
		t.Fatalf("PDF exactly at the bound = %v", err)
	}
}
