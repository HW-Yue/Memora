package agent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/agent"
)

func TestDocumentIRSealsStrictlyDecodesAndPreservesStructure(t *testing.T) {
	t.Parallel()

	draft := validDocumentIR(t)
	sealed, err := agent.SealDocumentIR(draft)
	if err != nil {
		t.Fatal(err)
	}
	if sealed.Validate() != nil || !validTestSHA256(sealed.SHA256) {
		t.Fatalf("sealed = %#v, Validate=%v", sealed, sealed.Validate())
	}
	encoded, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"chunk`, `"embedding`, `"vector`} {
		if bytes.Contains(bytes.ToLower(encoded), []byte(forbidden)) {
			t.Fatalf("IR encoded forbidden field %q: %s", forbidden, encoded)
		}
	}
	decoded, err := agent.DecodeDocumentIR(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, sealed) {
		t.Fatalf("decoded = %#v, want %#v", decoded, sealed)
	}
	if len(decoded.ReadingOrder) != 6 || len(decoded.References) != 1 ||
		decoded.References[0].Kind != agent.DocumentReferenceFootnote {
		t.Fatalf("decoded structure = %#v", decoded)
	}
	if decoded.Nodes[4].Kind != agent.DocumentNodeTable ||
		decoded.Nodes[5].Kind != agent.DocumentNodeTableRow ||
		decoded.Nodes[6].Kind != agent.DocumentNodeTableCell {
		t.Fatalf("table hierarchy = %#v", decoded.Nodes[4:8])
	}
}

func TestDocumentIRStableIDsDigestAndDeepCopies(t *testing.T) {
	t.Parallel()

	draft := validDocumentIR(t)
	first, err := agent.SealDocumentIR(draft)
	if err != nil {
		t.Fatal(err)
	}
	second, err := agent.SealDocumentIR(validDocumentIR(t))
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 || !reflect.DeepEqual(first, second) {
		t.Fatalf("deterministic seal mismatch: %#v != %#v", first, second)
	}
	resourceID, err := agent.StableDocumentResourceID(first.Source.SHA256, "OPS/content.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	if resourceID != first.Resources[0].ID {
		t.Fatalf("resource ID = %q, want %q", resourceID, first.Resources[0].ID)
	}
	documentID, err := agent.StableDocumentID(first.Source.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	if documentID != first.DocumentID {
		t.Fatalf("document ID = %q, want %q", documentID, first.DocumentID)
	}
	nodeID, err := agent.StableDocumentNodeID(
		first.DocumentID, first.Nodes[1].ID, agent.DocumentNodeParagraph, 2,
	)
	if err != nil {
		t.Fatal(err)
	}
	if nodeID != first.Nodes[3].ID {
		t.Fatalf("node ID = %q, want %q", nodeID, first.Nodes[3].ID)
	}

	changedDraft := validDocumentIR(t)
	changedDraft.Nodes[3].Text = "A changed paragraph with the same structural position."
	changed, err := agent.SealDocumentIR(changedDraft)
	if err != nil {
		t.Fatal(err)
	}
	if changed.SHA256 == first.SHA256 || changed.Nodes[3].ID != first.Nodes[3].ID {
		t.Fatalf("changed IR digest/id = %q/%q, original = %q/%q",
			changed.SHA256, changed.Nodes[3].ID, first.SHA256, first.Nodes[3].ID)
	}
	draft.Nodes[3].Text = "caller mutation"
	draft.Resources[0].Locator = "caller mutation"
	if first.Nodes[3].Text == "caller mutation" || first.Resources[0].Locator == "caller mutation" {
		t.Fatalf("SealDocumentIR aliased draft = %#v", first)
	}
	cloned := agent.CloneDocumentIR(first)
	cloned.Nodes[3].Text = "clone mutation"
	cloned.ReadingOrder[0] = "clone mutation"
	if first.Nodes[3].Text == "clone mutation" || first.ReadingOrder[0] == "clone mutation" {
		t.Fatalf("CloneDocumentIR aliased source = %#v", first)
	}
}

func TestDocumentIRRejectsBrokenHierarchyOrderAnchorTableAndReference(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*agent.DocumentIR)
	}{
		{name: "missing parent", mutate: func(value *agent.DocumentIR) { value.Nodes[3].ParentID = "node:sha256:" + strings.Repeat("f", 64) }},
		{name: "parent after child", mutate: func(value *agent.DocumentIR) { value.Nodes[1], value.Nodes[2] = value.Nodes[2], value.Nodes[1] }},
		{name: "ordinal gap", mutate: func(value *agent.DocumentIR) { value.Nodes[3].Ordinal = 9 }},
		{name: "unstable node id", mutate: func(value *agent.DocumentIR) { value.Nodes[3].ID = "node:sha256:" + strings.Repeat("e", 64) }},
		{name: "anchor out of bounds", mutate: func(value *agent.DocumentIR) { value.Nodes[3].Anchor.End = 5000 }},
		{name: "unknown resource", mutate: func(value *agent.DocumentIR) {
			value.Nodes[3].Anchor.ResourceID = "resource:sha256:" + strings.Repeat("d", 64)
		}},
		{name: "leaf has child", mutate: func(value *agent.DocumentIR) {
			value.Nodes[3].Kind = agent.DocumentNodeSection
			value.Nodes[3].ID, _ = agent.StableDocumentNodeID(value.DocumentID, value.Nodes[3].ParentID, value.Nodes[3].Kind, value.Nodes[3].Ordinal)
		}},
		{name: "table child kind", mutate: func(value *agent.DocumentIR) {
			value.Nodes[5].Kind = agent.DocumentNodeParagraph
			value.Nodes[5].ID, _ = agent.StableDocumentNodeID(value.DocumentID, value.Nodes[5].ParentID, value.Nodes[5].Kind, value.Nodes[5].Ordinal)
		}},
		{name: "duplicate reading order", mutate: func(value *agent.DocumentIR) { value.ReadingOrder[1] = value.ReadingOrder[0] }},
		{name: "missing reading leaf", mutate: func(value *agent.DocumentIR) { value.ReadingOrder = value.ReadingOrder[:len(value.ReadingOrder)-1] }},
		{name: "missing reference target", mutate: func(value *agent.DocumentIR) {
			value.References[0].TargetNodeID = "node:sha256:" + strings.Repeat("c", 64)
		}},
		{name: "footnote targets paragraph", mutate: func(value *agent.DocumentIR) { value.References[0].TargetNodeID = value.Nodes[3].ID }},
		{name: "duplicate resource", mutate: func(value *agent.DocumentIR) { value.Resources = append(value.Resources, value.Resources[0]) }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			draft := validDocumentIR(t)
			test.mutate(&draft)
			if _, err := agent.SealDocumentIR(draft); !errors.Is(err, agent.ErrDocumentIRInvalid) {
				t.Fatalf("SealDocumentIR error = %v", err)
			}
		})
	}
}

func TestDocumentIRRejectsBoundsDigestTamperingAndUnknownJSON(t *testing.T) {
	t.Parallel()

	draft := validDocumentIR(t)
	draft.Nodes[3].Text = strings.Repeat("x", agent.MaxDocumentNodeTextBytes+1)
	if _, err := agent.SealDocumentIR(draft); !errors.Is(err, agent.ErrDocumentIRBudget) {
		t.Fatalf("oversized node error = %v", err)
	}
	sealed, err := agent.SealDocumentIR(validDocumentIR(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed.SHA256 = "sha256:" + strings.Repeat("0", 64)
	if err := sealed.Validate(); !errors.Is(err, agent.ErrDocumentIRDigest) {
		t.Fatalf("tampered digest Validate error = %v", err)
	}
	valid, err := agent.SealDocumentIR(validDocumentIR(t))
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(`{"unknown":true,`), encoded[1:]...)
	if _, err := agent.DecodeDocumentIR(unknown); !errors.Is(err, agent.ErrDocumentIRInvalid) {
		t.Fatalf("unknown JSON decode error = %v", err)
	}
}

func validDocumentIR(t *testing.T) agent.DocumentIR {
	t.Helper()
	source := agent.SourceReference{
		Version: agent.SourceReferenceVersion, JobID: "job-ir", SourceID: "source-ir",
		SHA256: "sha256:" + strings.Repeat("a", 64), Bytes: 2000,
		MediaType: "application/epub+zip", FileName: "book.epub",
	}
	resourceID, err := agent.StableDocumentResourceID(source.SHA256, "OPS/content.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	resource := agent.DocumentResource{
		ID: resourceID, Locator: "OPS/content.xhtml", MediaType: "application/xhtml+xml",
		Bytes: 1000, SHA256: "sha256:" + strings.Repeat("b", 64),
	}
	documentID, err := agent.StableDocumentID(source.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	nodes := make([]agent.DocumentNode, 10)
	nodes[0] = documentNode(t, documentID, "", agent.DocumentNodeDocument, 1, resourceID, 0, 1000, "Book", "")
	nodes[1] = documentNode(t, documentID, nodes[0].ID, agent.DocumentNodeChapter, 1, resourceID, 0, 900, "Chapter one", "")
	nodes[2] = documentNode(t, documentID, nodes[1].ID, agent.DocumentNodeHeading, 1, resourceID, 10, 30, "", "Chapter one")
	nodes[3] = documentNode(t, documentID, nodes[1].ID, agent.DocumentNodeParagraph, 2, resourceID, 31, 120, "", "A paragraph with a footnote.")
	nodes[4] = documentNode(t, documentID, nodes[1].ID, agent.DocumentNodeTable, 3, resourceID, 121, 300, "Metrics", "")
	nodes[5] = documentNode(t, documentID, nodes[4].ID, agent.DocumentNodeTableRow, 1, resourceID, 130, 200, "", "")
	nodes[6] = documentNode(t, documentID, nodes[5].ID, agent.DocumentNodeTableCell, 1, resourceID, 140, 160, "", "Name")
	nodes[7] = documentNode(t, documentID, nodes[5].ID, agent.DocumentNodeTableCell, 2, resourceID, 161, 190, "", "Value")
	nodes[8] = documentNode(t, documentID, nodes[1].ID, agent.DocumentNodeFootnote, 4, resourceID, 301, 350, "n1", "Footnote detail")
	nodes[9] = documentNode(t, documentID, nodes[1].ID, agent.DocumentNodeParagraph, 5, resourceID, 351, 450, "", "Closing paragraph")
	return agent.DocumentIR{
		Version: agent.DocumentIRVersion, DocumentID: documentID, Source: source,
		Title: "A structured book", Language: "en", Resources: []agent.DocumentResource{resource}, Nodes: nodes,
		ReadingOrder: []string{nodes[2].ID, nodes[3].ID, nodes[6].ID, nodes[7].ID, nodes[8].ID, nodes[9].ID},
		References: []agent.DocumentReference{{
			FromNodeID: nodes[3].ID, Kind: agent.DocumentReferenceFootnote,
			TargetNodeID: nodes[8].ID, Label: "1",
		}},
	}
}

func documentNode(
	t *testing.T,
	documentID string,
	parentID string,
	kind agent.DocumentNodeKind,
	ordinal uint64,
	resourceID string,
	start uint64,
	end uint64,
	label string,
	text string,
) agent.DocumentNode {
	t.Helper()
	id, err := agent.StableDocumentNodeID(documentID, parentID, kind, ordinal)
	if err != nil {
		t.Fatal(err)
	}
	return agent.DocumentNode{
		ID: id, ParentID: parentID, Kind: kind, Ordinal: ordinal, Label: label, Text: text,
		Anchor: agent.DocumentAnchor{ResourceID: resourceID, Start: start, End: end},
	}
}
