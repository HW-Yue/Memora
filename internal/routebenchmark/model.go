package routebenchmark

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	CorpusVersion    = "memora.route-benchmark-corpus/v1"
	GeneratorVersion = "memora.route-benchmark-generator/v1"
)

var allowedFanouts = []int{4, 8, 12, 16, 24, 32}
var allowedDepths = []int{1, 2, 3, 4, 6}

type Corpus struct {
	Version          string     `json:"version"`
	GeneratorVersion string     `json:"generator_version"`
	ID               string     `json:"id"`
	Seed             uint64     `json:"seed"`
	Scenarios        []Scenario `json:"scenarios"`
	Hash             string     `json:"hash"`
}

type Scenario struct {
	ID          string   `json:"id"`
	Topic       string   `json:"topic"`
	Difficulty  string   `json:"difficulty"`
	Language    string   `json:"language"`
	Fanout      int      `json:"fanout"`
	Depth       int      `json:"depth"`
	ShuffleSeed uint64   `json:"shuffle_seed"`
	Snapshot    string   `json:"snapshot"`
	Database    Database `json:"database"`
	Table       Table    `json:"table"`
	Nodes       []Node   `json:"nodes"`
	Question    string   `json:"question"`
	Limits      Limits   `json:"limits"`
	Expected    Expected `json:"expected"`
}

type Database struct {
	DatabaseID string `json:"database_id"`
	Name       string `json:"name"`
	Purpose    string `json:"purpose"`
	Scope      string `json:"scope"`
}

type Table struct {
	TableID       string   `json:"table_id"`
	DatabaseID    string   `json:"database_id"`
	Name          string   `json:"name"`
	Purpose       string   `json:"purpose"`
	RowSemantics  string   `json:"row_semantics"`
	RouteRevision uint64   `json:"route_revision"`
	RootRouteIDs  []string `json:"root_route_ids"`
}

type Node struct {
	RouteID       string   `json:"route_id"`
	ParentRouteID string   `json:"parent_route_id,omitempty"`
	Level         int      `json:"level"`
	Kind          string   `json:"kind"`
	Name          string   `json:"name"`
	Aliases       []string `json:"aliases"`
	Purpose       string   `json:"purpose"`
	Revision      uint64   `json:"revision"`
	ChildRouteIDs []string `json:"child_route_ids"`
	RowIDs        []string `json:"row_ids"`
}

type Limits struct {
	MaxFallbacks         int    `json:"max_fallbacks"`
	MaxToolCalls         int    `json:"max_tool_calls"`
	MaxContextCharacters int    `json:"max_context_characters"`
	MaxElapsedMillis     int    `json:"max_elapsed_ms"`
	StopCondition        string `json:"stop_condition"`
}

type Expected struct {
	Match bool     `json:"match"`
	Path  []string `json:"path"`
	RowID string   `json:"row_id,omitempty"`
}

type Error struct{ Message string }

func (err *Error) Error() string { return "Route benchmark corpus: " + err.Message }

func Load(path string) (Corpus, error) {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return Corpus{}, corpusError("read: %v", err)
	}
	var corpus Corpus
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&corpus); err != nil {
		return Corpus{}, corpusError("decode: %v", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("unexpected trailing JSON value")
		}
		return Corpus{}, corpusError("decode: %v", err)
	}
	if err := corpus.Validate(); err != nil {
		return Corpus{}, err
	}
	return corpus, nil
}

func Encode(corpus Corpus) ([]byte, error) {
	if err := corpus.Validate(); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(corpus, "", "  ")
	if err != nil {
		return nil, corpusError("encode: %v", err)
	}
	return append(encoded, '\n'), nil
}

func (corpus Corpus) Validate() error {
	if corpus.Version != CorpusVersion || corpus.GeneratorVersion != GeneratorVersion ||
		corpus.ID != "route-retrieval-v1" || corpus.Seed != generatorSeed || len(corpus.Scenarios) != 30 {
		return corpusError("identity or scenario count is invalid")
	}
	seenIDs, combinations := map[string]bool{}, map[string]bool{}
	difficulties, languages, topics := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for index := range corpus.Scenarios {
		scenario := corpus.Scenarios[index]
		if seenIDs[scenario.ID] {
			return corpusError("scenario ID %q is duplicated", scenario.ID)
		}
		seenIDs[scenario.ID] = true
		if err := scenario.Validate(); err != nil {
			return err
		}
		combinations[matrixKey(scenario.Fanout, scenario.Depth)] = true
		difficulties[scenario.Difficulty] = true
		languages[scenario.Language] = true
		topics[scenario.Topic] = true
	}
	for _, fanout := range allowedFanouts {
		for _, depth := range allowedDepths {
			if !combinations[matrixKey(fanout, depth)] {
				return corpusError("fanout=%d depth=%d is missing", fanout, depth)
			}
		}
	}
	if len(difficulties) != 5 || len(languages) != 3 || len(topics) != 6 {
		return corpusError("difficulty, language, or topic coverage is incomplete")
	}
	wantHash, err := corpusDigest(corpus)
	if err != nil {
		return err
	}
	if corpus.Hash != wantHash {
		return corpusError("corpus hash mismatch")
	}
	return nil
}

func (scenario Scenario) Validate() error {
	if !validSlug(scenario.ID) || !validSlug(scenario.Topic) ||
		!containsString([]string{"separated", "related", "boundary_overlap", "synonym", "negative"}, scenario.Difficulty) ||
		!containsString([]string{"zh", "en", "mixed"}, scenario.Language) ||
		!containsInt(allowedFanouts, scenario.Fanout) || !containsInt(allowedDepths, scenario.Depth) ||
		!validText(scenario.Question, 512) || scenario.ShuffleSeed == 0 ||
		strings.Contains(scenario.Question, "db_") || strings.Contains(scenario.Question, "tbl_") ||
		strings.Contains(scenario.Question, "route_") || strings.Contains(scenario.Question, "row_") {
		return corpusError("scenario %q metadata is invalid", scenario.ID)
	}
	if !validStableID(scenario.Database.DatabaseID, "db_") || !validText(scenario.Database.Name, 120) ||
		!validText(scenario.Database.Purpose, 1000) || !validText(scenario.Database.Scope, 1000) ||
		!validStableID(scenario.Table.TableID, "tbl_") || scenario.Table.DatabaseID != scenario.Database.DatabaseID ||
		!validText(scenario.Table.Name, 120) || !validText(scenario.Table.Purpose, 1000) ||
		!validText(scenario.Table.RowSemantics, 1000) || scenario.Table.RouteRevision != 1 ||
		len(scenario.Table.RootRouteIDs) != scenario.Fanout {
		return corpusError("scenario %q Database/Table fixture is invalid", scenario.ID)
	}
	if scenario.Limits.MaxFallbacks != 4 || scenario.Limits.MaxToolCalls != 64 ||
		scenario.Limits.MaxContextCharacters != 12000 || scenario.Limits.MaxElapsedMillis != 600000 {
		return corpusError("scenario %q limits are invalid", scenario.ID)
	}
	nodes := make(map[string]Node, len(scenario.Nodes))
	childrenByParent := map[string][]string{}
	rowIDs := map[string]bool{}
	for _, node := range scenario.Nodes {
		if !validStableID(node.RouteID, "route_") || nodes[node.RouteID].RouteID != "" ||
			node.Level < 1 || node.Level > scenario.Depth ||
			(node.Kind != "branch" && node.Kind != "leaf") || !validText(node.Name, 160) ||
			!validText(node.Purpose, 1000) || node.Revision != 1 || node.Aliases == nil ||
			node.ChildRouteIDs == nil || node.RowIDs == nil {
			return corpusError("scenario %q Route node is invalid or duplicated", scenario.ID)
		}
		if node.ParentRouteID != "" && !validStableID(node.ParentRouteID, "route_") {
			return corpusError("scenario %q Route parent is invalid", scenario.ID)
		}
		if node.ParentRouteID == "" && node.Level != 1 {
			return corpusError("scenario %q root Route level is invalid", scenario.ID)
		}
		if node.Kind == "branch" && (len(node.ChildRouteIDs) == 0 || len(node.RowIDs) != 0) ||
			node.Kind == "leaf" && (len(node.ChildRouteIDs) != 0 || len(node.RowIDs) != 1 ||
				!validStableID(node.RowIDs[0], "row_")) {
			return corpusError("scenario %q Route branch/leaf projection is invalid", scenario.ID)
		}
		if hasDuplicate(node.Aliases) || hasDuplicate(node.ChildRouteIDs) || hasDuplicate(node.RowIDs) {
			return corpusError("scenario %q Route lists contain duplicates", scenario.ID)
		}
		if node.Kind == "leaf" {
			if rowIDs[node.RowIDs[0]] {
				return corpusError("scenario %q RowID is duplicated across leaves", scenario.ID)
			}
			rowIDs[node.RowIDs[0]] = true
		}
		nodes[node.RouteID] = node
		childrenByParent[node.ParentRouteID] = append(childrenByParent[node.ParentRouteID], node.RouteID)
	}
	if hasDuplicate(scenario.Table.RootRouteIDs) || !sameSet(scenario.Table.RootRouteIDs, childrenByParent[""]) {
		return corpusError("scenario %q root Route order/set is invalid", scenario.ID)
	}
	for _, node := range scenario.Nodes {
		if node.ParentRouteID != "" {
			parent, ok := nodes[node.ParentRouteID]
			if !ok || parent.Level+1 != node.Level || !containsString(parent.ChildRouteIDs, node.RouteID) {
				return corpusError("scenario %q Route tree is disconnected", scenario.ID)
			}
		}
		if node.Kind == "branch" && !sameSet(node.ChildRouteIDs, childrenByParent[node.RouteID]) {
			return corpusError("scenario %q branch children are inconsistent", scenario.ID)
		}
	}
	if scenario.Expected.Match == (scenario.Difficulty == "negative") {
		return corpusError("scenario %q difficulty and match label disagree", scenario.ID)
	}
	if scenario.Expected.Match {
		if len(scenario.Expected.Path) != scenario.Depth || !validStableID(scenario.Expected.RowID, "row_") ||
			scenario.Limits.StopCondition != "rowid_selected" {
			return corpusError("scenario %q positive ground truth is invalid", scenario.ID)
		}
		parent := ""
		for index, routeID := range scenario.Expected.Path {
			node, ok := nodes[routeID]
			if !ok || node.ParentRouteID != parent ||
				(index < scenario.Depth-1 && node.Kind != "branch") ||
				(index == scenario.Depth-1 && (node.Kind != "leaf" || node.RowIDs[0] != scenario.Expected.RowID)) {
				return corpusError("scenario %q ground-truth path is disconnected", scenario.ID)
			}
			parent = routeID
		}
	} else if scenario.Difficulty != "negative" || len(scenario.Expected.Path) != 0 ||
		scenario.Expected.RowID != "" || scenario.Limits.StopCondition != "no_matching_route" {
		return corpusError("scenario %q negative ground truth is invalid", scenario.ID)
	}
	wantSnapshot, err := scenarioDigest(scenario)
	if err != nil {
		return err
	}
	if scenario.Snapshot != wantSnapshot {
		return corpusError("scenario %q snapshot mismatch", scenario.ID)
	}
	return nil
}

func corpusDigest(corpus Corpus) (string, error) {
	corpus.Hash = ""
	return digestJSON(corpus)
}

func scenarioDigest(scenario Scenario) (string, error) {
	fixture := struct {
		Database Database `json:"database"`
		Table    Table    `json:"table"`
		Nodes    []Node   `json:"nodes"`
	}{scenario.Database, scenario.Table, scenario.Nodes}
	return digestJSON(fixture)
}

func digestJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", corpusError("encode identity: %v", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func sameSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy, rightCopy := append([]string(nil), left...), append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	return reflectStrings(leftCopy, rightCopy)
}

func reflectStrings(left, right []string) bool {
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func hasDuplicate(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] {
			return true
		}
		seen[value] = true
	}
	return false
}

func validSlug(value string) bool {
	if len(value) == 0 || len(value) > 160 {
		return false
	}
	for index, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			index > 0 && (character == '-' || character == '_') {
			continue
		}
		return false
	}
	return true
}

func validStableID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func validText(value string, maximum int) bool {
	if !utf8.ValidString(value) || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > maximum {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func matrixKey(fanout, depth int) string { return fmt.Sprintf("%d/%d", fanout, depth) }

func corpusError(format string, arguments ...any) error {
	return &Error{Message: fmt.Sprintf(format, arguments...)}
}
