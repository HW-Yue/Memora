package agent

import (
	"errors"
	"io"
	"strings"
)

const (
	SourcePutRequestVersion = "memora.source-put-request/v1"
	SourceReferenceVersion  = "memora.source-reference/v1"
)

var (
	ErrSourceStoreInvalid  = errors.New("Source Store input is invalid")
	ErrSourceStoreNotFound = errors.New("Source Store reference was not found")
	ErrSourceStoreConflict = errors.New("Source Store reference has different content")
	ErrSourceStoreQuota    = errors.New("Source Store quota exceeded")
	ErrSourceStoreDigest   = errors.New("Source Store content digest does not match")
	ErrSourceStoreCorrupt  = errors.New("Source Store is corrupt")
)

type SourceStoreConfig struct {
	MaxObjectBytes   uint64 `json:"max_object_bytes"`
	MaxJobBytes      uint64 `json:"max_job_bytes"`
	MaxPhysicalBytes uint64 `json:"max_physical_bytes"`
	MaxSourcesPerJob uint64 `json:"max_sources_per_job"`
}

type SourcePutRequest struct {
	Version        string `json:"version"`
	JobID          string `json:"job_id"`
	SourceID       string `json:"source_id"`
	ExpectedSHA256 string `json:"expected_sha256"`
	MediaType      string `json:"media_type"`
	FileName       string `json:"file_name"`
}

type SourceReference struct {
	Version   string `json:"version"`
	JobID     string `json:"job_id"`
	SourceID  string `json:"source_id"`
	SHA256    string `json:"sha256"`
	Bytes     uint64 `json:"bytes"`
	MediaType string `json:"media_type"`
	FileName  string `json:"file_name"`
}

func (reference SourceReference) Validate() error {
	if reference.Version != SourceReferenceVersion || !validSourceIntakeID(reference.JobID, 128) ||
		!validSourceIntakeID(reference.SourceID, 128) || !validSourceSHA256(reference.SHA256) ||
		reference.Bytes == 0 || !validSourceMediaType(reference.MediaType) ||
		!validSourceFileName(reference.FileName) {
		return ErrSourceStoreInvalid
	}
	return nil
}

type SourcePutReceipt struct {
	Reference    SourceReference `json:"reference"`
	Replayed     bool            `json:"replayed"`
	ObjectReused bool            `json:"object_reused"`
}

type SourceCleanupReceipt struct {
	ReferencesRemoved uint64 `json:"references_removed"`
	ObjectsRemoved    uint64 `json:"objects_removed"`
	BytesRemoved      uint64 `json:"bytes_removed"`
}

type SourceStoreStats struct {
	References    uint64 `json:"references"`
	Objects       uint64 `json:"objects"`
	LogicalBytes  uint64 `json:"logical_bytes"`
	PhysicalBytes uint64 `json:"physical_bytes"`
}

type SourceReadCloser interface {
	io.Reader
	io.Closer
}

type SourceRandomAccess interface {
	io.Reader
	io.ReaderAt
	io.Seeker
	io.Closer
}

func validSourceStoreConfig(config SourceStoreConfig) bool {
	const maxConfiguredBytes = uint64(1 << 50)
	return config.MaxObjectBytes > 0 && config.MaxObjectBytes <= maxConfiguredBytes &&
		config.MaxJobBytes > 0 && config.MaxJobBytes <= maxConfiguredBytes &&
		config.MaxPhysicalBytes > 0 && config.MaxPhysicalBytes <= maxConfiguredBytes &&
		config.MaxSourcesPerJob > 0 && config.MaxSourcesPerJob <= 1_000_000
}

func validSourcePutRequest(request SourcePutRequest) bool {
	return request.Version == SourcePutRequestVersion && validSourceIntakeID(request.JobID, 128) &&
		validSourceIntakeID(request.SourceID, 128) && validSourceSHA256(request.ExpectedSHA256) &&
		validSourceMediaType(request.MediaType) && validSourceFileName(request.FileName)
}

func validSourceMediaType(value string) bool {
	return validSourceIntakeID(value, 160) && strings.Contains(value, "/") &&
		!strings.ContainsAny(value, " ;\"")
}

func validSourceFileName(value string) bool {
	return validSourceIntakeText(value, 255) && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "/\\\r\n\x00") && value != "." && value != ".."
}
