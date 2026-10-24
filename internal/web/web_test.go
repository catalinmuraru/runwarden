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

func TestSinglePageApp(t *testing.T) {
	h := handlerFor(fstest.MapFS{
		"index.html":         {Data: []byte("<html>app</html>")},
		"assets/app-1234.js": {Data: []byte("console.log(1)")},
		".gitkeep":           {Data: nil},
	})

	for _, page := range []string{"/", "/scripts/Backup%20Database", "/runs/20261024-1", "/login"} {
		r := get(t, h, page)
		if r.code != http.StatusOK || !strings.Contains(r.body, "app") {
			t.Errorf("%s: %d %q", page, r.code, r.body)
		}
		if r.header.Get("Cache-Control") != "no-cache" || !strings.Contains(r.header.Get("Content-Security-Policy"), "default-src 'self'") ||
			r.header.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s headers: %v", page, r.header)
		}
	}

	r := get(t, h, "/assets/app-1234.js")
	if r.code != http.StatusOK || r.body != "console.log(1)" || !strings.Contains(r.header.Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %q %v", r.code, r.body, r.header)
	}
	for _, missing := range []string{"/assets/gone.js", "/favicon.ico", "/.gitkeep", "/api/nope", "/auth/nope"} {
		if r := get(t, h, missing); r.code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", missing, r.code)
		}
	}
}

func TestUINotBuilt(t *testing.T) {
	if r := get(t, handlerFor(fstest.MapFS{".gitkeep": {}}), "/"); r.code != http.StatusServiceUnavailable {
		t.Errorf("status = %d", r.code)
	}
}
