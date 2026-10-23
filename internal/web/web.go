// Package web serves the Vue UI, built into dist/ by `npm run build` in web/ and embedded in the binary.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// dist always contains a committed .gitkeep, so the embed compiles before the UI is built.
//
//go:embed all:dist
var dist embed.FS

// Handler serves the built UI, or 503 with a hint if the UI was not built.
func Handler() http.Handler {
	root, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // unreachable: dist is embedded above
	}
	if _, err := fs.Stat(root, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "UI not built: run `npm ci && npm run build` in web/", http.StatusServiceUnavailable)
		})
	}
	return http.FileServerFS(root)
}
