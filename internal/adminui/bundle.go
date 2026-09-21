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
		hash: "8f0362e24e4d5b3e3ec14ffb2d4ad8925d7d2f04314603b6d4e818e62f86b7df", size: 31632,
	},
	{
		file: "dist/assets/app.js", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8",
		hash: "2fdbb5c9204c8d4e66ccf449fce2013bf39a8c8791d8488995a90c4e7630da0b", size: 8208,
	},
	{
		file: "dist/assets/catalog.js", path: "/assets/catalog.js", contentType: "text/javascript; charset=utf-8",
		hash: "13c4598f0be512723d7af950a6c3ea9c2d7be761e696ae9d827ccbf2fe18f817", size: 16492,
	},
	{
		file: "dist/assets/changes.js", path: "/assets/changes.js", contentType: "text/javascript; charset=utf-8",
		hash: "4a62c2c2cd8f7398de049b20d8179a000ffe22bc180879759714872d9103a6ad", size: 24844,
	},
	{
		file: "dist/assets/diffs.js", path: "/assets/diffs.js", contentType: "text/javascript; charset=utf-8",
		hash: "60d2d4a41dbdb2c0c9b25c8801bdf83dfe62e5a00258886d10545c29db4ccb25", size: 14016,
	},
	{
		file: "dist/assets/routes.js", path: "/assets/routes.js", contentType: "text/javascript; charset=utf-8",
		hash: "eb6fd336ab7ded267f559e2f786bb368b7b2396597e311c6390530ae42692592", size: 44112,
	},
	{
		file: "dist/assets/rows.js", path: "/assets/rows.js", contentType: "text/javascript; charset=utf-8",
		hash: "9188bf6174d7d15ba5738ba1124ed0b8d643e8998c085985b2354f930b32a898", size: 18307,
	},
	{
		file: "dist/assets/search.js", path: "/assets/search.js", contentType: "text/javascript; charset=utf-8",
		hash: "6e4487629b7f74cdfcd59c82e098622855ca37cce54080e5b62594767bc676dc", size: 13861,
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
