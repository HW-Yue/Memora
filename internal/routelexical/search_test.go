package routelexical_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/routelexical"
	"github.com/HW-Yue/Memora/internal/router"
)

func TestSearchAggregatesOnlySemanticLocationFieldsAcrossLanguages(t *testing.T) {
	t.Parallel()
	source := semanticSource()
	english, err := routelexical.Search(source, "recover write ahead log")
	if err != nil {
		t.Fatal(err)
	}
	if english.CatalogRevision == "" || english.Snapshot == "" || len(english.Matches) < 1 {
		t.Fatalf("English Search() = %#v", english)
	}
	first := english.Matches[0]
	if first.RouteID != "route_wal" || first.DatabaseID != "db_work" || first.TableID != "tbl_notes" ||
		first.RouteRevision != 2 || first.MatchCount < 2 || !contains(first.MatchedFields, "route.purpose") {
		t.Fatalf("first English match = %#v", first)
	}

	chinese, err := routelexical.Search(source, "数据库崩溃恢复")
	if err != nil {
		t.Fatal(err)
	}
	if len(chinese.Matches) == 0 || chinese.Matches[0].RouteID != "route_recovery" ||
		!contains(chinese.Matches[0].MatchedFields, "route.name") {
		t.Fatalf("Chinese Search() = %#v", chinese)
	}
	for _, match := range append(english.Matches, chinese.Matches...) {
		if match.DatabaseID == "db_private" {
			t.Fatalf("unprovided Database leaked through match %#v", match)
		}
	}
}

func TestSearchUsesLatestLiveRouteAndNeverAcceptsRowsOrMemberships(t *testing.T) {
	t.Parallel()
	source := semanticSource()
	source.Routes = append(source.Routes,
		router.Node{
			Version: router.Version, ID: "route_wal", DatabaseID: "db_work", TableID: "tbl_notes",
			ParentID: "route_root", Name: "retired secret", Path: "/retired", Kind: router.KindLeaf,
			Purpose: "obsolete material", Revision: 1,
		},
		router.Node{
			Version: router.Version, ID: "route_deleted", DatabaseID: "db_work", TableID: "tbl_notes",
			ParentID: "route_root", Name: "deleted secret", Path: "/deleted", Kind: router.KindLeaf,
			Purpose: "obsolete material", Revision: 2, Deleted: true,
		},
	)
	result, err := routelexical.Search(source, "retired deleted obsolete secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range result.Matches {
		if match.RouteID == "route_wal" || match.RouteID == "route_deleted" {
			t.Fatalf("stale/deleted Route matched: %#v", match)
		}
	}

	typeOfSource := reflect.TypeOf(routelexical.Source{})
	for _, forbidden := range []string{"Rows", "History", "Memberships", "Snippets", "Body"} {
		if _, exists := typeOfSource.FieldByName(forbidden); exists {
			t.Fatalf("Source unexpectedly accepts fact field %q", forbidden)
		}
	}
}

func TestSearchSnapshotIsDeterministicAndChangesWithVisibleMetadata(t *testing.T) {
	t.Parallel()
	first := semanticSource()
	second := semanticSource()
	reverseDatabases(second.Databases)
	reverseRoutes(second.Routes)
	one, err := routelexical.Search(first, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	two, err := routelexical.Search(second, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if one.Snapshot != two.Snapshot || one.CatalogRevision != two.CatalogRevision ||
		!reflect.DeepEqual(one.Matches, two.Matches) {
		t.Fatalf("ordering changed deterministic result: %#v != %#v", one, two)
	}

	changedCatalog := semanticSource()
	changedCatalog.Databases[0].Tables[0].Purpose = "Changed semantic purpose"
	catalogResult, err := routelexical.Search(changedCatalog, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if catalogResult.CatalogRevision == one.CatalogRevision || catalogResult.Snapshot == one.Snapshot {
		t.Fatal("Catalog surface change did not change revisions")
	}

	changedRoute := semanticSource()
	changedRoute.Routes[0].Purpose = "Changed Route purpose"
	routeResult, err := routelexical.Search(changedRoute, "recovery")
	if err != nil {
		t.Fatal(err)
	}
	if routeResult.CatalogRevision != one.CatalogRevision || routeResult.Snapshot == one.Snapshot {
		t.Fatal("Route surface change did not change only discovery snapshot")
	}
}

func TestSearchRejectsInvalidQueryAndCorruptScope(t *testing.T) {
	t.Parallel()
	source := semanticSource()
	for _, query := range []string{"", "!!!", tooManyTerms(), strings.Repeat("界", 257)} {
		if _, err := routelexical.Search(source, query); !errors.Is(err, routelexical.ErrInvalidQuery) {
			t.Fatalf("Search(%q) error = %v", query, err)
		}
	}
	crossScope := semanticSource()
	crossScope.Routes[0].TableID = "tbl_missing"
	if _, err := routelexical.Search(crossScope, "recovery"); !errors.Is(err, routelexical.ErrInvalidSource) {
		t.Fatalf("cross-scope error = %v", err)
	}
}

func semanticSource() routelexical.Source {
	return routelexical.Source{
		Databases: []catalog.Database{
			{
				ID: "db_work", Name: "work", Aliases: []string{"工作"}, Purpose: "Engineering knowledge",
				Scope: "software architecture", SchemaVersion: 3,
				Tables: []catalog.Table{
					{ID: "tbl_notes", DatabaseID: "db_work", Name: "notes", Aliases: []string{"笔记"},
						Purpose: "Durable design notes", Scope: "engineering", RowSemantics: "One decision", SchemaVersion: 4},
					{ID: "tbl_people", DatabaseID: "db_work", Name: "people", Aliases: []string{},
						Purpose: "People directory", Scope: "contacts", RowSemantics: "One person", SchemaVersion: 1},
				},
			},
		},
		Routes: []router.Node{
			{Version: router.Version, ID: "route_wal", DatabaseID: "db_work", TableID: "tbl_notes",
				ParentID: "route_root", Name: "write ahead log", Aliases: []string{"WAL"}, Path: "/storage/wal",
				Kind: router.KindLeaf, Purpose: "Recover committed writes after crash", Synopsis: "redo recovery", Revision: 2},
			{Version: router.Version, ID: "route_recovery", DatabaseID: "db_work", TableID: "tbl_notes",
				ParentID: "route_root", Name: "数据库恢复", Aliases: []string{"崩溃恢复"}, Path: "/存储/恢复",
				Kind: router.KindLeaf, Purpose: "数据库崩溃后的恢复流程", Revision: 1},
		},
	}
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func reverseDatabases(values []catalog.Database) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
	for index := range values {
		for left, right := 0, len(values[index].Tables)-1; left < right; left, right = left+1, right-1 {
			values[index].Tables[left], values[index].Tables[right] = values[index].Tables[right], values[index].Tables[left]
		}
	}
}

func reverseRoutes(values []router.Node) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}

func tooManyTerms() string {
	return strings.Join([]string{
		"t00", "t01", "t02", "t03", "t04", "t05", "t06", "t07", "t08", "t09", "t10",
		"t11", "t12", "t13", "t14", "t15", "t16", "t17", "t18", "t19", "t20", "t21",
		"t22", "t23", "t24", "t25", "t26", "t27", "t28", "t29", "t30", "t31", "t32",
	}, " ")
}
