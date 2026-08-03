package answercorpus

import "sort"

const sourceID = "source_memora_synthetic_v1"

func Generate() (Bundle, error) {
	fixture := generatedFixture()
	fixtureHash, err := fixtureDigest(fixture)
	if err != nil {
		return Bundle{}, err
	}
	fixture.SnapshotSHA256 = fixtureHash
	truth := generatedGroundTruth(fixtureHash)
	truthHash, err := truthDigest(truth)
	if err != nil {
		return Bundle{}, err
	}
	truth.Hash = truthHash
	manifest := Manifest{
		Version: ManifestVersion, CorpusID: "answer-retrieval-v1", Revision: 1,
		License: License{
			ID: "license_memora", SPDXID: "PolyForm-Noncommercial-1.0.0",
			Path: "LICENSE", RequiredNotice: requiredNotice,
		},
		Sources: []Source{{
			ID: sourceID, Kind: "synthetic", Title: "Memora synthetic answer benchmark v1",
			Locator: "repo:internal/answercorpus/generator.go", LicenseID: "license_memora",
			ContentSHA256: fixtureHash,
		}},
		Fixture: fixture, Cases: generatedCases(), GroundTruthSHA256: truthHash,
	}
	manifestHash, err := manifestDigest(manifest)
	if err != nil {
		return Bundle{}, err
	}
	manifest.Hash = manifestHash
	bundle := Bundle{Manifest: manifest, GroundTruth: truth}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func generatedFixture() Fixture {
	databases := []FixtureDatabase{
		personalDatabase(), projectsDatabase(), readingDatabase(),
	}
	sort.Slice(databases, func(left, right int) bool { return databases[left].ID < databases[right].ID })
	return Fixture{
		Version: FixtureVersion, SnapshotID: "answer-retrieval-v1-snapshot", Databases: databases,
	}
}

func personalDatabase() FixtureDatabase {
	rows := []FixtureRow{
		fixtureRow("row_personal_coffee", map[string]string{
			"col_reason": "It keeps the flavor clear without excessive bitterness",
			"col_topic":  "Coffee preference", "col_value": "Light-roast pour-over",
		}),
		fixtureRow("row_personal_editor", map[string]string{
			"col_reason": "Fast keyboard-driven editing with a small local footprint",
			"col_topic":  "Preferred editor", "col_value": "Neovim",
		}),
		fixtureRow("row_personal_language", map[string]string{
			"col_reason": "Discussion stays natural while technical names remain unambiguous",
			"col_topic":  "Working language", "col_value": "Chinese for discussion, English for code identifiers",
		}),
		fixtureRow("row_personal_timezone", map[string]string{
			"col_reason": "Daily planning follows China Standard Time",
			"col_topic":  "Primary time zone", "col_value": "Asia/Shanghai",
		}),
	}
	table := FixtureTable{
		ID: "tbl_preferences", DatabaseID: "db_personal", Name: "preferences",
		Purpose: "Stable personal working preferences", RowSemantics: "One current preference with its reason",
		Columns: []FixtureColumn{
			fixtureColumn("col_reason", "reason", "Why the preference is useful", "rationale"),
			fixtureColumn("col_topic", "topic", "Preference subject", "title"),
			fixtureColumn("col_value", "value", "Current preferred value", "fact"),
		},
		Rows: rows,
		Routes: fixtureRoutes("personal", []routeSeed{
			{"coffee", "Coffee", []string{"brew", "咖啡"}, "Beverage and brewing preference", "row_personal_coffee"},
			{"editor", "Editor", []string{"coding tool", "编辑器"}, "Text and code editing preference", "row_personal_editor"},
			{"language", "Language", []string{"communication", "沟通语言"}, "Discussion and identifier language", "row_personal_language"},
			{"timezone", "Time zone", []string{"schedule", "时区"}, "Scheduling and local time preference", "row_personal_timezone"},
		}),
	}
	return FixtureDatabase{
		ID: "db_personal", Name: "personal", Purpose: "Durable personal preferences",
		Scope: "Current working and daily defaults", Tables: []FixtureTable{table},
	}
}

func projectsDatabase() FixtureDatabase {
	rows := []FixtureRow{
		fixtureRow("row_projects_admin", map[string]string{
			"col_decision":  "Bind Admin to 127.0.0.1:3888",
			"col_rationale": "One predictable loopback endpoint avoids random-port confusion",
			"col_status":    "accepted", "col_topic": "Admin listener",
		}),
		fixtureRow("row_projects_indexing", map[string]string{
			"col_decision":  "Index all live Row content and semantic surfaces in rebuildable lexical postings",
			"col_rationale": "Lexical hits remain disposable locations while SQL Rows stay authoritative",
			"col_status":    "accepted", "col_topic": "Lexical coverage",
		}),
		fixtureRow("row_projects_runtime", map[string]string{
			"col_decision":  "Use a Memora-owned thin Go loop",
			"col_rationale": "Avoid framework size and keep Provider, MSQL, and Trace contracts authoritative",
			"col_status":    "accepted", "col_topic": "Agent runtime",
		}),
		fixtureRow("row_projects_vector", map[string]string{
			"col_decision":  "Start with CPU exact vector matching and defer HNSW",
			"col_rationale": "Measured Route-scale exact matching is already within the latency budget",
			"col_status":    "accepted", "col_topic": "Vector retrieval",
		}),
	}
	table := FixtureTable{
		ID: "tbl_decisions", DatabaseID: "db_projects", Name: "decisions",
		Purpose: "Accepted architecture and product decisions", RowSemantics: "One current decision with rationale",
		Columns: []FixtureColumn{
			fixtureColumn("col_decision", "decision", "Accepted decision statement", "fact"),
			fixtureColumn("col_rationale", "rationale", "Reason supporting the decision", "rationale"),
			fixtureColumn("col_status", "status", "Decision lifecycle state", "status"),
			fixtureColumn("col_topic", "topic", "Decision subject", "title"),
		},
		Rows: rows,
		Routes: fixtureRoutes("projects", []routeSeed{
			{"admin", "Admin", []string{"local UI", "管理页面"}, "Admin serving and access decisions", "row_projects_admin"},
			{"indexing", "Indexing", []string{"lexical postings", "倒排索引"}, "Lexical coverage and authority decisions", "row_projects_indexing"},
			{"runtime", "Runtime", []string{"agent loop", "运行时"}, "Embedded Agent runtime decisions", "row_projects_runtime"},
			{"vector", "Vector", []string{"similarity", "向量检索"}, "Vector matching and index decisions", "row_projects_vector"},
		}),
	}
	return FixtureDatabase{
		ID: "db_projects", Name: "projects", Purpose: "Project architecture knowledge",
		Scope: "Accepted current technical decisions", Tables: []FixtureTable{table},
	}
}

func readingDatabase() FixtureDatabase {
	rows := []FixtureRow{
		fixtureRow("row_reading_dune", map[string]string{
			"col_author": "Frank Herbert", "col_takeaway": "Power and ecology shape institutions",
			"col_title": "Dune",
		}),
		fixtureRow("row_reading_left_hand", map[string]string{
			"col_author": "Ursula K. Le Guin", "col_takeaway": "Culture changes how identity and loyalty are understood",
			"col_title": "The Left Hand of Darkness",
		}),
		fixtureRow("row_reading_snow_crash", map[string]string{
			"col_author": "Neal Stephenson", "col_takeaway": "Language, software, and power can reinforce each other",
			"col_title": "Snow Crash",
		}),
		fixtureRow("row_reading_three_body", map[string]string{
			"col_author": "Liu Cixin", "col_takeaway": "Civilizations reason under uncertainty and existential risk",
			"col_title": "The Three-Body Problem",
		}),
	}
	table := FixtureTable{
		ID: "tbl_books", DatabaseID: "db_reading", Name: "books",
		Purpose: "Read books and compact takeaways", RowSemantics: "One book with author and reviewed takeaway",
		Columns: []FixtureColumn{
			fixtureColumn("col_author", "author", "Book author", "fact"),
			fixtureColumn("col_takeaway", "takeaway", "Reviewed reading takeaway", "summary"),
			fixtureColumn("col_title", "title", "Book title", "title"),
		},
		Rows: rows,
		Routes: fixtureRoutes("reading", []routeSeed{
			{"dune", "Dune", []string{"Arrakis", "沙丘"}, "Dune reading record", "row_reading_dune"},
			{"left_hand", "The Left Hand of Darkness", []string{"Gethen", "黑暗的左手"}, "Le Guin reading record", "row_reading_left_hand"},
			{"snow_crash", "Snow Crash", []string{"Metaverse", "雪崩"}, "Stephenson reading record", "row_reading_snow_crash"},
			{"three_body", "The Three-Body Problem", []string{"Trisolaris", "三体"}, "Liu Cixin reading record", "row_reading_three_body"},
		}),
	}
	return FixtureDatabase{
		ID: "db_reading", Name: "reading", Purpose: "Reviewed reading memory",
		Scope: "Books, authors, and compact takeaways", Tables: []FixtureTable{table},
	}
}

func generatedCases() []Case {
	budget := defaultCaseBudget()
	return []Case{
		{ID: "case-001", Language: "zh", Category: "direct", Question: "内置查询 Agent 第一版最终选择了什么运行时方案？", AuthorizedDatabases: []string{"projects"}, Budget: budget},
		{ID: "case-002", Language: "en", Category: "paraphrase", Question: "How should vector lookup begin before an approximate index is justified?", AuthorizedDatabases: []string{"projects"}, Budget: budget},
		{ID: "case-003", Language: "mixed", Category: "direct", Question: "Admin 的固定 loopback 地址和端口是什么？", AuthorizedDatabases: []string{"projects"}, Budget: budget},
		{ID: "case-004", Language: "zh", Category: "paraphrase", Question: "倒排检索应该覆盖哪些持久内容？", AuthorizedDatabases: []string{"projects"}, Budget: budget},
		{ID: "case-005", Language: "en", Category: "direct", Question: "Which time zone should scheduling use?", AuthorizedDatabases: []string{"personal"}, Budget: budget},
		{ID: "case-006", Language: "zh", Category: "direct", Question: "日常写代码偏好使用哪个编辑器？", AuthorizedDatabases: []string{"personal"}, Budget: budget},
		{ID: "case-007", Language: "mixed", Category: "paraphrase", Question: "讨论和 code identifier 分别偏好使用什么语言？", AuthorizedDatabases: []string{"personal"}, Budget: budget},
		{ID: "case-008", Language: "en", Category: "direct", Question: "Who wrote the desert-planet novel in the reading list?", AuthorizedDatabases: []string{"reading"}, Budget: budget},
		{ID: "case-009", Language: "zh", Category: "paraphrase", Question: "哪本书的笔记强调文化会改变人们理解身份与忠诚的方式？", AuthorizedDatabases: []string{"reading"}, Budget: budget},
		{ID: "case-010", Language: "zh", Category: "multi_fact", Question: "对比 Agent 运行时和向量检索，两项决策分别怎样控制复杂度？", AuthorizedDatabases: []string{"projects"}, Budget: budget},
		{ID: "case-011", Language: "en", Category: "multi_fact", Question: "Name the authors of the cyberpunk Metaverse book and the first-contact trilogy entry.", AuthorizedDatabases: []string{"reading"}, Budget: budget},
		{ID: "case-012", Language: "zh", Category: "negative", Question: "当前数据库记录了应该购买哪一款 GPU 吗？", AuthorizedDatabases: []string{"personal", "projects"}, Budget: budget},
	}
}

func generatedGroundTruth(snapshot string) GroundTruth {
	return GroundTruth{
		Version: GroundTruthVersion, CorpusID: "answer-retrieval-v1", SnapshotSHA256: snapshot,
		Cases: []ExpectedCase{
			answer("case-001", "第一版采用 Memora 自有的轻量 Go 循环。", projectFact("row_projects_runtime", "col_decision", "Use a Memora-owned thin Go loop")),
			answer("case-002", "Vector lookup starts with exact CPU matching; HNSW is deferred.", projectFact("row_projects_vector", "col_decision", "Start with CPU exact vector matching and defer HNSW")),
			answer("case-003", "Admin 固定绑定到 127.0.0.1:3888。", projectFact("row_projects_admin", "col_decision", "Bind Admin to 127.0.0.1:3888")),
			answer("case-004", "倒排索引覆盖所有 live Row 正文和语义表面，并保持可重建。", projectFact("row_projects_indexing", "col_decision", "Index all live Row content and semantic surfaces in rebuildable lexical postings")),
			answer("case-005", "Scheduling should use Asia/Shanghai.", personalFact("row_personal_timezone", "col_value", "Asia/Shanghai")),
			answer("case-006", "偏好的编辑器是 Neovim。", personalFact("row_personal_editor", "col_value", "Neovim")),
			answer("case-007", "讨论使用中文，代码标识符使用英文。", personalFact("row_personal_language", "col_value", "Chinese for discussion, English for code identifiers")),
			answer("case-008", "Frank Herbert wrote it.", readingFact("row_reading_dune", "col_author", "Frank Herbert")),
			answer("case-009", "这条笔记来自《黑暗的左手》。", readingFact("row_reading_left_hand", "col_title", "The Left Hand of Darkness")),
			answer("case-010", "运行时采用自有轻量 Go loop；向量检索先用 CPU 精确匹配并延后 HNSW。",
				projectFact("row_projects_runtime", "col_decision", "Use a Memora-owned thin Go loop"),
				projectFact("row_projects_vector", "col_decision", "Start with CPU exact vector matching and defer HNSW")),
			answer("case-011", "They are Neal Stephenson and Liu Cixin.",
				readingFact("row_reading_snow_crash", "col_author", "Neal Stephenson"),
				readingFact("row_reading_three_body", "col_author", "Liu Cixin")),
			{CaseID: "case-012", Kind: "unanswerable", ReferenceAnswer: "该快照没有记录 GPU 型号。", Facts: []ExpectedFact{}},
		},
	}
}

type routeSeed struct {
	Slug, Name string
	Aliases    []string
	Purpose    string
	RowID      string
}

func fixtureRoutes(namespace string, seeds []routeSeed) []FixtureRoute {
	routes := []FixtureRoute{{
		ID: "route_" + namespace + "_00_root", Kind: "branch", Name: "All " + namespace,
		Aliases: []string{"overview", "总览"}, Purpose: "Root navigation for the synthetic fixture", Revision: 1,
	}}
	for index, seed := range seeds {
		routes = append(routes, FixtureRoute{
			ID: routeID(namespace, index+1, seed.Slug), ParentID: routes[0].ID, Kind: "leaf",
			Name: seed.Name, Aliases: append([]string(nil), seed.Aliases...), Purpose: seed.Purpose,
			Revision: 1, RowID: seed.RowID,
		})
	}
	sort.Slice(routes, func(left, right int) bool { return routes[left].ID < routes[right].ID })
	return routes
}

func routeID(namespace string, ordinal int, slug string) string {
	digit := byte('0' + ordinal)
	return "route_" + namespace + "_1" + string(digit) + "_" + slug
}

func fixtureColumn(id, name, purpose, role string) FixtureColumn {
	return FixtureColumn{ID: id, Name: name, Type: "TEXT(4000)", Purpose: purpose, SemanticRole: role}
}

func fixtureRow(id string, values map[string]string) FixtureRow {
	return FixtureRow{ID: id, SourceID: sourceID, Values: values}
}

func defaultCaseBudget() CaseBudget {
	return CaseBudget{
		MaxProviderCalls: 4, MaxToolCalls: 3, MaxOutputTokens: 2048,
		BootstrapFrameUTF8Bytes: 12000, ToolResultUTF8Bytes: 65536,
	}
}

func answer(caseID, reference string, facts ...ExpectedFact) ExpectedCase {
	sort.Slice(facts, func(left, right int) bool {
		return factKey(facts[left]) < factKey(facts[right])
	})
	return ExpectedCase{CaseID: caseID, Kind: "answerable", ReferenceAnswer: reference, Facts: facts}
}

func projectFact(rowID, columnID, value string) ExpectedFact {
	return ExpectedFact{DatabaseID: "db_projects", TableID: "tbl_decisions", RowID: rowID, ColumnID: columnID, Value: value}
}

func personalFact(rowID, columnID, value string) ExpectedFact {
	return ExpectedFact{DatabaseID: "db_personal", TableID: "tbl_preferences", RowID: rowID, ColumnID: columnID, Value: value}
}

func readingFact(rowID, columnID, value string) ExpectedFact {
	return ExpectedFact{DatabaseID: "db_reading", TableID: "tbl_books", RowID: rowID, ColumnID: columnID, Value: value}
}

func factKey(fact ExpectedFact) string {
	return fact.DatabaseID + "\x00" + fact.TableID + "\x00" + fact.RowID + "\x00" + fact.ColumnID
}
