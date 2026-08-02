package routefulltext

import (
	"errors"
	"testing"

	"github.com/HW-Yue/Memora/internal/fulltext"
	"github.com/HW-Yue/Memora/internal/router"
)

func TestProjectCoversCurrentRouteSemanticSurface(t *testing.T) {
	documents, err := Project(routeFixture())
	if err != nil || len(documents) != 3 {
		t.Fatalf("Project() = %#v, %v", documents, err)
	}
	index, err := fulltext.Build(documents)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct{ term, objectID, fieldID string }{
		{"root", "route_root", "name"},
		{"architecture", "route_branch", "name"},
		{"design", "route_branch", "aliases"},
		{"branch", "route_branch", "kind"},
		{"decisions", "route_branch", "purpose"},
		{"boundaries", "route_branch", "synopsis"},
		{"leaf", "route_leaf", "kind"},
	}
	for _, test := range tests {
		postings := index.Postings(test.term)
		found := false
		for _, posting := range postings {
			found = found || (posting.ObjectID == test.objectID && posting.FieldID == test.fieldID)
		}
		if !found {
			t.Fatalf("Postings(%q) = %#v", test.term, postings)
		}
	}
}

func TestProjectIsOrderDeterministicAndSkipsBlankSynopsis(t *testing.T) {
	nodes := routeFixture()
	nodes[2].Synopsis = " \t"
	first, err := Project([]router.Node{nodes[2], nodes[0], nodes[1]})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Project(nodes)
	if err != nil || len(first) != len(second) {
		t.Fatalf("Project() = %#v / %#v, %v", first, second, err)
	}
	for index := range first {
		left, leftErr := fulltext.Compile(first[index])
		right, rightErr := fulltext.Compile(second[index])
		if leftErr != nil || rightErr != nil || left.Digest != right.Digest {
			t.Fatalf("document %d digest = %+v/%v %+v/%v", index, left, leftErr, right, rightErr)
		}
	}
}

func TestTombstoneAdvancesRouteRevision(t *testing.T) {
	document, err := Tombstone(fulltext.Object{
		Kind: fulltext.KindRoute, DatabaseID: "db_work", TableID: "tbl_notes",
		ObjectID: "route_leaf", Revision: 3, State: fulltext.StateLive, Digest: "sha256:old",
	})
	if err != nil || document.Revision != 4 || document.SchemaRevision != 0 ||
		document.State != fulltext.StateDeleted || len(document.Fields) != 0 {
		t.Fatalf("Tombstone() = %#v, %v", document, err)
	}
	if _, err := fulltext.Compile(document); err != nil {
		t.Fatal(err)
	}
}

func TestProjectChangesUsesDeletedNodeCurrentRevision(t *testing.T) {
	nodes := routeFixture()
	nodes[1].Revision = 3
	nodes[2].Revision, nodes[2].Deleted = 2, true
	documents, err := ProjectChanges([]router.Node{nodes[2], nodes[1]})
	if err != nil || len(documents) != 2 {
		t.Fatalf("ProjectChanges() = %#v, %v", documents, err)
	}
	if documents[0].ObjectID != "route_branch" || documents[0].State != fulltext.StateLive ||
		documents[1].ObjectID != "route_leaf" || documents[1].Revision != 2 ||
		documents[1].State != fulltext.StateDeleted || len(documents[1].Fields) != 0 {
		t.Fatalf("change documents = %#v", documents)
	}
	for _, document := range documents {
		if _, err := fulltext.Compile(document); err != nil {
			t.Fatal(err)
		}
	}
}

func TestProjectRejectsInvalidRouteIdentityScopeAndState(t *testing.T) {
	tests := []func([]router.Node){
		func(nodes []router.Node) { nodes[0].Version = "future" },
		func(nodes []router.Node) { nodes[1].Revision = 0 },
		func(nodes []router.Node) { nodes[1].Kind = "folder" },
		func(nodes []router.Node) { nodes[1].DatabaseID = "" },
		func(nodes []router.Node) { nodes[1].Purpose = " " },
		func(nodes []router.Node) { nodes[2].Deleted = true },
	}
	for index, mutate := range tests {
		nodes := routeFixture()
		mutate(nodes)
		if _, err := Project(nodes); !errors.Is(err, ErrInvalid) {
			t.Fatalf("case %d Project() error = %v", index, err)
		}
	}
	if _, err := Tombstone(fulltext.Object{
		Kind: fulltext.KindRow, DatabaseID: "db_work", TableID: "tbl_notes",
		ObjectID: "row_1", Revision: 1, State: fulltext.StateLive,
	}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Row Tombstone() error = %v", err)
	}
}

func routeFixture() []router.Node {
	return []router.Node{
		{Version: router.Version, ID: "route_root", DatabaseID: "db_work", TableID: "tbl_notes",
			Name: "root", Path: "/", Kind: router.KindRoot, Purpose: "All notes", Revision: 1},
		{Version: router.Version, ID: "route_branch", DatabaseID: "db_work", TableID: "tbl_notes",
			ParentID: "route_root", Name: "architecture", Aliases: []string{"design"}, Path: "/architecture",
			Kind: router.KindBranch, Purpose: "Architecture decisions", Synopsis: "System boundaries", Revision: 2},
		{Version: router.Version, ID: "route_leaf", DatabaseID: "db_work", TableID: "tbl_notes",
			ParentID: "route_branch", Name: "storage", Path: "/architecture/storage",
			Kind: router.KindLeaf, Purpose: "Storage decisions", Revision: 1},
	}
}
