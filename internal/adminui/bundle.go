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

const BundleVersion = "memora.admin-bundle/v3"

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
		hash: "4fd00a5b894b82338d20669df71559c6ceb4970a2476d02d22a50190a0945653", size: 2580,
	},
	{
		file: "dist/assets/app.css", path: "/assets/app.css", contentType: "text/css; charset=utf-8",
		hash: "a007037314290cce71162a94516aa1c1b21633be531ed66c54509edee47d37e2", size: 24712,
	},
	{
		file: "dist/assets/app.js", path: "/assets/app.js", contentType: "text/javascript; charset=utf-8",
		hash: "6712c1698e4e1b2c10afd6bdf10dec30ce9e209fe5e56025dff8ad6d92a9b420", size: 7702,
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
		file: "dist/assets/vendor/dompurify-3.4.7.min.js", path: "/assets/vendor/dompurify-3.4.7.min.js", contentType: "text/javascript; charset=utf-8",
		hash: "f84e522876a6cfadecb89c173356409acec39f580c69018559c9a50e96299b0c", size: 26816,
	},
	{
		file: "dist/assets/catalog.js", path: "/assets/catalog.js", contentType: "text/javascript; charset=utf-8",
		hash: "9fb6501492bce24588b0eec76c8ab72b69465314949f166dbba11856166c5d71", size: 17096,
	},
	{
		file: "dist/assets/routes.js", path: "/assets/routes.js", contentType: "text/javascript; charset=utf-8",
		hash: "cb7ca8266942bca7b1a4f0e92d51639400c5879feb5f90540e9c97d191ebb3a2", size: 28407,
	},
	{
		file: "dist/assets/rows.js", path: "/assets/rows.js", contentType: "text/javascript; charset=utf-8",
		hash: "21ef5144869a04d08b3cc206eee60ac0ee988064ba69633c23d7fb0079cb1f02", size: 19428,
	},
	{
		file: "dist/assets/changes.js", path: "/assets/changes.js", contentType: "text/javascript; charset=utf-8",
		hash: "f2f113605afbf630a12d9720f1823712a93194423b15dcd58b9ae1e2efd48b12", size: 25655,
	},
	{
		file: "dist/assets/diffs.js", path: "/assets/diffs.js", contentType: "text/javascript; charset=utf-8",
		hash: "cc99a7f6dd62822829ae0bba4aa72face3d1815813ff5c66877fd646fed7a66d", size: 14135,
	},
	{
		file: "dist/assets/traces.js", path: "/assets/traces.js", contentType: "text/javascript; charset=utf-8",
		hash: "ae613bc84d47d3723ca811c86abbdd30ff12d9793b2247877abab5daa7b672ea", size: 27203,
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
		response.Header().Set("Cache-Control", "no-cache, must-revalidate")
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
