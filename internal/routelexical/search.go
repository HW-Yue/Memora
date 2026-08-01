package routelexical

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/router"
)

const (
	maxQueryRunes = 256
	maxQueryTerms = 32
)

var (
	ErrInvalidQuery  = errors.New("invalid lexical Route query")
	ErrInvalidSource = errors.New("invalid lexical Route source")
)

type Source struct {
	Databases []catalog.Database
	Routes    []router.Node
}

type Match struct {
	DatabaseID    string
	TableID       string
	RouteID       string
	RouteRevision uint64
	MatchedFields []string
	MatchCount    uint64
}

type Result struct {
	Snapshot        string
	CatalogRevision string
	Matches         []Match
}

type SnapshotState struct {
	Snapshot        string
	CatalogRevision string
}

type surface struct {
	location Match
	fields   []field
}

type field struct {
	name string
	text []string
}

type posting struct {
	surface int
	field   string
}

type catalogView struct {
	Databases []databaseView `json:"databases"`
}

type databaseView struct {
	ID            string      `json:"database_id"`
	Name          string      `json:"name"`
	Aliases       []string    `json:"aliases"`
	Purpose       string      `json:"purpose"`
	Scope         string      `json:"scope"`
	AntiScope     string      `json:"anti_scope,omitempty"`
	SchemaVersion uint64      `json:"schema_version"`
	Tables        []tableView `json:"tables"`
}

type tableView struct {
	ID            string   `json:"table_id"`
	DatabaseID    string   `json:"database_id"`
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	Purpose       string   `json:"purpose"`
	Scope         string   `json:"scope,omitempty"`
	AntiScope     string   `json:"anti_scope,omitempty"`
	RowSemantics  string   `json:"row_semantics"`
	SchemaVersion uint64   `json:"schema_version"`
}

type snapshotView struct {
	CatalogSHA256 string       `json:"catalog_sha256"`
	Routes        []routerView `json:"routes"`
}

type routerView struct {
	ID         string      `json:"route_id"`
	DatabaseID string      `json:"database_id"`
	TableID    string      `json:"table_id"`
	ParentID   string      `json:"parent_id,omitempty"`
	Name       string      `json:"name"`
	Aliases    []string    `json:"aliases"`
	Path       string      `json:"path"`
	Kind       router.Kind `json:"kind"`
	Purpose    string      `json:"purpose"`
	Synopsis   string      `json:"synopsis,omitempty"`
	Revision   uint64      `json:"revision"`
}

func Search(source Source, query string) (Result, error) {
	queryTerms, err := queryTokens(query)
	if err != nil {
		return Result{}, err
	}
	surfaces, state, err := normalizeSnapshot(source)
	if err != nil {
		return Result{}, err
	}

	postings := make(map[string][]posting)
	for surfaceIndex, candidate := range surfaces {
		for _, value := range candidate.fields {
			for term := range tokenizeMany(value.text) {
				postings[term] = append(postings[term], posting{surface: surfaceIndex, field: value.name})
			}
		}
	}
	hits := make(map[int]map[string]uint64)
	for term := range queryTerms {
		for _, value := range postings[term] {
			if hits[value.surface] == nil {
				hits[value.surface] = make(map[string]uint64)
			}
			hits[value.surface][value.field]++
		}
	}
	matches := make([]Match, 0, len(hits))
	for surfaceIndex, candidate := range surfaces {
		matchedFields := make([]string, 0, len(candidate.fields))
		var count uint64
		for _, value := range candidate.fields {
			fieldMatches := hits[surfaceIndex][value.name]
			if fieldMatches > 0 {
				matchedFields = append(matchedFields, value.name)
				count += fieldMatches
			}
		}
		if count == 0 {
			continue
		}
		match := candidate.location
		match.MatchedFields = matchedFields
		match.MatchCount = count
		matches = append(matches, match)
	}
	sort.Slice(matches, func(left, right int) bool { return lessMatch(matches[left], matches[right]) })
	return Result{Snapshot: state.Snapshot, CatalogRevision: state.CatalogRevision, Matches: matches}, nil
}

// Snapshot validates and hashes the same current semantic source used by
// lexical discovery without requiring an artificial lexical query.
func Snapshot(source Source) (SnapshotState, error) {
	_, state, err := normalizeSnapshot(source)
	return state, err
}

func normalizeSnapshot(source Source) ([]surface, SnapshotState, error) {
	surfaces, catalogState, routeState, err := normalizeSource(source)
	if err != nil {
		return nil, SnapshotState{}, err
	}
	catalogDigest, err := digest(catalogState)
	if err != nil {
		return nil, SnapshotState{}, sourceError("encode Catalog snapshot: %v", err)
	}
	snapshotDigest, err := digest(snapshotView{CatalogSHA256: catalogDigest, Routes: routeState})
	if err != nil {
		return nil, SnapshotState{}, sourceError("encode Route snapshot: %v", err)
	}
	return surfaces, SnapshotState{Snapshot: snapshotDigest, CatalogRevision: catalogDigest}, nil
}

func normalizeSource(source Source) ([]surface, catalogView, []routerView, error) {
	if source.Databases == nil || source.Routes == nil {
		return nil, catalogView{}, nil, sourceError("Database and Route inputs must be arrays")
	}
	databases := append([]catalog.Database(nil), source.Databases...)
	sort.Slice(databases, func(left, right int) bool { return databases[left].ID < databases[right].ID })
	state := catalogView{Databases: make([]databaseView, 0, len(databases))}
	surfaces := make([]surface, 0)
	databaseIDs := make(map[string]struct{}, len(databases))
	tableIDs := make(map[string]string)
	for _, database := range databases {
		if !validIdentity(database.ID) || strings.TrimSpace(database.Name) == "" || database.SchemaVersion == 0 {
			return nil, catalogView{}, nil, sourceError("Database identity is invalid")
		}
		if _, exists := databaseIDs[database.ID]; exists {
			return nil, catalogView{}, nil, sourceError("Database %q is duplicated", database.ID)
		}
		databaseIDs[database.ID] = struct{}{}
		aliases := sortedStrings(database.Aliases)
		view := databaseView{
			ID: database.ID, Name: database.Name, Aliases: aliases, Purpose: database.Purpose,
			Scope: database.Scope, AntiScope: database.AntiScope, SchemaVersion: database.SchemaVersion,
			Tables: []tableView{},
		}
		surfaces = append(surfaces, surface{
			location: Match{DatabaseID: database.ID},
			fields: []field{
				{name: "database.aliases", text: aliases},
				{name: "database.anti_scope", text: []string{database.AntiScope}},
				{name: "database.name", text: []string{database.Name}},
				{name: "database.purpose", text: []string{database.Purpose}},
				{name: "database.scope", text: []string{database.Scope}},
			},
		})
		tables := append([]catalog.Table(nil), database.Tables...)
		sort.Slice(tables, func(left, right int) bool { return tables[left].ID < tables[right].ID })
		for _, table := range tables {
			if !validIdentity(table.ID) || table.DatabaseID != database.ID ||
				strings.TrimSpace(table.Name) == "" || table.SchemaVersion == 0 {
				return nil, catalogView{}, nil, sourceError("Table identity or Database scope is invalid")
			}
			if _, exists := tableIDs[table.ID]; exists {
				return nil, catalogView{}, nil, sourceError("Table %q is duplicated", table.ID)
			}
			tableIDs[table.ID] = database.ID
			aliases := sortedStrings(table.Aliases)
			view.Tables = append(view.Tables, tableView{
				ID: table.ID, DatabaseID: table.DatabaseID, Name: table.Name, Aliases: aliases,
				Purpose: table.Purpose, Scope: table.Scope, AntiScope: table.AntiScope,
				RowSemantics: table.RowSemantics, SchemaVersion: table.SchemaVersion,
			})
			surfaces = append(surfaces, surface{
				location: Match{DatabaseID: database.ID, TableID: table.ID},
				fields: []field{
					{name: "table.aliases", text: aliases},
					{name: "table.anti_scope", text: []string{table.AntiScope}},
					{name: "table.name", text: []string{table.Name}},
					{name: "table.purpose", text: []string{table.Purpose}},
					{name: "table.row_semantics", text: []string{table.RowSemantics}},
					{name: "table.scope", text: []string{table.Scope}},
				},
			})
		}
		state.Databases = append(state.Databases, view)
	}

	latest := make(map[string]router.Node)
	for _, node := range source.Routes {
		if node.Version != router.Version || !validIdentity(node.ID) || node.Revision == 0 {
			return nil, catalogView{}, nil, sourceError("Route identity is invalid")
		}
		current, exists := latest[node.ID]
		if !exists || node.Revision > current.Revision {
			latest[node.ID] = cloneNode(node)
		} else if node.Revision == current.Revision {
			return nil, catalogView{}, nil, sourceError("Route %q revision is duplicated", node.ID)
		}
	}
	routeState := make([]routerView, 0, len(latest))
	for _, node := range latest {
		if node.Deleted || node.TableID == "" {
			continue
		}
		databaseID, exists := tableIDs[node.TableID]
		if !exists || databaseID != node.DatabaseID || !validRouteKind(node.Kind) || strings.TrimSpace(node.Purpose) == "" {
			return nil, catalogView{}, nil, sourceError("Route %q violates its Table scope", node.ID)
		}
		aliases := sortedStrings(node.Aliases)
		routeState = append(routeState, routerView{
			ID: node.ID, DatabaseID: node.DatabaseID, TableID: node.TableID, ParentID: node.ParentID,
			Name: node.Name, Aliases: aliases, Path: node.Path, Kind: node.Kind,
			Purpose: node.Purpose, Synopsis: node.Synopsis, Revision: node.Revision,
		})
		surfaces = append(surfaces, surface{
			location: Match{DatabaseID: node.DatabaseID, TableID: node.TableID, RouteID: node.ID, RouteRevision: node.Revision},
			fields: []field{
				{name: "route.aliases", text: aliases},
				{name: "route.name", text: []string{node.Name}},
				{name: "route.path", text: []string{node.Path}},
				{name: "route.purpose", text: []string{node.Purpose}},
				{name: "route.synopsis", text: []string{node.Synopsis}},
			},
		})
	}
	sort.Slice(routeState, func(left, right int) bool { return routeState[left].ID < routeState[right].ID })
	return surfaces, state, routeState, nil
}

func queryTokens(query string) (map[string]struct{}, error) {
	if !utf8.ValidString(query) || strings.TrimSpace(query) == "" || utf8.RuneCountInString(query) > maxQueryRunes {
		return nil, queryError("query must contain 1 to %d Unicode characters", maxQueryRunes)
	}
	values := tokenize(query)
	if len(values) == 0 || len(values) > maxQueryTerms {
		return nil, queryError("query must produce 1 to %d unique terms", maxQueryTerms)
	}
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result, nil
}

func tokenizeMany(values []string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, value := range values {
		for _, token := range tokenize(value) {
			result[token] = struct{}{}
		}
	}
	return result
}

func tokenize(value string) []string {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	word := make([]rune, 0)
	han := make([]rune, 0)
	appendToken := func(token string) {
		if token == "" {
			return
		}
		if _, exists := seen[token]; exists {
			return
		}
		seen[token] = struct{}{}
		result = append(result, token)
	}
	flushWord := func() {
		if len(word) > 0 {
			appendToken(strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		if len(han) == 1 {
			appendToken(string(han))
		} else {
			for index := 0; index+1 < len(han); index++ {
				appendToken(string(han[index : index+2]))
			}
		}
		han = han[:0]
	}
	for _, character := range value {
		switch {
		case unicode.Is(unicode.Han, character):
			flushWord()
			han = append(han, character)
		case unicode.IsLetter(character) || unicode.IsDigit(character):
			flushHan()
			word = append(word, unicode.ToLower(character))
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return result
}

func lessMatch(left, right Match) bool {
	if left.MatchCount != right.MatchCount {
		return left.MatchCount > right.MatchCount
	}
	leftSpecificity, rightSpecificity := specificity(left), specificity(right)
	if leftSpecificity != rightSpecificity {
		return leftSpecificity > rightSpecificity
	}
	if left.DatabaseID != right.DatabaseID {
		return left.DatabaseID < right.DatabaseID
	}
	if left.TableID != right.TableID {
		return left.TableID < right.TableID
	}
	return left.RouteID < right.RouteID
}

func specificity(value Match) int {
	if value.RouteID != "" {
		return 3
	}
	if value.TableID != "" {
		return 2
	}
	return 1
}

func sortedStrings(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	return result
}

func cloneNode(value router.Node) router.Node {
	value.Aliases = append([]string{}, value.Aliases...)
	return value
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= 200 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n\t")
}

func validRouteKind(value router.Kind) bool {
	return value == router.KindRoot || value == router.KindBranch || value == router.KindLeaf
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func queryError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidQuery, fmt.Sprintf(format, arguments...))
}

func sourceError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidSource, fmt.Sprintf(format, arguments...))
}
