package dbpackage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/HW-Yue/Memora/internal/catalog"
	"github.com/HW-Yue/Memora/internal/result"
	"github.com/HW-Yue/Memora/internal/security"
	"github.com/HW-Yue/Memora/internal/snapshot"
	"github.com/HW-Yue/Memora/internal/store"
)

const (
	Version         = "memora.database-package/v1"
	maxPackageBytes = 64 << 20
)

type Manifest struct {
	Version                string `json:"version"`
	DatabaseID             string `json:"database_id"`
	Name                   string `json:"name"`
	Purpose                string `json:"purpose"`
	Scope                  string `json:"scope"`
	AntiScope              string `json:"anti_scope,omitempty"`
	SchemaVersion          uint64 `json:"schema_version"`
	SnapshotVersion        string `json:"snapshot_version"`
	SnapshotSHA256         string `json:"snapshot_sha256"`
	CreatedBy              string `json:"created_by"`
	Compatibility          string `json:"compatibility"`
	RowCount               int    `json:"row_count"`
	HistoryCount           int    `json:"history_count"`
	RelationCount          int    `json:"relation_count"`
	DerivedIndexesIncluded bool   `json:"derived_indexes_included"`
	ReadOnlyDefault        bool   `json:"read_only_default"`
}

type Opened struct {
	Manifest      Manifest
	Snapshot      []byte
	PackageSHA256 string
	ReadOnly      bool
}

type InstallOptions struct {
	Trusted bool
}

type InstallReceipt struct {
	DatabaseID     string `json:"database_id"`
	Name           string `json:"name"`
	PackageSHA256  string `json:"package_sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

type Error struct {
	Code    result.Code
	Message string
}

func (err *Error) Error() string      { return err.Message }
func (err *Error) StableCode() string { return string(err.Code) }

type Service struct {
	store     store.Store
	snapshots *snapshot.Service
}

type envelope struct {
	Version  string          `json:"version"`
	Manifest Manifest        `json:"manifest"`
	Snapshot json.RawMessage `json:"snapshot"`
}

type snapshotShape struct {
	Version   string            `json:"version"`
	Catalog   json.RawMessage   `json:"catalog"`
	Rows      []json.RawMessage `json:"rows"`
	History   []json.RawMessage `json:"history"`
	Relations struct {
		Current  []json.RawMessage `json:"current"`
		Versions []json.RawMessage `json:"versions"`
	} `json:"relations"`
}

func New(database store.Store) *Service {
	return &Service{store: database, snapshots: snapshot.New(database)}
}

func (service *Service) Pack(ctx context.Context, selector, createdBy string) ([]byte, Manifest, error) {
	if service.store == nil {
		return nil, Manifest{}, packageError(result.CodeInternal, "package source Store is unavailable")
	}
	if strings.TrimSpace(selector) == "" || strings.TrimSpace(createdBy) == "" {
		return nil, Manifest{}, packageError(result.CodeValidation, "package selector and author declaration are required")
	}
	exported, err := service.snapshots.Export(ctx)
	if err != nil {
		return nil, Manifest{}, err
	}
	filtered, database, err := snapshot.FilterDatabase(exported, selector)
	if err != nil {
		return nil, Manifest{}, err
	}
	shape, err := inspectSnapshot(filtered)
	if err != nil {
		return nil, Manifest{}, err
	}
	hash, err := snapshot.CanonicalHash(filtered)
	if err != nil {
		return nil, Manifest{}, err
	}
	manifest := Manifest{
		Version: Version, DatabaseID: database.ID, Name: database.Name,
		Purpose: database.Purpose, Scope: database.Scope, AntiScope: database.AntiScope,
		SchemaVersion: database.SchemaVersion, SnapshotVersion: snapshot.Version,
		SnapshotSHA256: hash, CreatedBy: strings.TrimSpace(createdBy),
		Compatibility: "memora-logical-snapshot-v1", RowCount: len(shape.Rows),
		HistoryCount:           len(shape.History),
		RelationCount:          len(shape.Relations.Versions),
		DerivedIndexesIncluded: false, ReadOnlyDefault: true,
	}
	encoded, err := json.Marshal(envelope{Version: Version, Manifest: manifest, Snapshot: filtered})
	if err != nil {
		return nil, Manifest{}, packageError(result.CodeInternal, "database package could not be encoded")
	}
	return encoded, manifest, nil
}

func (service *Service) Open(encoded []byte) (Opened, error) {
	value, err := decode(encoded)
	if err != nil {
		return Opened{}, err
	}
	shape, err := inspectSnapshot(value.Snapshot)
	if err != nil {
		return Opened{}, err
	}
	var catalogValue struct {
		Version   string             `json:"version"`
		Databases []catalog.Database `json:"databases"`
	}
	if json.Unmarshal(shape.Catalog, &catalogValue) != nil ||
		catalogValue.Version != catalog.Version || len(catalogValue.Databases) != 1 {
		return Opened{}, packageError(result.CodeValidation, "package must contain exactly one valid Database")
	}
	database := catalogValue.Databases[0]
	manifest := value.Manifest
	for _, metadata := range []struct {
		value    string
		maximum  int
		required bool
	}{
		{manifest.Name, 200, true},
		{manifest.Purpose, 1200, true},
		{manifest.Scope, 1200, true},
		{manifest.AntiScope, 1200, false},
		{manifest.CreatedBy, 200, true},
	} {
		if err := security.ValidateMetadataText(metadata.value, metadata.maximum, metadata.required); err != nil {
			return Opened{}, packageError(result.CodeValidation, "package manifest contains unsafe metadata text")
		}
	}
	if manifest.Version != Version || manifest.SnapshotVersion != snapshot.Version ||
		manifest.Compatibility != "memora-logical-snapshot-v1" ||
		manifest.DatabaseID != database.ID || manifest.Name != database.Name ||
		manifest.Purpose != database.Purpose || manifest.Scope != database.Scope ||
		manifest.AntiScope != database.AntiScope || manifest.SchemaVersion != database.SchemaVersion ||
		manifest.RowCount != len(shape.Rows) || manifest.HistoryCount != len(shape.History) ||
		manifest.RelationCount != len(shape.Relations.Versions) ||
		manifest.DerivedIndexesIncluded || !manifest.ReadOnlyDefault ||
		strings.TrimSpace(manifest.CreatedBy) == "" {
		return Opened{}, packageError(result.CodeValidation, "database package manifest does not match its authority")
	}
	hash, err := snapshot.CanonicalHash(value.Snapshot)
	if err != nil {
		return Opened{}, err
	}
	if !equalHash(hash, manifest.SnapshotSHA256) {
		return Opened{}, packageError(result.CodeValidation, "database package content hash does not match")
	}
	return Opened{
		Manifest: manifest, Snapshot: append([]byte(nil), value.Snapshot...),
		PackageSHA256: Hash(encoded), ReadOnly: true,
	}, nil
}

func (service *Service) Install(ctx context.Context, encoded []byte, options InstallOptions) (InstallReceipt, error) {
	if !options.Trusted {
		return InstallReceipt{}, packageError(result.CodePermissionDenied, "installing a database package requires explicit trust")
	}
	if err := security.RequireApproval(ctx, security.ActionInstallPackage, Hash(encoded)); err != nil {
		return InstallReceipt{}, err
	}
	if service.store == nil {
		return InstallReceipt{}, packageError(result.CodeInternal, "package target Store is unavailable")
	}
	opened, err := service.Open(encoded)
	if err != nil {
		return InstallReceipt{}, err
	}
	if err := service.snapshots.MergeImport(ctx, opened.Snapshot); err != nil {
		return InstallReceipt{}, err
	}
	return InstallReceipt{
		DatabaseID: opened.Manifest.DatabaseID, Name: opened.Manifest.Name,
		PackageSHA256: opened.PackageSHA256, SnapshotSHA256: opened.Manifest.SnapshotSHA256,
	}, nil
}

func Hash(encoded []byte) string {
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func decode(encoded []byte) (envelope, error) {
	if len(encoded) == 0 || len(encoded) > maxPackageBytes {
		return envelope{}, packageError(result.CodeValidation, "database package exceeds its size budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var value envelope
	if err := decoder.Decode(&value); err != nil {
		return envelope{}, packageError(result.CodeValidation, "database package JSON is invalid")
	}
	if err := requireEOF(decoder); err != nil || value.Version != Version || len(value.Snapshot) == 0 {
		return envelope{}, packageError(result.CodeValidation, "database package version or envelope is invalid")
	}
	return value, nil
}

func inspectSnapshot(encoded []byte) (snapshotShape, error) {
	migrated, err := snapshot.Migrate(encoded)
	if err != nil {
		return snapshotShape{}, err
	}
	var value snapshotShape
	if json.Unmarshal(migrated, &value) != nil || value.Version != snapshot.Version ||
		value.Rows == nil || value.History == nil || value.Relations.Current == nil || value.Relations.Versions == nil {
		return snapshotShape{}, packageError(result.CodeValidation, "database package snapshot shape is invalid")
	}
	return value, nil
}

func requireEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON value")
	}
	return nil
}

func equalHash(left, right string) bool {
	if len(left) != sha256.Size*2 || len(right) != sha256.Size*2 {
		return false
	}
	return strings.EqualFold(left, right)
}

func packageError(code result.Code, message string) error {
	return &Error{Code: code, Message: message}
}
