package adminui

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestEmbeddedBundleHasFrozenOfflineAssets(t *testing.T) {
	t.Parallel()

	bundle, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	manifest := bundle.Manifest()
	if manifest.Version != BundleVersion || len(manifest.Assets) != 3 {
		t.Fatalf("manifest = %#v", manifest)
	}
	for _, asset := range manifest.Assets {
		if len(asset.SHA256) != 64 || asset.Size == 0 || asset.ContentType == "" {
			t.Fatalf("asset = %#v", asset)
		}
	}
	index, err := fs.ReadFile(embeddedFiles, "dist/index.html")
	if err != nil {
		t.Fatal(err)
	}
	text := string(index)
	for _, forbidden := range []string{"https://", "http://", "<style", "localStorage", "sessionStorage"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("index contains forbidden %q", forbidden)
		}
	}
	if !strings.Contains(text, `src="/assets/app.js"`) || !strings.Contains(text, `href="/assets/app.css"`) {
		t.Fatalf("index does not use embedded assets: %s", text)
	}
	script, err := fs.ReadFile(embeddedFiles, "dist/assets/app.js")
	if err != nil {
		t.Fatal(err)
	}
	javascript := string(script)
	for _, forbidden := range []string{"https://", "http://", "localStorage", "sessionStorage", "document.cookie", "console."} {
		if strings.Contains(javascript, forbidden) {
			t.Fatalf("JavaScript contains forbidden %q", forbidden)
		}
	}
	clearAt := strings.Index(javascript, "clearFragment();")
	fetchAt := strings.Index(javascript, `fetch("/api/v1/session"`)
	if clearAt < 0 || fetchAt < 0 || clearAt >= fetchAt || !strings.Contains(javascript, "export async function executeMSQL") {
		t.Fatalf("JavaScript does not clear fragment before bootstrap or expose the module API client")
	}
}

func TestBundleServesDeepLinksAssetsAndSecurityHeaders(t *testing.T) {
	t.Parallel()

	bundle, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/catalog/work", "/routes/route_1"} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Memora Admin") {
			t.Fatalf("GET %s status=%d body=%q", path, response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Security-Policy") == "" ||
			response.Header().Get("Referrer-Policy") != "no-referrer" ||
			response.Header().Get("X-Frame-Options") != "DENY" ||
			response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("GET %s headers = %#v", path, response.Header())
		}
	}
	for _, path := range []string{"/assets/app.js", "/assets/app.css"} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK || response.Header().Get("ETag") == "" ||
			response.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatalf("GET %s status=%d headers=%#v", path, response.Code, response.Header())
		}
	}
	for _, path := range []string{"/assets/missing.js", "/api/v1/unknown"} {
		response := httptest.NewRecorder()
		bundle.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusNotFound || strings.Contains(response.Body.String(), "Memora Admin") {
			t.Fatalf("GET %s status=%d body=%q", path, response.Code, response.Body.String())
		}
	}
	response := httptest.NewRecorder()
	bundle.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/catalog", strings.NewReader("{}")))
	if response.Code != http.StatusMethodNotAllowed || strings.Contains(response.Body.String(), "Memora Admin") {
		t.Fatalf("POST deep link status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestBundleRejectsMissingExtraAndTamperedFiles(t *testing.T) {
	t.Parallel()

	files := copyEmbeddedFiles(t)
	delete(files, "dist/assets/app.js")
	if _, err := New(files); err == nil {
		t.Fatal("bundle missing JavaScript succeeded")
	}
	files = copyEmbeddedFiles(t)
	files["dist/extra.txt"] = &fstest.MapFile{Data: []byte("extra")}
	if _, err := New(files); err == nil {
		t.Fatal("bundle with extra asset succeeded")
	}
	files = copyEmbeddedFiles(t)
	files["dist/index.html"].Data = append(files["dist/index.html"].Data, []byte("tampered")...)
	if _, err := New(files); err == nil {
		t.Fatal("tampered bundle succeeded")
	}
}

func copyEmbeddedFiles(t *testing.T) fstest.MapFS {
	t.Helper()
	files := fstest.MapFS{}
	err := fs.WalkDir(embeddedFiles, "dist", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		content, err := fs.ReadFile(embeddedFiles, path)
		if err != nil {
			return err
		}
		files[path] = &fstest.MapFile{Data: append([]byte(nil), content...), Mode: 0o444}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
