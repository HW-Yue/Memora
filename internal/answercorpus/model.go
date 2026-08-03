// Package answercorpus owns the deterministic, model-blind answer benchmark
// corpus contract. It does not execute an Agent or materialize a database.
package answercorpus

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	ManifestVersion    = "memora.answer-corpus-manifest/v1"
	FixtureVersion     = "memora.answer-fixture/v1"
	GroundTruthVersion = "memora.answer-ground-truth/v1"
	BlindTaskVersion   = "memora.answer-blind-task/v1"
)

type Manifest struct {
	Version           string   `json:"version"`
	CorpusID          string   `json:"corpus_id"`
	Revision          uint64   `json:"revision"`
	License           License  `json:"license"`
	Sources           []Source `json:"sources"`
	Fixture           Fixture  `json:"fixture"`
	Cases             []Case   `json:"cases"`
	GroundTruthSHA256 string   `json:"ground_truth_sha256"`
	Hash              string   `json:"hash"`
}

type License struct {
	ID             string `json:"id"`
	SPDXID         string `json:"spdx_id"`
	Path           string `json:"path"`
	RequiredNotice string `json:"required_notice"`
}

type Source struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	Locator       string `json:"locator"`
	LicenseID     string `json:"license_id"`
	ContentSHA256 string `json:"content_sha256"`
}

type Fixture struct {
	Version        string            `json:"version"`
	SnapshotID     string            `json:"snapshot_id"`
	SnapshotSHA256 string            `json:"snapshot_sha256"`
	Databases      []FixtureDatabase `json:"databases"`
}

type FixtureDatabase struct {
	ID      string         `json:"id"`
	Name    string         `json:"name"`
	Purpose string         `json:"purpose"`
	Scope   string         `json:"scope"`
	Tables  []FixtureTable `json:"tables"`
}

type FixtureTable struct {
	ID           string          `json:"id"`
	DatabaseID   string          `json:"database_id"`
	Name         string          `json:"name"`
	Purpose      string          `json:"purpose"`
	RowSemantics string          `json:"row_semantics"`
	Columns      []FixtureColumn `json:"columns"`
	Routes       []FixtureRoute  `json:"routes"`
	Rows         []FixtureRow    `json:"rows"`
}

type FixtureColumn struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Purpose      string `json:"purpose"`
	SemanticRole string `json:"semantic_role"`
}

type FixtureRoute struct {
	ID       string   `json:"id"`
	ParentID string   `json:"parent_id,omitempty"`
	Kind     string   `json:"kind"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Purpose  string   `json:"purpose"`
	Revision uint64   `json:"revision"`
	RowID    string   `json:"row_id,omitempty"`
}

type FixtureRow struct {
	ID       string            `json:"id"`
	SourceID string            `json:"source_id"`
	Values   map[string]string `json:"values"`
}

type Case struct {
	ID                  string     `json:"id"`
	Language            string     `json:"language"`
	Category            string     `json:"category"`
	Question            string     `json:"question"`
	AuthorizedDatabases []string   `json:"authorized_databases"`
	Budget              CaseBudget `json:"budget"`
}

type CaseBudget struct {
	MaxProviderCalls        uint64 `json:"max_provider_calls"`
	MaxToolCalls            uint64 `json:"max_tool_calls"`
	MaxOutputTokens         uint64 `json:"max_output_tokens"`
	BootstrapFrameUTF8Bytes uint64 `json:"bootstrap_frame_utf8_bytes"`
	ToolResultUTF8Bytes     uint64 `json:"tool_result_utf8_bytes"`
}

type GroundTruth struct {
	Version        string         `json:"version"`
	CorpusID       string         `json:"corpus_id"`
	SnapshotSHA256 string         `json:"snapshot_sha256"`
	Cases          []ExpectedCase `json:"cases"`
	Hash           string         `json:"hash"`
}

type ExpectedCase struct {
	CaseID          string         `json:"case_id"`
	Kind            string         `json:"kind"`
	ReferenceAnswer string         `json:"reference_answer"`
	Facts           []ExpectedFact `json:"facts"`
}

type ExpectedFact struct {
	DatabaseID string `json:"database_id"`
	TableID    string `json:"table_id"`
	RowID      string `json:"row_id"`
	ColumnID   string `json:"column_id"`
	Value      string `json:"value"`
}

type Bundle struct {
	Manifest    Manifest
	GroundTruth GroundTruth
}

type BlindTask struct {
	Version             string     `json:"version"`
	CorpusID            string     `json:"corpus_id"`
	CorpusRevision      uint64     `json:"corpus_revision"`
	CaseID              string     `json:"case_id"`
	SnapshotID          string     `json:"snapshot_id"`
	SnapshotSHA256      string     `json:"snapshot_sha256"`
	Question            string     `json:"question"`
	AuthorizedDatabases []string   `json:"authorized_databases"`
	Budget              CaseBudget `json:"budget"`
}

type Error struct{ Message string }

func (err *Error) Error() string { return "answer corpus: " + err.Message }

func Load(manifestPath, groundTruthPath string) (Bundle, error) {
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return Bundle{}, corpusError("read manifest: %v", err)
	}
	truthBytes, err := os.ReadFile(groundTruthPath)
	if err != nil {
		return Bundle{}, corpusError("read ground truth: %v", err)
	}
	var manifest Manifest
	if err := decodeStrict(manifestBytes, &manifest); err != nil {
		return Bundle{}, corpusError("decode manifest: %v", err)
	}
	var truth GroundTruth
	if err := decodeStrict(truthBytes, &truth); err != nil {
		return Bundle{}, corpusError("decode ground truth: %v", err)
	}
	bundle := Bundle{Manifest: manifest, GroundTruth: truth}
	if err := bundle.Validate(); err != nil {
		return Bundle{}, err
	}
	return bundle, nil
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	return encodeIndented(manifest)
}

func EncodeGroundTruth(truth GroundTruth) ([]byte, error) {
	if err := truth.Validate(); err != nil {
		return nil, err
	}
	return encodeIndented(truth)
}

func encodeIndented(value any) ([]byte, error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, corpusError("encode: %v", err)
	}
	return append(encoded, '\n'), nil
}

func decodeStrict(encoded []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}

func digest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", corpusError("digest encode: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func fixtureDigest(fixture Fixture) (string, error) {
	fixture.SnapshotSHA256 = ""
	return digest(fixture)
}

func truthDigest(truth GroundTruth) (string, error) {
	truth.Hash = ""
	return digest(truth)
}

func manifestDigest(manifest Manifest) (string, error) {
	manifest.Hash = ""
	return digest(manifest)
}

func corpusError(format string, arguments ...any) error {
	return &Error{Message: fmt.Sprintf(format, arguments...)}
}
