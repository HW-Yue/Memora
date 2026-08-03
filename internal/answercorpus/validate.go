package answercorpus

import (
	"encoding/hex"
	"sort"
	"strings"
	"unicode/utf8"
)

const requiredNotice = "Copyright 2026 HW-Yue. Commercial use requires a separate paid commercial license from the copyright holder. Commercial licensing inquiries: https://github.com/HW-Yue/Memora"

func (manifest Manifest) Validate() error {
	if manifest.Version != ManifestVersion || manifest.CorpusID != "answer-retrieval-v1" ||
		manifest.Revision != 1 || len(manifest.Cases) != 12 || len(manifest.Sources) != 1 ||
		!validDigest(manifest.GroundTruthSHA256) || !validDigest(manifest.Hash) {
		return corpusError("manifest identity is invalid")
	}
	if err := manifest.License.validate(); err != nil {
		return err
	}
	if err := manifest.Fixture.Validate(); err != nil {
		return err
	}
	source := manifest.Sources[0]
	if !validID(source.ID, "source_") || source.Kind != "synthetic" ||
		!validText(source.Title, 240) || source.Locator != "repo:internal/answercorpus/generator.go" ||
		source.LicenseID != manifest.License.ID || source.ContentSHA256 != manifest.Fixture.SnapshotSHA256 {
		return corpusError("source metadata is invalid")
	}
	databaseNames := make(map[string]bool, len(manifest.Fixture.Databases))
	for _, database := range manifest.Fixture.Databases {
		if databaseNames[database.Name] {
			return corpusError("fixture Database names are duplicated")
		}
		databaseNames[database.Name] = true
		for _, table := range database.Tables {
			for _, row := range table.Rows {
				if row.SourceID != source.ID {
					return corpusError("fixture Row %q references an unknown source", row.ID)
				}
			}
		}
	}
	languages, categories := map[string]bool{}, map[string]bool{}
	previousID := ""
	for _, testCase := range manifest.Cases {
		if previousID >= testCase.ID {
			return corpusError("manifest cases are not in stable ID order or are duplicated")
		}
		previousID = testCase.ID
		if err := testCase.validate(databaseNames); err != nil {
			return err
		}
		languages[testCase.Language], categories[testCase.Category] = true, true
	}
	for _, value := range []string{"en", "mixed", "zh"} {
		if !languages[value] {
			return corpusError("language coverage is incomplete")
		}
	}
	for _, value := range []string{"direct", "multi_fact", "negative", "paraphrase"} {
		if !categories[value] {
			return corpusError("category coverage is incomplete")
		}
	}
	want, err := manifestDigest(manifest)
	if err != nil {
		return err
	}
	if manifest.Hash != want {
		return corpusError("manifest hash mismatch")
	}
	return nil
}

func (license License) validate() error {
	if license.ID != "license_memora" || license.SPDXID != "PolyForm-Noncommercial-1.0.0" ||
		license.Path != "LICENSE" || license.RequiredNotice != requiredNotice {
		return corpusError("license metadata is invalid")
	}
	return nil
}

func (fixture Fixture) Validate() error {
	if fixture.Version != FixtureVersion || fixture.SnapshotID != "answer-retrieval-v1-snapshot" ||
		!validDigest(fixture.SnapshotSHA256) || len(fixture.Databases) != 3 {
		return corpusError("fixture identity is invalid")
	}
	previousDatabase := ""
	for _, database := range fixture.Databases {
		if previousDatabase >= database.ID {
			return corpusError("fixture Databases are not in stable ID order or are duplicated")
		}
		previousDatabase = database.ID
		if err := database.validate(); err != nil {
			return err
		}
	}
	want, err := fixtureDigest(fixture)
	if err != nil {
		return err
	}
	if fixture.SnapshotSHA256 != want {
		return corpusError("fixture snapshot hash mismatch")
	}
	return nil
}

func (database FixtureDatabase) validate() error {
	if !validID(database.ID, "db_") || !validName(database.Name) || !validText(database.Purpose, 1000) ||
		!validText(database.Scope, 1000) || len(database.Tables) != 1 {
		return corpusError("fixture Database %q is invalid", database.ID)
	}
	previousTable := ""
	for _, table := range database.Tables {
		if previousTable >= table.ID {
			return corpusError("fixture Tables are not in stable ID order or are duplicated")
		}
		previousTable = table.ID
		if err := table.validate(database.ID); err != nil {
			return err
		}
	}
	return nil
}

func (table FixtureTable) validate(databaseID string) error {
	if !validID(table.ID, "tbl_") || table.DatabaseID != databaseID || !validName(table.Name) ||
		!validText(table.Purpose, 1000) || !validText(table.RowSemantics, 1000) ||
		len(table.Columns) < 3 || len(table.Rows) != 4 || len(table.Routes) != len(table.Rows)+1 {
		return corpusError("fixture Table %q is invalid", table.ID)
	}
	columns := map[string]bool{}
	previousColumn := ""
	for _, column := range table.Columns {
		if previousColumn >= column.ID || !validID(column.ID, "col_") || !validName(column.Name) ||
			!validText(column.Type, 80) || !validText(column.Purpose, 500) || !validName(column.SemanticRole) {
			return corpusError("fixture Table %q Columns are invalid or out of order", table.ID)
		}
		previousColumn, columns[column.ID] = column.ID, true
	}
	rows := map[string]FixtureRow{}
	previousRow := ""
	for _, row := range table.Rows {
		if previousRow >= row.ID || !validID(row.ID, "row_") || !validID(row.SourceID, "source_") ||
			len(row.Values) != len(columns) {
			return corpusError("fixture Table %q Rows are invalid or out of order", table.ID)
		}
		previousRow = row.ID
		for columnID, value := range row.Values {
			if !columns[columnID] || !validText(value, 4000) {
				return corpusError("fixture Row %q value is invalid", row.ID)
			}
		}
		rows[row.ID] = row
	}
	routes := map[string]FixtureRoute{}
	previousRoute, rootID := "", ""
	leafRows := map[string]int{}
	for _, route := range table.Routes {
		if previousRoute >= route.ID || !validID(route.ID, "route_") ||
			(route.Kind != "branch" && route.Kind != "leaf") || !validName(route.Name) ||
			!validText(route.Purpose, 1000) || route.Revision != 1 || len(route.Aliases) == 0 ||
			hasDuplicates(route.Aliases) {
			return corpusError("fixture Table %q Routes are invalid or out of order", table.ID)
		}
		previousRoute = route.ID
		if route.ParentID == "" {
			if rootID != "" || route.Kind != "branch" {
				return corpusError("fixture Table %q root Route is invalid", table.ID)
			}
			rootID = route.ID
		} else if !validID(route.ParentID, "route_") {
			return corpusError("fixture Route %q parent is invalid", route.ID)
		}
		if route.Kind == "branch" && route.RowID != "" ||
			route.Kind == "leaf" && rows[route.RowID].ID == "" {
			return corpusError("fixture Route %q Leaf/Row cardinality is invalid", route.ID)
		}
		if route.Kind == "leaf" {
			leafRows[route.RowID]++
		}
		routes[route.ID] = route
	}
	if rootID == "" {
		return corpusError("fixture Table %q has no root Route", table.ID)
	}
	for _, route := range routes {
		if route.ParentID != "" && routes[route.ParentID].ID == "" {
			return corpusError("fixture Route %q references a missing parent", route.ID)
		}
		if !routeReachesRoot(route, routes, rootID) {
			return corpusError("fixture Route %q is disconnected or cyclic", route.ID)
		}
	}
	for rowID := range rows {
		if leafRows[rowID] != 1 {
			return corpusError("fixture Row %q must belong to exactly one Leaf", rowID)
		}
	}
	return nil
}

func routeReachesRoot(route FixtureRoute, routes map[string]FixtureRoute, rootID string) bool {
	seen := map[string]bool{}
	for route.ID != rootID {
		if seen[route.ID] || route.ParentID == "" {
			return false
		}
		seen[route.ID] = true
		route = routes[route.ParentID]
		if route.ID == "" {
			return false
		}
	}
	return true
}

func (testCase Case) validate(databaseNames map[string]bool) error {
	if !validID(testCase.ID, "case-") || !contains([]string{"en", "mixed", "zh"}, testCase.Language) ||
		!contains([]string{"direct", "multi_fact", "negative", "paraphrase"}, testCase.Category) ||
		!validText(testCase.Question, 512) || len(testCase.AuthorizedDatabases) == 0 ||
		!sort.StringsAreSorted(testCase.AuthorizedDatabases) || hasDuplicates(testCase.AuthorizedDatabases) ||
		!testCase.Budget.valid() {
		return corpusError("case %q is invalid", testCase.ID)
	}
	for _, database := range testCase.AuthorizedDatabases {
		if !databaseNames[database] {
			return corpusError("case %q references unknown Database %q", testCase.ID, database)
		}
	}
	return nil
}

func (budget CaseBudget) valid() bool {
	return budget.MaxProviderCalls == 4 && budget.MaxToolCalls == 3 && budget.MaxOutputTokens == 2048 &&
		budget.BootstrapFrameUTF8Bytes == 12000 && budget.ToolResultUTF8Bytes == 65536
}

func (truth GroundTruth) Validate() error {
	if err := truth.validateShape(); err != nil {
		return err
	}
	want, err := truthDigest(truth)
	if err != nil {
		return err
	}
	if truth.Hash != want {
		return corpusError("ground truth hash mismatch")
	}
	return nil
}

func (truth GroundTruth) validateShape() error {
	if truth.Version != GroundTruthVersion || truth.CorpusID != "answer-retrieval-v1" ||
		!validDigest(truth.SnapshotSHA256) || !validDigest(truth.Hash) || len(truth.Cases) != 12 {
		return corpusError("ground truth identity is invalid")
	}
	previousID := ""
	for _, expected := range truth.Cases {
		if previousID >= expected.CaseID {
			return corpusError("ground truth cases are not in stable ID order or are duplicated")
		}
		previousID = expected.CaseID
		if err := expected.validate(); err != nil {
			return err
		}
	}
	return nil
}

func (expected ExpectedCase) validate() error {
	if !validID(expected.CaseID, "case-") || !contains([]string{"answerable", "unanswerable"}, expected.Kind) ||
		!validText(expected.ReferenceAnswer, 2000) || expected.Facts == nil ||
		expected.Kind == "answerable" && len(expected.Facts) == 0 ||
		expected.Kind == "unanswerable" && len(expected.Facts) != 0 {
		return corpusError("ground truth case %q is invalid", expected.CaseID)
	}
	previous := ""
	for _, fact := range expected.Facts {
		key := fact.DatabaseID + "\x00" + fact.TableID + "\x00" + fact.RowID + "\x00" + fact.ColumnID
		if previous >= key || !validID(fact.DatabaseID, "db_") || !validID(fact.TableID, "tbl_") ||
			!validID(fact.RowID, "row_") || !validID(fact.ColumnID, "col_") || !validText(fact.Value, 4000) {
			return corpusError("ground truth case %q facts are invalid or out of order", expected.CaseID)
		}
		previous = key
	}
	return nil
}

func (bundle Bundle) Validate() error {
	if err := bundle.Manifest.Validate(); err != nil {
		return err
	}
	// Validate the structural shape before checking the digest so cross-file
	// authority errors retain a precise diagnostic instead of being hidden by
	// the expected hash mismatch caused by the same mutation.
	if err := bundle.GroundTruth.validateShape(); err != nil {
		return err
	}
	if bundle.GroundTruth.CorpusID != bundle.Manifest.CorpusID ||
		bundle.GroundTruth.SnapshotSHA256 != bundle.Manifest.Fixture.SnapshotSHA256 ||
		bundle.GroundTruth.Hash != bundle.Manifest.GroundTruthSHA256 {
		return corpusError("manifest and ground truth identity do not match")
	}
	caseByID := make(map[string]Case, len(bundle.Manifest.Cases))
	for _, testCase := range bundle.Manifest.Cases {
		caseByID[testCase.ID] = testCase
	}
	facts := fixtureFacts(bundle.Manifest.Fixture)
	databaseNamesByID := make(map[string]string, len(bundle.Manifest.Fixture.Databases))
	for _, database := range bundle.Manifest.Fixture.Databases {
		databaseNamesByID[database.ID] = database.Name
	}
	for _, expected := range bundle.GroundTruth.Cases {
		testCase, exists := caseByID[expected.CaseID]
		if !exists {
			return corpusError("ground truth case %q has no manifest case", expected.CaseID)
		}
		delete(caseByID, expected.CaseID)
		if testCase.Category == "negative" && expected.Kind != "unanswerable" ||
			testCase.Category != "negative" && expected.Kind != "answerable" {
			return corpusError("case %q category and answer kind do not match", expected.CaseID)
		}
		switch testCase.Category {
		case "multi_fact":
			if len(expected.Facts) < 2 {
				return corpusError("case %q must require multiple facts", expected.CaseID)
			}
		case "direct", "paraphrase":
			if len(expected.Facts) != 1 {
				return corpusError("case %q must require exactly one fact", expected.CaseID)
			}
		}
		if strings.Contains(testCase.Question, expected.ReferenceAnswer) {
			return corpusError("case %q question leaks its reference answer", expected.CaseID)
		}
		for _, fact := range expected.Facts {
			key := fact.DatabaseID + "\x00" + fact.TableID + "\x00" + fact.RowID + "\x00" + fact.ColumnID
			if facts[key] != fact.Value {
				return corpusError("case %q fact references missing fixture authority", expected.CaseID)
			}
			if !contains(testCase.AuthorizedDatabases, databaseNamesByID[fact.DatabaseID]) {
				return corpusError("case %q fact is outside its authorized Database scope", expected.CaseID)
			}
			if strings.Contains(testCase.Question, fact.RowID) || strings.Contains(testCase.Question, fact.Value) {
				return corpusError("case %q question leaks a ground-truth fact", expected.CaseID)
			}
		}
	}
	if len(caseByID) != 0 {
		return corpusError("manifest contains cases without ground truth")
	}
	return bundle.GroundTruth.Validate()
}

func fixtureFacts(fixture Fixture) map[string]string {
	values := map[string]string{}
	for _, database := range fixture.Databases {
		for _, table := range database.Tables {
			for _, row := range table.Rows {
				for columnID, value := range row.Values {
					key := database.ID + "\x00" + table.ID + "\x00" + row.ID + "\x00" + columnID
					values[key] = value
				}
			}
		}
	}
	return values
}

func validDigest(value string) bool {
	const prefix = "sha256:"
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	decoded, err := hex.DecodeString(value[len(prefix):])
	return err == nil && len(decoded) == 32 && strings.ToLower(value) == value
}

func validID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) <= len(prefix) || len(value) > 120 {
		return false
	}
	for _, character := range value[len(prefix):] {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' ||
			character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func validName(value string) bool {
	return validText(value, 160) && !strings.ContainsAny(value, "\r\n")
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func hasDuplicates(values []string) bool {
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value] || !validText(value, 240) {
			return true
		}
		seen[value] = true
	}
	return false
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
