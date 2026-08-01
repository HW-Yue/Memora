package routebenchmark

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
)

const generatorSeed uint64 = 0x4d454d4f5241

type term struct {
	Slug    string
	English string
	Chinese string
	Purpose string
}

type topic struct {
	Slug      string
	Chain     []term
	RelatedEN string
	RelatedZH string
	OverlapEN string
	OverlapZH string
	SynonymEN string
	SynonymZH string
}

var difficulties = []string{"separated", "related", "boundary_overlap", "synonym", "negative"}
var languages = []string{"zh", "en", "mixed"}

func Generate() (Corpus, error) {
	corpus := Corpus{
		Version: CorpusVersion, GeneratorVersion: GeneratorVersion,
		ID: "route-retrieval-v1", Seed: generatorSeed, Scenarios: []Scenario{},
	}
	for fanoutIndex, fanout := range allowedFanouts {
		selectedTopic := topics()[fanoutIndex]
		for depthIndex, depth := range allowedDepths {
			difficulty := difficulties[(fanoutIndex+depthIndex)%len(difficulties)]
			language := languages[(fanoutIndex*len(allowedDepths)+depthIndex)%len(languages)]
			scenario, err := generateScenario(selectedTopic, fanout, depth, difficulty, language)
			if err != nil {
				return Corpus{}, err
			}
			corpus.Scenarios = append(corpus.Scenarios, scenario)
		}
	}
	hash, err := corpusDigest(corpus)
	if err != nil {
		return Corpus{}, err
	}
	corpus.Hash = hash
	if err := corpus.Validate(); err != nil {
		return Corpus{}, err
	}
	return corpus, nil
}

func generateScenario(selectedTopic topic, fanout, depth int, difficulty, language string) (Scenario, error) {
	id := fmt.Sprintf("%s-f%02d-d%d-%s-%s", selectedTopic.Slug, fanout, depth, difficulty, language)
	shuffleSeed := seedFor(id)
	databaseID := stableID("db_", id, "database")
	tableID := stableID("tbl_", id, "table")
	scenario := Scenario{
		ID: id, Topic: selectedTopic.Slug, Difficulty: difficulty, Language: language,
		Fanout: fanout, Depth: depth, ShuffleSeed: shuffleSeed,
		Database: Database{DatabaseID: databaseID, Name: "benchmark_" + selectedTopic.Slug,
			Purpose: "Frozen semantic navigation benchmark / 冻结语义导航基准",
			Scope:   "Synthetic Route-only evaluation fixtures / 合成 Route 评测数据"},
		Table: Table{TableID: tableID, DatabaseID: databaseID, Name: "knowledge_routes",
			Purpose:      "Short semantic modules for Route retrieval evaluation",
			RowSemantics: "One synthetic benchmark answer locator", RouteRevision: 1, RootRouteIDs: []string{}},
		Nodes: []Node{}, Question: questionFor(selectedTopic, depth, difficulty, language),
		Limits: Limits{MaxFallbacks: 4, MaxToolCalls: 64, MaxContextCharacters: 12000,
			MaxElapsedMillis: 600000, StopCondition: "rowid_selected"},
		Expected: Expected{Match: difficulty != "negative", Path: []string{}},
	}
	if difficulty == "negative" {
		scenario.Limits.StopCondition = "no_matching_route"
	}
	chain := selectedTopic.Chain[len(selectedTopic.Chain)-depth:]
	targetIDs := make([]string, depth)
	for level := range chain {
		targetIDs[level] = stableID("route_", id, "target", fmt.Sprint(level+1), chain[level].Slug)
	}
	nodeIndex := map[string]int{}
	levelRouteIDs := make([][]string, depth)
	for level := 1; level <= depth; level++ {
		parentID := ""
		if level > 1 {
			parentID = targetIDs[level-2]
		}
		terms := decoysFor(selectedTopic, shuffleSeed, level, fanout-1)
		siblings := make([]Node, 0, fanout)
		targetTerm := chain[level-1]
		siblings = append(siblings, makeNode(id, targetIDs[level-1], parentID, level,
			targetTerm, level < depth, "target"))
		for decoyIndex, decoy := range terms {
			routeID := stableID("route_", id, "decoy", fmt.Sprint(level), fmt.Sprint(decoyIndex), decoy.Slug)
			siblings = append(siblings, makeNode(id, routeID, parentID, level, decoy, level < depth,
				fmt.Sprintf("decoy-%d", decoyIndex)))
		}
		shuffleNodes(siblings, shuffleSeed^uint64(level)*0x9e3779b97f4a7c15)
		levelRouteIDs[level-1] = make([]string, 0, len(siblings))
		for _, node := range siblings {
			levelRouteIDs[level-1] = append(levelRouteIDs[level-1], node.RouteID)
			nodeIndex[node.RouteID] = len(scenario.Nodes)
			scenario.Nodes = append(scenario.Nodes, node)
		}
		if level < depth {
			for _, sibling := range siblings {
				if sibling.RouteID == targetIDs[level-1] {
					continue
				}
				leafID := stableID("route_", id, "deadend", fmt.Sprint(level), sibling.RouteID)
				leafTerm := term{Slug: "specific-notes", English: sibling.Name + " notes",
					Chinese: sibling.Aliases[0] + "笔记", Purpose: "Specific distractor notes / 具体干扰笔记"}
				leaf := makeNode(id, leafID, sibling.RouteID, level+1, leafTerm, false, leafID)
				parentPosition := nodeIndex[sibling.RouteID]
				scenario.Nodes[parentPosition].ChildRouteIDs = []string{leafID}
				nodeIndex[leafID] = len(scenario.Nodes)
				scenario.Nodes = append(scenario.Nodes, leaf)
			}
		}
	}
	for level := 1; level < depth; level++ {
		position := nodeIndex[targetIDs[level-1]]
		scenario.Nodes[position].ChildRouteIDs = append([]string(nil), levelRouteIDs[level]...)
	}
	scenario.Table.RootRouteIDs = append([]string(nil), levelRouteIDs[0]...)
	if scenario.Expected.Match {
		scenario.Expected.Path = append([]string(nil), targetIDs...)
		targetLeaf := scenario.Nodes[nodeIndex[targetIDs[len(targetIDs)-1]]]
		scenario.Expected.RowID = targetLeaf.RowIDs[0]
	}
	snapshot, err := scenarioDigest(scenario)
	if err != nil {
		return Scenario{}, err
	}
	scenario.Snapshot = snapshot
	if err := scenario.Validate(); err != nil {
		return Scenario{}, err
	}
	return scenario, nil
}

func makeNode(scenarioID, routeID, parentID string, level int, value term, branch bool, identity string) Node {
	kind := "leaf"
	children, rows := []string{}, []string{stableID("row_", scenarioID, identity, value.Slug)}
	if branch {
		kind, rows = "branch", []string{}
	}
	return Node{
		RouteID: routeID, ParentRouteID: parentID, Level: level, Kind: kind,
		Name: value.English, Aliases: []string{value.Chinese, strings.ReplaceAll(value.Slug, "-", " ")},
		Purpose: value.Purpose, Revision: 1, ChildRouteIDs: children, RowIDs: rows,
	}
}

func decoysFor(selected topic, seed uint64, level, count int) []term {
	excluded := map[string]bool{}
	for _, value := range selected.Chain {
		excluded[value.Slug] = true
	}
	pool := append([]term(nil), decoyTerms()...)
	for _, other := range topics() {
		if other.Slug == selected.Slug {
			continue
		}
		pool = append(pool, other.Chain...)
	}
	filtered := make([]term, 0, len(pool))
	seen := map[string]bool{}
	for _, value := range pool {
		if !excluded[value.Slug] && !seen[value.Slug] {
			seen[value.Slug] = true
			filtered = append(filtered, value)
		}
	}
	shuffleTerms(filtered, seed^uint64(level)*0xd1b54a32d192ed03)
	return append([]term(nil), filtered[:count]...)
}

func questionFor(selected topic, depth int, difficulty, language string) string {
	leaf := selected.Chain[len(selected.Chain)-1]
	if difficulty == "negative" {
		switch language {
		case "zh":
			return "查找家庭旅行的酒店入住与早餐安排。"
		case "en":
			return "Find the family travel plan for hotel check-in and breakfast."
		default:
			return "查找 family travel 的 hotel check-in 和早餐安排。"
		}
	}
	switch language {
	case "zh":
		switch difficulty {
		case "separated":
			return fmt.Sprintf("请找到关于%s的记录。", leaf.Chinese)
		case "related":
			return selected.RelatedZH
		case "boundary_overlap":
			return selected.OverlapZH
		default:
			return selected.SynonymZH
		}
	case "en":
		switch difficulty {
		case "separated":
			return fmt.Sprintf("Find the record about %s.", leaf.English)
		case "related":
			return selected.RelatedEN
		case "boundary_overlap":
			return selected.OverlapEN
		default:
			return selected.SynonymEN
		}
	default:
		return fmt.Sprintf("请定位 %s / %s 的边界记录；只沿语义 Route 导航。", leaf.Chinese, leaf.English)
	}
}

func stableID(prefix string, parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return prefix + fmt.Sprintf("%x", digest[:16])
}

func seedFor(value string) uint64 {
	digest := sha256.Sum256([]byte(value))
	seed := binary.BigEndian.Uint64(digest[:8])
	if seed == 0 {
		return generatorSeed
	}
	return seed
}

func shuffleNodes(values []Node, seed uint64) {
	shuffle(len(values), seed, func(left, right int) { values[left], values[right] = values[right], values[left] })
}

func shuffleTerms(values []term, seed uint64) {
	shuffle(len(values), seed, func(left, right int) { values[left], values[right] = values[right], values[left] })
}

func shuffle(length int, seed uint64, swap func(int, int)) {
	state := seed
	for index := length - 1; index > 0; index-- {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		swap(index, int(state%uint64(index+1)))
	}
}
