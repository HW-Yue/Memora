package evaldata

import (
	"net/http"
)

const (
	RegistryVersion = "memora.external-evaluation-registry/v1"
	ReceiptVersion  = "memora.external-evaluation-receipt/v1"
)

type Registry struct {
	Version  string    `json:"version"`
	Datasets []Dataset `json:"datasets"`
}

type Dataset struct {
	ID      string  `json:"id"`
	Title   string  `json:"title"`
	Role    string  `json:"role"`
	License License `json:"license"`
	Source  Source  `json:"source"`
}

type License struct {
	SPDX string `json:"spdx"`
	URL  string `json:"url"`
	Note string `json:"note,omitempty"`
}

type Source struct {
	Kind      string     `json:"kind"`
	URL       string     `json:"url,omitempty"`
	Revision  string     `json:"revision,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
}

type Artifact struct {
	Path   string `json:"path"`
	URL    string `json:"url"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type Options struct {
	Root        string
	DatasetIDs  []string
	VerifyOnly  bool
	Progress    func(Progress)
	HTTPRetries int
	HTTPClient  *http.Client
}

type Progress struct {
	DatasetID string
	Path      string
	State     string
	Bytes     int64
}

type Receipt struct {
	Version        string         `json:"version"`
	RegistrySHA256 string         `json:"registry_sha256"`
	Datasets       []DatasetState `json:"datasets"`
}

type DatasetState struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Revision string `json:"revision,omitempty"`
	Files    int    `json:"files,omitempty"`
	Bytes    int64  `json:"bytes,omitempty"`
}
