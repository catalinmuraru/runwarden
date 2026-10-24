// Package api exposes scripts and runs over HTTP: a JSON REST API and a WebSocket stream for
// live output and input.
package api

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
)

// Options configure the API.
type Options struct {
	BaseDir        string   // resolves relative helper script paths, like the config loader
	DataDir        string   // uploads go to DataDir/uploads
	MaxUploadBytes int64    // per request
	AllowedOrigins []string // extra hosts allowed to open WebSockets (same host always is)
}

// Server serves the API.
type Server struct {
	opts    Options
	exec    *executor.Executor
	hist    *history.Store
	catalog atomic.Pointer[config.Catalog]
}

// New returns a Server. Set a catalog before serving.
func New(opts Options, ex *executor.Executor, hist *history.Store) *Server {
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 100 << 20
	}
	return &Server{opts: opts, exec: ex, hist: hist}
}

// SetCatalog replaces the scripts served, e.g. after a config reload.
func (s *Server) SetCatalog(c *config.Catalog) { s.catalog.Store(c) }

// Ready reports whether scripts have been loaded.
func (s *Server) Ready() bool { return s.catalog.Load() != nil }

func (s *Server) script(name string) *config.Script {
	c := s.catalog.Load()
	if c == nil {
		return nil
	}
	return c.Script(name)
}

// Register adds the API routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	h := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, withUser(fn)) }
	h("GET /api/scripts", s.listScripts)
	h("GET /api/scripts/{name}", s.getScript)
	h("GET /api/scripts/{name}/parameters/{param}/values", s.parameterValues)
	h("POST /api/scripts/{name}/runs", s.startRun)
	h("GET /api/runs", s.listRuns)
	h("GET /api/runs/{id}", s.getRun)
	h("GET /api/runs/{id}/output", s.runOutput)
	h("POST /api/runs/{id}/input", s.runInput)
	h("POST /api/runs/{id}/stop", s.runSignal)
	h("POST /api/runs/{id}/kill", s.runSignal)
	h("GET /api/runs/{id}/files/{file}", s.runFile)
	h("GET /api/runs/{id}/ws", s.runSocket)
	// Unknown API paths get JSON instead of the UI's index page.
	mux.Handle("GET /api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
}

// audit writes one structured line per user action.
func audit(r *http.Request, action string, attrs ...any) {
	attrs = append([]any{"audit", true, "action", action, "user", userFrom(r.Context()).Name}, attrs...)
	slog.Info("audit", attrs...)
}
