package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

type response struct {
	code   int
	header http.Header
	body   string
}

func get(t *testing.T, h http.Handler, path string) response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
	r := rec.Result()
	defer func() { _ = r.Body.Close() }()
	b, _ := io.ReadAll(r.Body)
	return response{code: r.StatusCode, header: r.Header, body: string(b)}
}

const manifest = `{
  "index.html": {"file": "assets/index-1.js", "imports": ["_vue-9.js"], "css": ["assets/index-1.css"]},
  "login.html": {"file": "assets/login-2.js", "imports": ["_vue-9.js", "_api-3.js"], "css": ["assets/login-2.css"]},
  "_vue-9.js": {"file": "assets/vue-9.js"},
  "_api-3.js": {"file": "assets/api-3.js", "imports": ["_vue-9.js"]},
  "src/views/AdminView.vue": {"file": "assets/AdminView-4.js", "imports": ["_vue-9.js"]}
}`

func uiFS() fstest.MapFS {
	return fstest.MapFS{
		"index.html":            {Data: []byte("<html>app</html>")},
		"login.html":            {Data: []byte("<html>sign in</html>")},
		".vite/manifest.json":   {Data: []byte(manifest)},
		"assets/index-1.js":     {Data: []byte("app()")},
		"assets/index-1.css":    {Data: []byte("app{}")},
		"assets/login-2.js":     {Data: []byte("login()")},
		"assets/login-2.css":    {Data: []byte("login{}")},
		"assets/vue-9.js":       {Data: []byte("vue()")},
		"assets/api-3.js":       {Data: []byte("api()")},
		"assets/AdminView-4.js": {Data: []byte("admin()")},
		".gitkeep":              {Data: nil},
	}
}

func TestSignedIn(t *testing.T) {
	h := handlerFor(uiFS(), func(*http.Request) bool { return true })
	for _, page := range []string{"/", "/scripts/Backup%20Database", "/runs/20261024-165321.883-0004", "/scripts/v1.2%20deploy"} {
		r := get(t, h, page)
		if r.code != http.StatusOK || !strings.Contains(r.body, "app") {
			t.Errorf("%s: %d %q", page, r.code, r.body)
		}
		if r.header.Get("Cache-Control") != "no-store" || !strings.Contains(r.header.Get("Content-Security-Policy"), "default-src 'self'") ||
			r.header.Get("X-Content-Type-Options") != "nosniff" || r.header.Get("Vary") != "Cookie" {
			t.Errorf("%s headers: %v", page, r.header)
		}
	}
	for _, asset := range []string{"/assets/index-1.js", "/assets/AdminView-4.js", "/assets/login-2.js"} {
		if r := get(t, h, asset); r.code != http.StatusOK || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
			t.Errorf("%s: %d %v", asset, r.code, r.header)
		}
	}
	for _, missing := range []string{"/assets/gone.js", "/favicon.ico", "/.gitkeep", "/.vite/manifest.json", "/api/nope", "/auth/nope"} {
		if r := get(t, h, missing); r.code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", missing, r.code)
		}
	}
}

func TestSignedOutGetsOnlyTheSignInPage(t *testing.T) {
	h := handlerFor(uiFS(), func(*http.Request) bool { return false })
	for _, page := range []string{"/", "/admin", "/runs/x"} {
		if r := get(t, h, page); r.code != http.StatusOK || r.body != "<html>sign in</html>" {
			t.Errorf("%s: %d %q", page, r.code, r.body)
		}
	}
	for _, public := range []string{"/assets/login-2.js", "/assets/login-2.css", "/assets/vue-9.js", "/assets/api-3.js"} {
		if r := get(t, h, public); r.code != http.StatusOK {
			t.Errorf("%s = %d, want 200 (needed by the sign-in page)", public, r.code)
		}
	}
	for _, private := range []string{"/assets/index-1.js", "/assets/index-1.css", "/assets/AdminView-4.js"} {
		if r := get(t, h, private); r.code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 for signed-out visitors", private, r.code)
		}
	}
}

func TestUINotBuilt(t *testing.T) {
	if r := get(t, handlerFor(fstest.MapFS{".gitkeep": {}}, nil), "/"); r.code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", r.code)
	}
}
