// Package answerbenchmark owns the model-blind end-to-end answer benchmark
// host. Database setup and Query Agent access are restricted to versioned MSQL.
package answerbenchmark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
	protocolmsql "github.com/HW-Yue/Memora/protocol/msql"
)

const MaterializationReceiptVersion = "memora.answer-materialization-receipt/v1"

var (
	ErrInvalidMaterializer = errors.New("answer benchmark Materializer is invalid")
	ErrMaterialization     = errors.New("answer benchmark materialization failed")
)

type ObjectKind string

const (
	ObjectDatabase       ObjectKind = "database"
	ObjectTable          ObjectKind = "table"
	ObjectColumn         ObjectKind = "column"
	ObjectTableRootRoute ObjectKind = "table_root_route"
	ObjectRoute          ObjectKind = "route"
	ObjectRow            ObjectKind = "row"
)

type ObjectMapping struct {
	Kind      ObjectKind `json:"kind"`
	FixtureID string     `json:"fixture_id"`
	ActualID  string     `json:"actual_id"`
	Revision  uint64     `json:"revision"`
}

type MaterializationReceipt struct {
	Version               string          `json:"version"`
	CorpusID              string          `json:"corpus_id"`
	CorpusRevision        uint64          `json:"corpus_revision"`
	ManifestSHA256        string          `json:"manifest_sha256"`
	FixtureSnapshotID     string          `json:"fixture_snapshot_id"`
	FixtureSnapshotSHA256 string          `json:"fixture_snapshot_sha256"`
	MSQLRequests          uint64          `json:"msql_requests"`
	MSQLStatements        uint64          `json:"msql_statements"`
	Objects               []ObjectMapping `json:"objects"`
	Hash                  string          `json:"hash"`
}

func (receipt MaterializationReceipt) Mapping(kind ObjectKind, fixtureID string) (ObjectMapping, bool) {
	for _, mapping := range receipt.Objects {
		if mapping.Kind == kind && mapping.FixtureID == fixtureID {
			return mapping, true
		}
	}
	return ObjectMapping{}, false
}

func (receipt MaterializationReceipt) Validate() error {
	if receipt.Version != MaterializationReceiptVersion || strings.TrimSpace(receipt.CorpusID) == "" ||
		receipt.CorpusRevision == 0 || !validDigest(receipt.ManifestSHA256) ||
		strings.TrimSpace(receipt.FixtureSnapshotID) == "" || !validDigest(receipt.FixtureSnapshotSHA256) ||
		receipt.MSQLRequests == 0 || receipt.MSQLStatements < receipt.MSQLRequests || len(receipt.Objects) == 0 ||
		!validDigest(receipt.Hash) {
		return ErrMaterialization
	}
	seen := make(map[string]bool, len(receipt.Objects))
	previous := ""
	for _, mapping := range receipt.Objects {
		key := string(mapping.Kind) + "\x00" + mapping.FixtureID
		if key <= previous || seen[key] || strings.TrimSpace(mapping.FixtureID) == "" ||
			strings.TrimSpace(mapping.ActualID) == "" || mapping.Revision == 0 || !validObjectKind(mapping.Kind) {
			return ErrMaterialization
		}
		seen[key], previous = true, key
	}
	want, err := receiptDigest(receipt)
	if err != nil || receipt.Hash != want {
		return ErrMaterialization
	}
	return nil
}

type Materializer struct{ msql agent.MSQLExecutor }

func NewMaterializer(msql agent.MSQLExecutor) (*Materializer, error) {
	if msql == nil {
		return nil, ErrInvalidMaterializer
	}
	return &Materializer{msql: msql}, nil
}

func (materializer *Materializer) Materialize(
	ctx context.Context,
	manifest answercorpus.Manifest,
) (MaterializationReceipt, error) {
	if materializer == nil || materializer.msql == nil {
		return MaterializationReceipt{}, ErrInvalidMaterializer
	}
	if err := manifest.Validate(); err != nil {
		return MaterializationReceipt{}, fmt.Errorf("%w: public manifest is invalid", ErrMaterialization)
	}
	state := materializationState{
		materializer: materializer,
		manifest:     manifest,
		databaseNames: func() []string {
			values := make([]string, 0, len(manifest.Fixture.Databases))
			for _, database := range manifest.Fixture.Databases {
				values = append(values, database.Name)
			}
			return values
		}(),
		objects: make([]ObjectMapping, 0, 46),
		actual:  make(map[string]ObjectMapping, 46),
	}
	if err := state.write(ctx); err != nil {
		return MaterializationReceipt{}, err
	}
	if err := state.verify(ctx); err != nil {
		return MaterializationReceipt{}, err
	}
	sort.Slice(state.objects, func(left, right int) bool {
		leftKey := string(state.objects[left].Kind) + "\x00" + state.objects[left].FixtureID
		rightKey := string(state.objects[right].Kind) + "\x00" + state.objects[right].FixtureID
		return leftKey < rightKey
	})
	receipt := MaterializationReceipt{
		Version: MaterializationReceiptVersion, CorpusID: manifest.CorpusID, CorpusRevision: manifest.Revision,
		ManifestSHA256: manifest.Hash, FixtureSnapshotID: manifest.Fixture.SnapshotID,
		FixtureSnapshotSHA256: manifest.Fixture.SnapshotSHA256,
		MSQLRequests:          state.requests, MSQLStatements: state.statements, Objects: state.objects,
	}
	hash, err := receiptDigest(receipt)
	if err != nil {
		return MaterializationReceipt{}, fmt.Errorf("%w: receipt could not be sealed", ErrMaterialization)
	}
	receipt.Hash = hash
	if err := receipt.Validate(); err != nil {
		return MaterializationReceipt{}, err
	}
	return receipt, nil
}

type materializationState struct {
	materializer  *Materializer
	manifest      answercorpus.Manifest
	databaseNames []string
	objects       []ObjectMapping
	actual        map[string]ObjectMapping
	requests      uint64
	statements    uint64
}

func (state *materializationState) write(ctx context.Context) error {
	for _, database := range state.manifest.Fixture.Databases {
		created, err := state.execute(ctx,
			"CREATE DATABASE "+quoteIdentifier(database.Name)+" PURPOSE "+quoteLiteral(database.Purpose)+" SCOPE "+quoteLiteral(database.Scope),
			nil, protocolmsql.MutationOptions{}, protocolmsql.LevelStructural,
		)
		if err != nil {
			return err
		}
		row, err := oneRow(created)
		if err != nil {
			return err
		}
		if err := state.mapObject(ObjectDatabase, database.ID, row, "database_id", "schema_version"); err != nil {
			return err
		}
		for _, table := range database.Tables {
			if err := state.writeTable(ctx, database, table); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *materializationState) writeTable(
	ctx context.Context,
	database answercorpus.FixtureDatabase,
	table answercorpus.FixtureTable,
) error {
	columnDDL := make([]string, 0, len(table.Columns))
	for _, column := range table.Columns {
		if column.Type != "TEXT(4000)" {
			return fmt.Errorf("%w: fixture Column type is not allowed", ErrMaterialization)
		}
		columnDDL = append(columnDDL, quoteIdentifier(column.Name)+" "+column.Type+
			" PURPOSE "+quoteLiteral(column.Purpose)+" ROLE "+quoteIdentifier(column.SemanticRole))
	}
	tableName := qualifiedName(database.Name, table.Name)
	created, err := state.execute(ctx,
		"CREATE TABLE "+tableName+" PURPOSE "+quoteLiteral(table.Purpose)+
			" ROW SEMANTICS "+quoteLiteral(table.RowSemantics)+" ("+strings.Join(columnDDL, ", ")+")",
		nil, protocolmsql.MutationOptions{}, protocolmsql.LevelStructural,
	)
	if err != nil {
		return err
	}
	row, err := oneRow(created)
	if err != nil {
		return err
	}
	if err := state.mapObject(ObjectTable, table.ID, row, "table_id", "schema_version"); err != nil {
		return err
	}
	columns, err := state.execute(ctx, "SHOW COLUMNS FROM "+tableName+" LIMIT 256", nil,
		protocolmsql.MutationOptions{}, protocolmsql.LevelRead)
	if err != nil {
		return err
	}
	if len(columns.Rows) != len(table.Columns) || columns.Truncated {
		return fmt.Errorf("%w: fixture Columns did not round-trip", ErrMaterialization)
	}
	columnsByName := make(map[string]protocolmsql.Row, len(columns.Rows))
	for _, value := range columns.Rows {
		name, ok := textValue(value["name"])
		if !ok {
			return fmt.Errorf("%w: Column name is missing", ErrMaterialization)
		}
		columnsByName[name] = value
	}
	for _, column := range table.Columns {
		value, ok := columnsByName[column.Name]
		if !ok {
			return fmt.Errorf("%w: fixture Column is missing", ErrMaterialization)
		}
		if err := state.mapObject(ObjectColumn, table.ID+"/"+column.ID, value, "column_id", "schema_version"); err != nil {
			return err
		}
	}
	root, err := state.execute(ctx, "CREATE ROUTE ROOT FOR TABLE "+tableName+" PURPOSE "+quoteLiteral(table.Purpose+" navigation"),
		nil, state.writeOptions("table-root:"+table.ID, "Create benchmark Table Router root"), protocolmsql.LevelStructural)
	if err != nil {
		return err
	}
	rootRow, err := oneRow(root)
	if err != nil {
		return err
	}
	if err := state.mapObject(ObjectTableRootRoute, table.ID, rootRow, "route_id", "revision"); err != nil {
		return err
	}
	rootMapping := state.actual[objectKey(ObjectTableRootRoute, table.ID)]
	remaining := append([]answercorpus.FixtureRoute(nil), table.Routes...)
	for len(remaining) > 0 {
		progress := false
		next := remaining[:0]
		for _, route := range remaining {
			parentID := rootMapping.ActualID
			if route.ParentID != "" {
				parent, exists := state.actual[objectKey(ObjectRoute, route.ParentID)]
				if !exists {
					next = append(next, route)
					continue
				}
				parentID = parent.ActualID
			}
			if err := state.writeRoute(ctx, route, parentID); err != nil {
				return err
			}
			progress = true
		}
		if !progress {
			return fmt.Errorf("%w: fixture Route graph cannot be lowered", ErrMaterialization)
		}
		remaining = append([]answercorpus.FixtureRoute(nil), next...)
	}
	columnsByID := make(map[string]string, len(table.Columns))
	for _, column := range table.Columns {
		columnsByID[column.ID] = column.Name
	}
	for _, fixtureRow := range table.Rows {
		if err := state.writeRow(ctx, database, table, fixtureRow, columnsByID); err != nil {
			return err
		}
	}
	return nil
}

func (state *materializationState) writeRoute(ctx context.Context, route answercorpus.FixtureRoute, parentID string) error {
	created, err := state.execute(ctx,
		"CREATE ROUTE UNDER :parent NAME :name KIND :kind PURPOSE :purpose",
		map[string]any{"parent": parentID, "name": routePathName(route.ID), "kind": route.Kind, "purpose": route.Purpose},
		state.writeOptions("route:"+route.ID, "Create benchmark semantic Route"), protocolmsql.LevelStructural,
	)
	if err != nil {
		return err
	}
	row, err := oneRow(created)
	if err != nil {
		return err
	}
	actualID, ok := textValue(row["route_id"])
	if !ok {
		return fmt.Errorf("%w: created Route ID is missing", ErrMaterialization)
	}
	aliases := append([]string{route.Name}, route.Aliases...)
	updated, err := state.execute(ctx, "ALTER ROUTE :route SET ALIASES :aliases",
		map[string]any{"route": actualID, "aliases": aliases},
		func() protocolmsql.MutationOptions {
			value := state.writeOptions("route-aliases:"+route.ID, "Set benchmark semantic Route aliases")
			value.ExpectedRevision = 1
			return value
		}(), protocolmsql.LevelStructural,
	)
	if err != nil {
		return err
	}
	updatedRow, err := oneRow(updated)
	if err != nil {
		return err
	}
	return state.mapObject(ObjectRoute, route.ID, updatedRow, "route_id", "revision")
}

func (state *materializationState) writeRow(
	ctx context.Context,
	database answercorpus.FixtureDatabase,
	table answercorpus.FixtureTable,
	fixtureRow answercorpus.FixtureRow,
	columnsByID map[string]string,
) error {
	columns := make([]string, 0, len(table.Columns))
	values := make([]string, 0, len(table.Columns))
	parameters := make(map[string]any, len(table.Columns))
	for index, column := range table.Columns {
		name, exists := columnsByID[column.ID]
		value, present := fixtureRow.Values[column.ID]
		if !exists || !present {
			return fmt.Errorf("%w: fixture Row shape is invalid", ErrMaterialization)
		}
		parameter := fmt.Sprintf("value_%d", index)
		columns = append(columns, quoteIdentifier(name))
		values = append(values, ":"+parameter)
		parameters[parameter] = value
	}
	leafID := ""
	for _, route := range table.Routes {
		if route.RowID == fixtureRow.ID {
			mapping, ok := state.actual[objectKey(ObjectRoute, route.ID)]
			if !ok || leafID != "" {
				return fmt.Errorf("%w: fixture Row Leaf membership is invalid", ErrMaterialization)
			}
			leafID = mapping.ActualID
		}
	}
	if leafID == "" {
		return fmt.Errorf("%w: fixture Row has no Leaf", ErrMaterialization)
	}
	options := state.writeOptions("row:"+fixtureRow.ID, "Insert benchmark semantic Row")
	options.ExpectedSchemaVersion = 1
	options.RouteLeafIDs = []string{leafID}
	inserted, err := state.execute(ctx,
		"INSERT INTO "+qualifiedName(database.Name, table.Name)+" ("+strings.Join(columns, ", ")+") VALUES ("+strings.Join(values, ", ")+")",
		parameters, options, protocolmsql.LevelWrite,
	)
	if err != nil {
		return err
	}
	row, err := oneRow(inserted)
	if err != nil {
		return err
	}
	if inserted.Revision == nil {
		return fmt.Errorf("%w: inserted Row revision is missing", ErrMaterialization)
	}
	row["revision"] = *inserted.Revision
	return state.mapObject(ObjectRow, fixtureRow.ID, row, "row_id", "revision")
}

func (state *materializationState) verify(ctx context.Context) error {
	for _, database := range state.manifest.Fixture.Databases {
		described, err := state.execute(ctx, "DESCRIBE DATABASE "+quoteIdentifier(database.Name)+" COMPACT", nil,
			protocolmsql.MutationOptions{}, protocolmsql.LevelRead)
		if err != nil {
			return err
		}
		row, err := oneRow(described)
		if err != nil || !mappingIdentityMatches(state.actual[objectKey(ObjectDatabase, database.ID)], row, "database_id") {
			return fmt.Errorf("%w: Database verification failed", ErrMaterialization)
		}
		if err := state.refreshMappingRevision(ObjectDatabase, database.ID, row["schema_version"]); err != nil {
			return err
		}
		for _, table := range database.Tables {
			if err := state.verifyTable(ctx, database, table); err != nil {
				return err
			}
		}
	}
	return nil
}

func (state *materializationState) verifyTable(
	ctx context.Context,
	database answercorpus.FixtureDatabase,
	table answercorpus.FixtureTable,
) error {
	tableName := qualifiedName(database.Name, table.Name)
	described, err := state.execute(ctx, "DESCRIBE TABLE "+tableName+" COMPACT", nil,
		protocolmsql.MutationOptions{}, protocolmsql.LevelRead)
	if err != nil {
		return err
	}
	row, err := oneRow(described)
	if err != nil || !mappingMatches(state.actual[objectKey(ObjectTable, table.ID)], row, "table_id", "schema_version") {
		return fmt.Errorf("%w: Table verification failed", ErrMaterialization)
	}
	for _, route := range append([]answercorpus.FixtureRoute(nil), table.Routes...) {
		mapping := state.actual[objectKey(ObjectRoute, route.ID)]
		value, err := state.execute(ctx, "DESCRIBE ROUTE :route", map[string]any{"route": mapping.ActualID},
			protocolmsql.MutationOptions{}, protocolmsql.LevelRead)
		if err != nil {
			return err
		}
		describedRoute, err := oneRow(value)
		wantAliases := append([]string{route.Name}, route.Aliases...)
		// The revision may have moved on since the Route was created: a leaf
		// records the Row hanging under it, so materializing this fixture's
		// Rows advances the leaves they mount on. What must not move is the
		// Route's identity or its semantic surface, which is what this check is
		// for. See docs/storage/leaf-rowid-v1.md §5.3.
		if err != nil || !mappingIdentityMatches(mapping, describedRoute, "route_id") ||
			!equalTextList(describedRoute["aliases"], wantAliases) {
			return fmt.Errorf("%w: Route verification failed", ErrMaterialization)
		}
		if revision, ok := uintValue(describedRoute["revision"]); !ok || revision < mapping.Revision {
			return fmt.Errorf("%w: Route revision went backwards", ErrMaterialization)
		}
	}
	for _, fixtureRow := range table.Rows {
		mapping := state.actual[objectKey(ObjectRow, fixtureRow.ID)]
		value, err := state.execute(ctx, "SELECT * FROM "+tableName+" WHERE row_id = :row LIMIT 1",
			map[string]any{"row": mapping.ActualID}, protocolmsql.MutationOptions{}, protocolmsql.LevelRead)
		if err != nil {
			return err
		}
		selected, err := oneRow(value)
		if err != nil || !mappingMatches(mapping, selected, "row_id", "revision") {
			return fmt.Errorf("%w: Row verification failed", ErrMaterialization)
		}
		for _, column := range table.Columns {
			if selected[column.Name] != fixtureRow.Values[column.ID] {
				return fmt.Errorf("%w: Row value verification failed", ErrMaterialization)
			}
		}
	}
	return nil
}

func (state *materializationState) execute(
	ctx context.Context,
	source string,
	parameters map[string]any,
	mutation protocolmsql.MutationOptions,
	level protocolmsql.RiskLevel,
) (protocolmsql.StatementResult, error) {
	request := protocolmsql.Request{
		Version: protocolmsql.RequestVersion, Source: source,
		Statements: []protocolmsql.StatementInput{{
			Parameters: protocolmsql.Parameters{Named: parameters}, Mutation: mutation,
			Authorization: protocolmsql.Authorization{
				Version: protocolmsql.AuthorizationVersion, Actor: "benchmark:materializer",
				AuthorizedDatabases: append([]string(nil), state.databaseNames...), DefaultLevel: level,
			},
		}},
	}
	if err := request.Validate(); err != nil {
		return protocolmsql.StatementResult{}, fmt.Errorf("%w: generated MSQL request is invalid", ErrMaterialization)
	}
	state.requests++
	state.statements += uint64(len(request.Statements))
	envelope, err := state.materializer.msql.ExecuteMSQL(ctx, request)
	if err != nil {
		return protocolmsql.StatementResult{}, fmt.Errorf("%w: MSQL transport: %v", ErrMaterialization, err)
	}
	if err := envelope.Validate(); err != nil {
		return protocolmsql.StatementResult{}, fmt.Errorf("%w: MSQL envelope is invalid", ErrMaterialization)
	}
	if !envelope.OK || envelope.Error != nil || len(envelope.Results) != 1 || envelope.Truncated {
		var statementError *protocolmsql.ResultError
		if len(envelope.Results) == 1 {
			statementError = envelope.Results[0].Error
		}
		return protocolmsql.StatementResult{}, fmt.Errorf("%w: MSQL request failed for %q: envelope=%+v statement=%+v", ErrMaterialization, source, envelope.Error, statementError)
	}
	result := envelope.Results[0]
	if result.Status != protocolmsql.Status("succeeded") || result.Error != nil || result.Truncated {
		return protocolmsql.StatementResult{}, fmt.Errorf("%w: MSQL statement failed for %q: %#v", ErrMaterialization, source, result.Error)
	}
	return result, nil
}

func (state *materializationState) writeOptions(locator, reason string) protocolmsql.MutationOptions {
	return protocolmsql.MutationOptions{
		MaxAffectedRows: 1, Actor: "benchmark:materializer", Source: "F182 public answer fixture",
		Reason: reason, SourceKind: protocolmsql.SourceRepositoryAnchor,
		SourceLocator:     "answer-corpus:" + state.manifest.Fixture.SnapshotID + "#" + locator,
		SourceContentHash: state.manifest.Fixture.SnapshotSHA256,
	}
}

func (state *materializationState) mapObject(kind ObjectKind, fixtureID string, row protocolmsql.Row, idKey, revisionKey string) error {
	actualID, ok := textValue(row[idKey])
	if !ok {
		return fmt.Errorf("%w: mapped object ID is missing", ErrMaterialization)
	}
	revision, ok := uintValue(row[revisionKey])
	if !ok || revision == 0 {
		return fmt.Errorf("%w: mapped object revision is missing", ErrMaterialization)
	}
	key := objectKey(kind, fixtureID)
	if _, exists := state.actual[key]; exists {
		return fmt.Errorf("%w: fixture object %q was mapped twice", ErrMaterialization, key)
	}
	mapping := ObjectMapping{Kind: kind, FixtureID: fixtureID, ActualID: actualID, Revision: revision}
	state.actual[key] = mapping
	state.objects = append(state.objects, mapping)
	return nil
}

func oneRow(result protocolmsql.StatementResult) (protocolmsql.Row, error) {
	if len(result.Rows) != 1 {
		return nil, fmt.Errorf("%w: MSQL result did not contain one Row", ErrMaterialization)
	}
	return result.Rows[0], nil
}

func mappingMatches(mapping ObjectMapping, row protocolmsql.Row, idKey, revisionKey string) bool {
	id, idOK := textValue(row[idKey])
	revision, revisionOK := uintValue(row[revisionKey])
	return idOK && revisionOK && id == mapping.ActualID && revision == mapping.Revision
}

func mappingIdentityMatches(mapping ObjectMapping, row protocolmsql.Row, idKey string) bool {
	id, ok := textValue(row[idKey])
	return ok && id == mapping.ActualID
}

func (state *materializationState) refreshMappingRevision(kind ObjectKind, fixtureID string, value any) error {
	revision, ok := uintValue(value)
	key := objectKey(kind, fixtureID)
	mapping, exists := state.actual[key]
	if !ok || revision == 0 || !exists || revision < mapping.Revision {
		return fmt.Errorf("%w: mapped object revision verification failed", ErrMaterialization)
	}
	mapping.Revision = revision
	state.actual[key] = mapping
	for index := range state.objects {
		if state.objects[index].Kind == kind && state.objects[index].FixtureID == fixtureID {
			state.objects[index].Revision = revision
			return nil
		}
	}
	return fmt.Errorf("%w: mapped object is missing", ErrMaterialization)
}

func textValue(value any) (string, bool) {
	text, ok := value.(string)
	return text, ok && strings.TrimSpace(text) != ""
}

func uintValue(value any) (uint64, bool) {
	switch number := value.(type) {
	case json.Number:
		parsed, err := number.Int64()
		return uint64(parsed), err == nil && parsed >= 0
	case uint64:
		return number, true
	case int64:
		return uint64(number), number >= 0
	case int:
		return uint64(number), number >= 0
	case float64:
		converted := uint64(number)
		return converted, number >= 0 && float64(converted) == number
	default:
		return 0, false
	}
}

func equalTextList(value any, want []string) bool {
	var got []string
	switch values := value.(type) {
	case []string:
		got = values
	case []any:
		got = make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
			if !ok {
				return false
			}
			got[index] = text
		}
	default:
		return false
	}
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func quoteIdentifier(value string) string {
	return "`" + strings.ReplaceAll(value, "`", "``") + "`"
}

func quoteLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func qualifiedName(database, table string) string {
	return quoteIdentifier(database) + "." + quoteIdentifier(table)
}

func routePathName(fixtureID string) string {
	return strings.TrimPrefix(fixtureID, "route_")
}

func objectKey(kind ObjectKind, fixtureID string) string {
	return string(kind) + "\x00" + fixtureID
}

func validObjectKind(kind ObjectKind) bool {
	switch kind {
	case ObjectDatabase, ObjectTable, ObjectColumn, ObjectTableRootRoute, ObjectRoute, ObjectRow:
		return true
	default:
		return false
	}
}

func receiptDigest(receipt MaterializationReceipt) (string, error) {
	receipt.Hash = ""
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}
