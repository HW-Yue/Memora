package pagestoremigration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/nativecatalog"
	"github.com/HW-Yue/Memora/internal/nativerouter"
	"github.com/HW-Yue/Memora/internal/nativerow"
	"github.com/HW-Yue/Memora/internal/router"
	"github.com/HW-Yue/Memora/internal/row"
	"github.com/HW-Yue/Memora/internal/rowfulltext"
	"github.com/HW-Yue/Memora/internal/store/catalogindex"
	"github.com/HW-Yue/Memora/internal/store/currentrowindex"
	nativestore "github.com/HW-Yue/Memora/internal/store/native"
	"github.com/HW-Yue/Memora/internal/store/rowversionindex"
)

const (
	PlanVersion       = "memora.page-index-migration-plan/v2"
	legacyPlanVersion = "memora.page-index-migration-plan/v1"
)

var (
	ErrInvalid       = errors.New("Page index migration plan is invalid")
	ErrCorrupt       = errors.New("legacy Page index source is corrupt")
	ErrSourceChanged = errors.New("legacy Page index source changed")
	ErrUnsupported   = errors.New("legacy Page index source contains unsupported records")
)

type RecordCount struct {
	Kind  nativestore.ObjectKind `json:"kind"`
	Count uint64                 `json:"count"`
}

type Plan struct {
	Version           string                    `json:"version"`
	SourceFingerprint string                    `json:"source_sha256"`
	RecordCounts      []RecordCount             `json:"record_counts"`
	Catalog           []catalog.Database        `json:"catalog"`
	CurrentRows       []currentrowindex.Locator `json:"current_rows"`
	CurrentRowBodies  []row.Row                 `json:"current_row_bodies"`
	RowVersions       []rowversionindex.Locator `json:"row_versions"`
	CurrentRoutes     []router.Node             `json:"current_routes"`
	Digest            string                    `json:"digest"`
}

type SourceState struct {
	Fingerprint [32]byte
	Counts      []RecordCount
}

type Source interface {
	Inventory(context.Context) (SourceState, error)
	Catalog(context.Context) ([]catalog.Database, error)
	RowVersions(context.Context) ([]row.Row, error)
	Routes(context.Context) ([]router.Node, error)
}

type Reader struct{ source Source }

func NewReader(source Source) (*Reader, error) {
	if source == nil {
		return nil, ErrInvalid
	}
	return &Reader{source: source}, nil
}

func NewNativeReader(file *nativestore.File) (*Reader, error) {
	if file == nil {
		return nil, ErrInvalid
	}
	kind, err := file.Kind()
	if err != nil || kind != nativestore.FileKindDatabase {
		return nil, fmt.Errorf("%w: legacy source must be a Database file", ErrInvalid)
	}
	return NewReader(&nativeSource{file: file})
}

func (reader *Reader) Build(ctx context.Context) (Plan, error) {
	if reader == nil || reader.source == nil || ctx == nil {
		return Plan{}, fmt.Errorf("%w: Reader or context", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	before, err := reader.source.Inventory(ctx)
	if err != nil {
		return Plan{}, err
	}
	databases, err := reader.source.Catalog(ctx)
	if err != nil {
		return Plan{}, err
	}
	rows, err := reader.source.RowVersions(ctx)
	if err != nil {
		return Plan{}, err
	}
	after, err := reader.source.Inventory(ctx)
	if err != nil {
		return Plan{}, err
	}
	if before.Fingerprint != after.Fingerprint || !equalCounts(before.Counts, after.Counts) {
		return Plan{}, ErrSourceChanged
	}
	plan := Plan{
		Version: PlanVersion, SourceFingerprint: hex.EncodeToString(before.Fingerprint[:]),
		RecordCounts: append([]RecordCount(nil), before.Counts...), Catalog: databases,
	}
	if err := planRows(&plan, rows); err != nil {
		return Plan{}, err
	}
	if err := validatePlan(plan, false); err != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	digest, err := planDigest(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.Digest = digest
	return plan, nil
}

// VerifySource confirms that a valid plan is still bound to the exact committed
// legacy record inventory from which it was built. Migration writers must call
// it immediately before applying the plan.
func (reader *Reader) VerifySource(ctx context.Context, plan Plan) error {
	if reader == nil || reader.source == nil || ctx == nil {
		return fmt.Errorf("%w: Reader or context", ErrInvalid)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	state, err := reader.source.Inventory(ctx)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if plan.SourceFingerprint != hex.EncodeToString(state.Fingerprint[:]) ||
		!equalCounts(plan.RecordCounts, state.Counts) {
		return ErrSourceChanged
	}
	return nil
}

type nativeSource struct{ file *nativestore.File }

func (source *nativeSource) Inventory(ctx context.Context) (SourceState, error) {
	if source == nil || source.file == nil || ctx == nil {
		return SourceState{}, fmt.Errorf("%w: native source", ErrInvalid)
	}
	references, err := source.file.Records()
	if err != nil {
		return SourceState{}, classifySourceError(err)
	}
	counts := make([]RecordCount, 0, int(nativestore.ObjectKindCommittedChange-nativestore.ObjectKindDatabase)+1)
	byKind := make(map[nativestore.ObjectKind]uint64)
	hash := sha256.New()
	_, _ = hash.Write([]byte("memora-legacy-record-inventory-v1\x00"))
	var encoded [8]byte
	for _, reference := range references {
		if err := ctx.Err(); err != nil {
			return SourceState{}, err
		}
		if reference.Kind < nativestore.ObjectKindDatabase || reference.Kind > nativestore.ObjectKindCommittedChange {
			return SourceState{}, fmt.Errorf("%w: object kind %d", ErrUnsupported, reference.Kind)
		}
		payload, err := source.file.Get(reference.Kind, reference.ID)
		if err != nil {
			return SourceState{}, classifySourceError(err)
		}
		if uint32(len(payload)) != reference.PayloadLength {
			return SourceState{}, fmt.Errorf("%w: Record length changed", ErrCorrupt)
		}
		binary.BigEndian.PutUint16(encoded[:2], uint16(reference.Kind))
		binary.BigEndian.PutUint32(encoded[2:6], reference.SchemaVersion)
		_, _ = hash.Write(encoded[:6])
		binary.BigEndian.PutUint32(encoded[:4], uint32(len(reference.ID)))
		_, _ = hash.Write(encoded[:4])
		_, _ = hash.Write([]byte(reference.ID))
		binary.BigEndian.PutUint32(encoded[:4], reference.PayloadLength)
		_, _ = hash.Write(encoded[:4])
		_, _ = hash.Write(payload)
		byKind[reference.Kind]++
	}
	for kind := nativestore.ObjectKindDatabase; kind <= nativestore.ObjectKindCommittedChange; kind++ {
		counts = append(counts, RecordCount{Kind: kind, Count: byKind[kind]})
	}
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], hash.Sum(nil))
	return SourceState{Fingerprint: fingerprint, Counts: counts}, nil
}

func (source *nativeSource) Catalog(ctx context.Context) ([]catalog.Database, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := nativecatalog.New(source.file).Read()
	if err != nil {
		return nil, classifySourceError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}

func (source *nativeSource) RowVersions(ctx context.Context) ([]row.Row, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, err := nativerow.New(source.file).AllRowVersions()
	if err != nil {
		return nil, classifySourceError(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return value, nil
}

func (source *nativeSource) Routes(ctx context.Context) ([]router.Node, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	values, err := nativerouter.New(source.file).Nodes()
	if err != nil {
		return nil, classifySourceError(err)
	}
	return values, ctx.Err()
}

func planRows(plan *Plan, rows []row.Row) error {
	sort.Slice(rows, func(left, right int) bool {
		if rows[left].ID != rows[right].ID {
			return rows[left].ID < rows[right].ID
		}
		return rows[left].Revision < rows[right].Revision
	})
	plan.RowVersions = make([]rowversionindex.Locator, 0, len(rows))
	plan.CurrentRows = make([]currentrowindex.Locator, 0)
	plan.CurrentRowBodies = make([]row.Row, 0)
	for index, value := range rows {
		locator := rowversionindex.Locator{
			DatabaseID: value.DatabaseID, TableID: value.TableID, RowID: value.ID,
			SchemaRevision: value.SchemaVersion, Revision: value.Revision,
			CommitSequence: value.CommitSequence, State: value.State,
		}
		plan.RowVersions = append(plan.RowVersions, locator)
		if index+1 == len(rows) || rows[index+1].ID != value.ID {
			plan.CurrentRows = append(plan.CurrentRows, currentrowindex.Locator{
				DatabaseID: locator.DatabaseID, TableID: locator.TableID, RowID: locator.RowID,
				SchemaRevision: locator.SchemaRevision, Revision: locator.Revision,
				CommitSequence: locator.CommitSequence, State: locator.State,
			})
			plan.CurrentRowBodies = append(plan.CurrentRowBodies, cloneRow(value))
		}
	}
	return nil
}

func (plan Plan) Validate() error { return validatePlan(plan, true) }

func validatePlan(plan Plan, checkDigest bool) error {
	if plan.Version != PlanVersion || !validSHA256(plan.SourceFingerprint) ||
		!validCounts(plan.RecordCounts) || catalogindex.Validate(plan.Catalog) != nil {
		return invalidPlan("header, source, counts, or Catalog")
	}
	tables := make(map[string]catalog.Table)
	databaseCount, tableCount, columnCount := uint64(len(plan.Catalog)), uint64(0), uint64(0)
	for _, database := range plan.Catalog {
		tableCount += uint64(len(database.Tables))
		for _, table := range database.Tables {
			columnCount += uint64(len(table.Columns))
			tables[table.ID] = table
		}
	}
	if recordCount(plan.RecordCounts, nativestore.ObjectKindDatabase) < databaseCount ||
		recordCount(plan.RecordCounts, nativestore.ObjectKindTable) < tableCount ||
		recordCount(plan.RecordCounts, nativestore.ObjectKindColumn) < columnCount {
		return invalidPlan("Catalog record counts")
	}
	current := make(map[string]currentrowindex.Locator, len(plan.CurrentRows))
	for index, locator := range plan.CurrentRows {
		if index > 0 && plan.CurrentRows[index-1].RowID >= locator.RowID {
			return invalidPlan("current Row order")
		}
		if _, duplicate := current[locator.RowID]; duplicate {
			return invalidPlan("duplicate current Row")
		}
		current[locator.RowID] = locator
	}
	previous := make(map[string]rowversionindex.Locator)
	positive := make(map[string]bool)
	for index, locator := range plan.RowVersions {
		table, exists := tables[locator.TableID]
		if !exists || table.DatabaseID != locator.DatabaseID ||
			table.SchemaVersion != locator.SchemaRevision || locator.RowID == "" || locator.Revision == 0 ||
			(locator.State != row.StateLive && locator.State != row.StateDeleted && locator.State != row.StateSuperseded) {
			return invalidPlan("Row version scope, schema, revision, or state")
		}
		if index > 0 {
			prior := plan.RowVersions[index-1]
			if prior.RowID > locator.RowID || (prior.RowID == locator.RowID && prior.Revision >= locator.Revision) {
				return invalidPlan("Row version order")
			}
		}
		prior, found := previous[locator.RowID]
		if (!found && locator.Revision != 1) ||
			(found && (locator.DatabaseID != prior.DatabaseID || locator.TableID != prior.TableID ||
				locator.Revision != prior.Revision+1)) {
			return invalidPlan("Row revision sequence")
		}
		if locator.CommitSequence == 0 {
			if positive[locator.RowID] {
				return invalidPlan("zero commit after positive commit")
			}
		} else {
			if found && prior.CommitSequence != 0 && locator.CommitSequence < prior.CommitSequence {
				return invalidPlan("Row commit sequence regressed")
			}
			positive[locator.RowID] = true
		}
		previous[locator.RowID] = locator
	}
	if len(current) != len(previous) || recordCount(plan.RecordCounts, nativestore.ObjectKindRow) != uint64(len(plan.RowVersions)) {
		return invalidPlan("current/version cardinality")
	}
	for rowID, latest := range previous {
		currentLocator, exists := current[rowID]
		if !exists || !sameLocator(currentLocator, latest) {
			return invalidPlan("current/version locator mismatch")
		}
	}
	if len(plan.CurrentRowBodies) != len(plan.CurrentRows) {
		return invalidPlan("current Row body cardinality")
	}
	for index, body := range plan.CurrentRowBodies {
		if index > 0 && plan.CurrentRowBodies[index-1].ID >= body.ID {
			return invalidPlan("current Row body order")
		}
		locator, exists := current[body.ID]
		table, tableExists := tables[body.TableID]
		if !exists || !tableExists || locator.DatabaseID != body.DatabaseID || locator.TableID != body.TableID ||
			locator.SchemaRevision != body.SchemaVersion || locator.Revision != body.Revision ||
			locator.CommitSequence != body.CommitSequence || locator.State != body.State {
			return invalidPlan("current Row body/locator mismatch")
		}
		if _, err := rowfulltext.Project(table, body); err != nil {
			return invalidPlan("current Row body projection: %v", err)
		}
	}
	if checkDigest {
		if !validSHA256(plan.Digest) {
			return invalidPlan("Plan digest encoding")
		}
		want, err := planDigest(plan)
		if err != nil || want != plan.Digest {
			return ErrInvalid
		}
	}
	return nil
}

func invalidPlan(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, arguments...))
}

func sameLocator(current currentrowindex.Locator, version rowversionindex.Locator) bool {
	return current.DatabaseID == version.DatabaseID && current.TableID == version.TableID &&
		current.RowID == version.RowID && current.SchemaRevision == version.SchemaRevision &&
		current.Revision == version.Revision && current.CommitSequence == version.CommitSequence &&
		current.State == version.State
}

func planDigest(plan Plan) (string, error) {
	payload := struct {
		Version           string                    `json:"version"`
		SourceFingerprint string                    `json:"source_sha256"`
		RecordCounts      []RecordCount             `json:"record_counts"`
		Catalog           []catalog.Database        `json:"catalog"`
		CurrentRows       []currentrowindex.Locator `json:"current_rows"`
		CurrentRowBodies  []row.Row                 `json:"current_row_bodies"`
		RowVersions       []rowversionindex.Locator `json:"row_versions"`
	}{
		plan.Version, plan.SourceFingerprint, plan.RecordCounts, plan.Catalog,
		plan.CurrentRows, plan.CurrentRowBodies, plan.RowVersions,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneRow(value row.Row) row.Row {
	values := value.Values
	value.Values = make(map[string]any, len(values))
	for key, item := range values {
		value.Values[key] = item
	}
	return value
}

func validSHA256(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func validCounts(counts []RecordCount) bool {
	legacy := int(nativestore.ObjectKindConfiguration-nativestore.ObjectKindDatabase) + 1
	current := int(nativestore.ObjectKindCommittedChange-nativestore.ObjectKindDatabase) + 1
	if len(counts) != legacy && len(counts) != current {
		return false
	}
	for index, count := range counts {
		if count.Kind != nativestore.ObjectKindDatabase+nativestore.ObjectKind(index) {
			return false
		}
	}
	return true
}

func recordCount(counts []RecordCount, kind nativestore.ObjectKind) uint64 {
	for _, count := range counts {
		if count.Kind == kind {
			return count.Count
		}
	}
	return 0
}

func equalCounts(left, right []RecordCount) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func classifySourceError(err error) error {
	if errors.Is(err, nativestore.ErrCorrupt) || errors.Is(err, nativecatalog.ErrCorrupt) ||
		errors.Is(err, nativerow.ErrCorrupt) {
		return fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	return err
}
