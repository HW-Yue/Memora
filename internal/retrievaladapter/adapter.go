package retrievaladapter

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HW-Yue/Memora/internal/retrievalscore"
)

type Config struct {
	Kind      string
	Root      string
	Split     string
	Domain    string
	QueryMode string
}

func Build(config Config) (retrievalscore.Suite, error) {
	if config.Root == "" || !filepath.IsAbs(config.Root) || filepath.Clean(config.Root) != config.Root {
		return retrievalscore.Suite{}, errors.New("retrieval adapter root must be absolute and normalized")
	}
	switch config.Kind {
	case "miracl-zh":
		return buildMIRACL(config)
	case "mtrag":
		return buildMTRAG(config)
	default:
		return retrievalscore.Suite{}, errors.New("retrieval adapter kind is unsupported")
	}
}

func buildMIRACL(config Config) (retrievalscore.Suite, error) {
	if config.Split != "dev" && config.Split != "train" {
		return retrievalscore.Suite{}, errors.New("MIRACL split must be dev or train")
	}
	root := filepath.Join(config.Root, "topics", "topics.miracl-v1.0-zh-"+config.Split+".tsv")
	qrelsPath := filepath.Join(config.Root, "qrels", "qrels.miracl-v1.0-zh-"+config.Split+".tsv")
	queries, err := readMIRACLTopics(root, "miracl-zh-"+config.Split)
	if err != nil {
		return retrievalscore.Suite{}, err
	}
	qrels, positive, err := readMIRACLQrels(qrelsPath)
	if err != nil {
		return retrievalscore.Suite{}, err
	}
	if err := ensureQrelQueries(queries, qrels); err != nil {
		return retrievalscore.Suite{}, err
	}
	queries = filterQueries(queries, positive)
	return sealSuite(retrievalscore.Suite{Version: retrievalscore.SuiteVersion, SuiteID: "miracl-zh-" + config.Split,
		DatasetID: "miracl-zh-v1", Split: config.Split, Queries: queries, Qrels: qrels})
}

func buildMTRAG(config Config) (retrievalscore.Suite, error) {
	if config.Domain == "" || config.QueryMode != "lastturn" && config.QueryMode != "rewrite" {
		return retrievalscore.Suite{}, errors.New("MTRAG domain and query-mode are required")
	}
	if config.Domain != "clapnq" && config.Domain != "cloud" && config.Domain != "fiqa" && config.Domain != "govt" {
		return retrievalscore.Suite{}, errors.New("MTRAG domain is unsupported")
	}
	queriesPath := filepath.Join(config.Root, "mtrag-human", "retrieval_tasks", config.Domain,
		config.Domain+"_"+config.QueryMode+".jsonl")
	qrelsPath := filepath.Join(config.Root, "mtrag-human", "retrieval_tasks", config.Domain, "qrels", "dev.tsv")
	qrels, positive, err := readMTRAGQrels(qrelsPath)
	if err != nil {
		return retrievalscore.Suite{}, err
	}
	queries, err := readMTRAGQueries(queriesPath, config.Domain, positive)
	if err != nil {
		return retrievalscore.Suite{}, err
	}
	return sealSuite(retrievalscore.Suite{Version: retrievalscore.SuiteVersion, SuiteID: "mtrag-" + config.Domain + "-" + config.QueryMode,
		DatasetID: "mtrag-human-v1", Split: "dev", Queries: queries, Qrels: qrels})
}

func readMIRACLTopics(path, group string) ([]retrievalscore.Query, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	queries := make([]retrievalscore.Query, 0)
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		fields := strings.Split(scanner.Text(), "\t")
		if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
			return nil, errors.New("MIRACL topic line is invalid")
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return nil, errors.New("MIRACL topic is duplicated")
		}
		seen[fields[0]] = struct{}{}
		queries = append(queries, retrievalscore.Query{QueryID: fields[0], Group: group, Text: fields[1]})
	}
	return queries, scanner.Err()
}

func readMIRACLQrels(path string) ([]retrievalscore.Qrel, map[string]bool, error) {
	return readQrels(path, false)
}

func readMTRAGQrels(path string) ([]retrievalscore.Qrel, map[string]bool, error) {
	return readQrels(path, true)
}

func readQrels(path string, header bool) ([]retrievalscore.Qrel, map[string]bool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = file.Close() }()
	qrels := make([]retrievalscore.Qrel, 0)
	positive := map[string]bool{}
	pairs := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if line == 1 && header && strings.HasPrefix(text, "query-id\t") {
			continue
		}
		fields := strings.Split(text, "\t")
		documentIndex, relevanceIndex := 2, 3
		if header {
			documentIndex, relevanceIndex = 1, 2
		}
		if len(fields) <= relevanceIndex || fields[0] == "" || fields[documentIndex] == "" {
			return nil, nil, fmt.Errorf("qrel line %d is invalid", line)
		}
		relevance, err := strconv.Atoi(fields[relevanceIndex])
		if err != nil || relevance < 0 {
			return nil, nil, fmt.Errorf("qrel line %d relevance is invalid", line)
		}
		pair := fields[0] + "\x00" + fields[documentIndex]
		if _, duplicate := pairs[pair]; duplicate {
			return nil, nil, fmt.Errorf("qrel line %d is duplicated", line)
		}
		pairs[pair] = struct{}{}
		qrels = append(qrels, retrievalscore.Qrel{QueryID: fields[0], DocumentID: fields[documentIndex], Relevance: relevance})
		if relevance > 0 {
			positive[fields[0]] = true
		}
	}
	return qrels, positive, scanner.Err()
}

func readMTRAGQueries(path, group string, positive map[string]bool) ([]retrievalscore.Query, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	queries := make([]retrievalscore.Query, 0)
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4<<20)
	for scanner.Scan() {
		var value struct {
			ID   string `json:"_id"`
			Text string `json:"text"`
		}
		if err := json.Unmarshal([]byte(scanner.Text()), &value); err != nil || value.ID == "" || value.Text == "" {
			return nil, errors.New("MTRAG query line is invalid")
		}
		value.Text = strings.TrimSpace(value.Text)
		if value.Text == "" {
			return nil, errors.New("MTRAG query text is empty")
		}
		if !positive[value.ID] {
			continue
		}
		if _, duplicate := seen[value.ID]; duplicate {
			return nil, errors.New("MTRAG query is duplicated")
		}
		seen[value.ID] = struct{}{}
		queries = append(queries, retrievalscore.Query{QueryID: value.ID, Group: group, Text: value.Text})
	}
	for queryID := range positive {
		if _, exists := seen[queryID]; !exists {
			return nil, fmt.Errorf("MTRAG qrel query %q is missing from query file", queryID)
		}
	}
	return queries, scanner.Err()
}

func ensureQrelQueries(queries []retrievalscore.Query, qrels []retrievalscore.Qrel) error {
	known := make(map[string]struct{}, len(queries))
	for _, query := range queries {
		known[query.QueryID] = struct{}{}
	}
	for _, qrel := range qrels {
		if _, exists := known[qrel.QueryID]; !exists {
			return fmt.Errorf("qrel query %q is missing from topics", qrel.QueryID)
		}
	}
	return nil
}

func filterQueries(queries []retrievalscore.Query, positive map[string]bool) []retrievalscore.Query {
	filtered := make([]retrievalscore.Query, 0, len(queries))
	for _, query := range queries {
		if positive[query.QueryID] {
			filtered = append(filtered, query)
		}
	}
	return filtered
}

func sealSuite(suite retrievalscore.Suite) (retrievalscore.Suite, error) {
	if len(suite.Queries) == 0 {
		return retrievalscore.Suite{}, errors.New("retrieval adapter produced no answerable queries")
	}
	if err := suite.Seal(); err != nil {
		return retrievalscore.Suite{}, err
	}
	return suite, nil
}
