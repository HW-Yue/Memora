// Package answerbenchmark owns the model-blind end-to-end answer benchmark
// host. Database setup and Query Agent access are restricted to versioned MSQL.
package answerbenchmark

import (
	"context"
	"errors"

	"github.com/HW-Yue/Memora/internal/agent"
	"github.com/HW-Yue/Memora/internal/answercorpus"
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

type Materializer struct{ msql agent.MSQLExecutor }

func NewMaterializer(msql agent.MSQLExecutor) (*Materializer, error) {
	if msql == nil {
		return nil, ErrInvalidMaterializer
	}
	return &Materializer{msql: msql}, nil
}

func (materializer *Materializer) Materialize(
	context.Context,
	answercorpus.Manifest,
) (MaterializationReceipt, error) {
	return MaterializationReceipt{}, ErrMaterialization
}
