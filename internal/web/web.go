// Package web serves the Vue UI, built into dist/ by `npm run build` in web/ and embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist always contains a committed .gitkeep, so the embed compiles before the UI is built.
//
//go:embed all:dist
var dist embed.FS

// contentSecurityPolicy allows only same-origin resources. Inline styles are needed by the
// terminal component, which sizes its elements at runtime.
const contentSecurityPolicy = "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// staticExt lists extensions of files, as opposed to pages: a missing one is a 404 rather than
// the app. Page paths may contain dots too (run IDs do), so this can't be "any extension".
var staticExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".json": true, ".txt": true, ".xml": true,
	".ico": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true,
	".woff": true, ".woff2": true, ".ttf": true, ".wasm": true,
}

// Handler serves the built UI, or 503 with a hint if the UI was not built.
func Handler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // unreachable: dist is embedded above
	}
	return handlerFor(root)
}

// handlerFor serves a single-page app from root: existing files as they are (hashed assets
// cached for a year), any other page path as index.html so deep links work.
func handlerFor(root fs.FS) http.Handler {
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "UI not built: run `npm ci && npm run build` in web/", http.StatusServiceUnavailable)
		})
	}
	files := http.FileServerFS(root)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")

		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		switch {
		case name == "" || name == "index.html":
		case strings.HasPrefix(path.Base(name), "."), strings.HasPrefix(name, "api/"), strings.HasPrefix(name, "auth/"):
			http.NotFound(w, r)
			return
		default:
			if info, err := fs.Stat(root, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "assets/") {
					h.Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
			if strings.HasPrefix(name, "assets/") || staticExt[strings.ToLower(path.Ext(name))] {
				http.NotFound(w, r) // a missing file, not a page
				return
			}
		}
		h.Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, root, "index.html")
	})
}
