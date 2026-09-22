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

const BundleVersion = "memora.admin-bundle/v4"

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
		file: "dist/assets/app.css", path: "/assets/app.css", contentType: "text/css; charset=utf-8",
		hash: "5700e9652aee51862d9daa3da3d40cd8e5b3472fa8c7c5e6a9ab412d1613650c", size: 29809,
	},
	{
		file: "dist/assets/app.js", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8",
		hash: "c3920a82ff39c8442853be75d394beb3c4a860b90ae9e369b54e7152a50d3477", size: 9225,
	},
	{
		file: "dist/assets/catalog.js", path: "/assets/catalog.js", contentType: "text/javascript; charset=utf-8",
		hash: "dc49900fa4cc8b6c0fd178280f01e6f9b64f17fb7537d9e3f64e08194c24d8b6", size: 16417,
	},
	{
		file: "dist/assets/changes.js", path: "/assets/changes.js", contentType: "text/javascript; charset=utf-8",
		hash: "4a62c2c2cd8f7398de049b20d8179a000ffe22bc180879759714872d9103a6ad", size: 24844,
	},
	{
		file: "dist/assets/diffs.js", path: "/assets/diffs.js", contentType: "text/javascript; charset=utf-8",
		hash: "8eb1b1dabac92be00858c74a8fe3a8eae4583a22d761524c5de525f3b1887a57", size: 13965,
	},
	{
		file: "dist/assets/routes.js", path: "/assets/routes.js", contentType: "text/javascript; charset=utf-8",
		hash: "e969062ea13b6e8c72cbad481264c77a99a902ff1739a0bde2b1e414915a7aa8", size: 46891,
	},
	{
		file: "dist/assets/rows.js", path: "/assets/rows.js", contentType: "text/javascript; charset=utf-8",
		hash: "1f4dbfede2e5ae4858e5eda4b103a2be4e10a146a3e4d65101300a8a47403d3e", size: 17759,
	},
	{
		file: "dist/assets/search.js", path: "/assets/search.js", contentType: "text/javascript; charset=utf-8",
		hash: "526452ae7a0a5cb5aff841ffda0f0a71ebe15a3e8879eb0e9d88086407961789", size: 17525,
	},
	{
		file: "dist/assets/traces.js", path: "/assets/traces.js", contentType: "text/javascript; charset=utf-8",
		hash: "d6ace5f46d64a05290564d002800fc01c7adcce063a0611230c90e93bbda126c", size: 26884,
	},
	{
		file: "dist/assets/vendor/dompurify-3.4.7.min.js", path: "/assets/vendor/dompurify-3.4.7.min.js", contentType: "text/javascript; charset=utf-8",
		hash: "f84e522876a6cfadecb89c173356409acec39f580c69018559c9a50e96299b0c", size: 26816,
	},
	{
		file: "dist/assets/vendor/g6-5.1.1.min.js", path: "/assets/vendor/g6-5.1.1.min.js", contentType: "text/javascript; charset=utf-8",
		hash: "3e091a94fd08994a383ff34bfba256bb8e382e4be4042197a206d2ecc0957331", size: 1383347,
	},
	{
		file: "dist/assets/vendor/markdown-it-15.0.0.min.js", path: "/assets/vendor/markdown-it-15.0.0.min.js", contentType: "text/javascript; charset=utf-8",
		hash: "8d0f6aca8f4de3321b6d07e03286176c59ec19b7b84abb6eb31f0fa795e83abc", size: 114128,
	},
	{
		file: "dist/index.html", path: "/", contentType: "text/html; charset=utf-8",
		hash: "875caf324117277c4b1449aa98bda36e541ee4559311a2e2e4819e6f83daed44", size: 1973,
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

func serveAsset(response http.ResponseWriter, request *http.Request, asset bundledAsset, revalidate bool) {
	response.Header().Set("Content-Type", asset.ContentType)
	response.Header().Set("ETag", `"sha256:`+asset.SHA256+`"`)
	if revalidate {
		// 强制禁用缓存，确保开发时每次都能获取最新内容
		response.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, proxy-revalidate")
		response.Header().Set("Pragma", "no-cache")
		response.Header().Set("Expires", "0")
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
