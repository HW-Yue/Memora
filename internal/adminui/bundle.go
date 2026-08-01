package adminui

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sort"
	"strings"
)

const BundleVersion = "memora.admin-bundle/v1"

//go:embed dist
var embeddedFiles embed.FS

type Asset struct {
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	SHA256      string `json:"sha256"`
	Size        int    `json:"size"`
}

type Manifest struct {
	Version string  `json:"version"`
	Assets  []Asset `json:"assets"`
}

type assetSpec struct {
	file        string
	path        string
	contentType string
	hash        string
	size        int
}

var frozenAssets = []assetSpec{
	{
		file: "dist/index.html", path: "/", contentType: "text/html; charset=utf-8",
		hash: "bc49b0e160e4c47f0dad9022af7a8888a0bcdf85f84e84db46b1760ad99ec97d", size: 2328,
	},
	{
		file: "dist/assets/app.css", path: "/assets/app.css", contentType: "text/css; charset=utf-8",
		hash: "84bbb3491a01bdb874eb3c5446e798ffe074987285b84f34e38a1d1ff2e37614", size: 13882,
	},
	{
		file: "dist/assets/app.js", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8",
		hash: "9b71677a72eb8ceb2b375b38a4387913a61fe031aac75d2e3b0961f27b0b08b3", size: 5950,
	},
	{
		file: "dist/assets/catalog.js", path: "/assets/catalog.js", contentType: "text/javascript; charset=utf-8",
		hash: "7b1e7a110674784802e10ae95d7f88bd3ac70d24bf92f1c50c3dd10f81a31f69", size: 16078,
	},
	{
		file: "dist/assets/routes.js", path: "/assets/routes.js", contentType: "text/javascript; charset=utf-8",
		hash: "423fd34fceb578ad970f8c4008f3adc38faf9f52bc34007285c0cb0d3d49fc41", size: 16893,
	},
	{
		file: "dist/assets/rows.js", path: "/assets/rows.js", contentType: "text/javascript; charset=utf-8",
		hash: "208fa50035e319f0cc92390daaa3b0c4c853b2abc95cb35d2159e3680537644b", size: 16394,
	},
	{
		file: "dist/assets/changes.js", path: "/assets/changes.js", contentType: "text/javascript; charset=utf-8",
		hash: "20b66eb3aecbfcffa98f7908f1ad60d7485b947f3174f0d9992a04107ced7a12", size: 25270,
	},
}

type bundledAsset struct {
	Asset
	content []byte
}

type Bundle struct {
	index    bundledAsset
	assets   map[string]bundledAsset
	manifest Manifest
}

func Embedded() (*Bundle, error) { return New(embeddedFiles) }

func New(source fs.FS) (*Bundle, error) {
	if source == nil {
		return nil, errors.New("admin bundle filesystem is nil")
	}
	files := []string{}
	err := fs.WalkDir(source, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("admin bundle file %q is not regular", path)
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect admin bundle: %w", err)
	}
	sort.Strings(files)
	expected := make([]string, len(frozenAssets))
	for index := range frozenAssets {
		expected[index] = frozenAssets[index].file
	}
	sort.Strings(expected)
	if len(files) != len(expected) {
		return nil, errors.New("admin bundle file set does not match manifest")
	}
	for index := range files {
		if files[index] != expected[index] {
			return nil, errors.New("admin bundle file set does not match manifest")
		}
	}

	bundle := &Bundle{
		assets:   make(map[string]bundledAsset, len(frozenAssets)-1),
		manifest: Manifest{Version: BundleVersion, Assets: []Asset{}},
	}
	for _, spec := range frozenAssets {
		content, err := fs.ReadFile(source, spec.file)
		if err != nil {
			return nil, fmt.Errorf("read admin bundle asset %q: %w", spec.file, err)
		}
		digest := sha256.Sum256(content)
		if len(content) != spec.size || hex.EncodeToString(digest[:]) != spec.hash {
			return nil, fmt.Errorf("admin bundle asset %q failed integrity validation", spec.file)
		}
		asset := bundledAsset{
			Asset: Asset{
				Path: spec.path, ContentType: spec.contentType, SHA256: spec.hash, Size: spec.size,
			},
			content: append([]byte(nil), content...),
		}
		bundle.manifest.Assets = append(bundle.manifest.Assets, asset.Asset)
		if spec.path == "/" {
			bundle.index = asset
		} else {
			bundle.assets[spec.path] = asset
		}
	}
	return bundle, nil
}

func (bundle *Bundle) Manifest() Manifest {
	manifest := bundle.manifest
	manifest.Assets = append([]Asset(nil), bundle.manifest.Assets...)
	return manifest
}

func (bundle *Bundle) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	shellHeaders(response)
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		response.Header().Set("Allow", "GET, HEAD")
		http.Error(response, "method is not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := request.URL.Path
	if path == "/api" || strings.HasPrefix(path, "/api/") ||
		path == "/assets" || strings.HasPrefix(path, "/assets/") {
		asset, exists := bundle.assets[path]
		if !exists {
			http.NotFound(response, request)
			return
		}
		serveAsset(response, request, asset, true)
		return
	}
	serveAsset(response, request, bundle.index, false)
}

func serveAsset(response http.ResponseWriter, request *http.Request, asset bundledAsset, immutable bool) {
	response.Header().Set("Content-Type", asset.ContentType)
	response.Header().Set("ETag", `"sha256:`+asset.SHA256+`"`)
	if immutable {
		response.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		response.Header().Set("Cache-Control", "no-store")
	}
	if request.Header.Get("If-None-Match") == response.Header().Get("ETag") {
		response.WriteHeader(http.StatusNotModified)
		return
	}
	response.WriteHeader(http.StatusOK)
	if request.Method == http.MethodGet {
		_, _ = response.Write(asset.content)
	}
}

func shellHeaders(response http.ResponseWriter) {
	response.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'")
	response.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	response.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
	response.Header().Set("Referrer-Policy", "no-referrer")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	response.Header().Set("X-Frame-Options", "DENY")
}
