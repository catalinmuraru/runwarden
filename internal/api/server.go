// Package api exposes scripts and runs over HTTP: a JSON REST API and a WebSocket stream for
// live output and input.
package api

import (
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/authz"
	"github.com/catalinmuraru/runwarden/internal/clientip"
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
	ClientIP       clientip.Resolver
	// Authenticate attaches the signed-in user (auth.WithUser) or rejects the request.
	// Without it every API request is rejected.
	Authenticate func(http.Handler) http.Handler
	Policy       authz.Policy
	// Activity, if set, records who uses the server and serves the admin pages.
	Activity *activity.Store
	// Helper scripts (dropdown values, defaults): concurrent calls per user and in total
	// (defaults 2 and 10), and how long their output is reused (0: not cached).
	ValuesMaxPerUser int
	ValuesMaxGlobal  int
	ValuesCacheTTL   time.Duration
}

// Server serves the API.
type Server struct {
	opts    Options
	exec    *executor.Executor
	hist    *history.Store
	helpers *helpers
	catalog atomic.Pointer[config.Catalog]
}

// New returns a Server. Set a catalog before serving.
func New(opts Options, ex *executor.Executor, hist *history.Store) *Server {
	if opts.MaxUploadBytes <= 0 {
		opts.MaxUploadBytes = 100 << 20
	}
	return &Server{opts: opts, exec: ex, hist: hist, helpers: newHelpers(opts.ValuesMaxPerUser, opts.ValuesMaxGlobal, opts.ValuesCacheTTL)}
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

// allowedScript returns the script if the requesting user may run it. Unknown and forbidden
// scripts look the same to the client.
func (s *Server) allowedScript(r *http.Request, name string) *config.Script {
	sc := s.script(name)
	if sc == nil || !s.opts.Policy.CanRun(currentUser(r), sc) {
		return nil
	}
	return sc
}

// canSee reports whether the requesting user may see and control a run.
func (s *Server) canSee(r *http.Request, meta history.Meta) bool {
	return s.opts.Policy.CanSeeRun(currentUser(r), meta.User, s.script(meta.Script))
}

// ReferencedGroups lists the groups that runner configs and admin settings refer to.
func (s *Server) ReferencedGroups() []string {
	groups := append([]string(nil), s.opts.Policy.AdminGroups...)
	if c := s.catalog.Load(); c != nil {
		for _, sc := range c.Scripts {
			for _, e := range append(append([]string(nil), sc.AllowedUsers...), sc.AdminUsers...) {
				if g, ok := strings.CutPrefix(strings.TrimSpace(e), "@"); ok {
					groups = append(groups, g)
				}
			}
		}
	}
	return groups
}

func currentUser(r *http.Request) auth.User {
	u, _ := auth.UserFrom(r.Context())
	return u
}

// Register adds the API routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	authenticate := s.opts.Authenticate
	if authenticate == nil {
		authenticate = func(http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeError(w, http.StatusUnauthorized, "sign in required")
			})
		}
	}
	h := func(pattern string, fn http.HandlerFunc) { mux.Handle(pattern, authenticate(s.trackActivity(fn))) }
	h("GET /api/scripts", s.listScripts)
	h("GET /api/scripts/{name}", s.getScript)
	h("GET /api/scripts/{name}/parameters/{param}/values", s.parameterValues)
	h("GET /api/scripts/{name}/parameters/{param}/files", s.parameterFiles)
	h("POST /api/scripts/{name}/runs", s.startRun)
	h("GET /api/runs", s.listRuns)
	h("GET /api/runs/{id}", s.getRun)
	h("GET /api/runs/{id}/output", s.runOutput)
	h("POST /api/runs/{id}/input", s.runInput)
	h("POST /api/runs/{id}/stop", s.runSignal)
	h("POST /api/runs/{id}/kill", s.runSignal)
	h("GET /api/runs/{id}/files/{file}", s.runFile)
	h("GET /api/runs/{id}/ws", s.runSocket)
	h("GET /api/admin/users", s.adminOnly(s.adminUsers))
	h("GET /api/admin/activity", s.adminOnly(s.adminActivity))
	// Unknown API paths get JSON instead of the UI's index page.
	mux.Handle("GET /api/", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	}))
}

// record writes an audit event for the requesting user.
func (s *Server) record(r *http.Request, e audit.Event) {
	u := currentUser(r)
	e.User, e.Groups = u.Name, u.Groups
	e.SourceIP = s.opts.ClientIP.IP(r)
	e.UserAgent = r.UserAgent()
	audit.Log(r.Context(), e)
}

// recordRunAction audits input, stop and kill on a run.
func (s *Server) recordRunAction(r *http.Request, action string, run *executor.Run, err error) {
	verb := map[string]string{audit.RunInput: "input sent to", audit.RunStop: "stop requested for", audit.RunKill: "kill requested for"}[action]
	e := audit.Event{Action: action, Category: "process", Type: "change", Success: err == nil,
		RunID: run.ID, Script: run.ScriptName, Message: fmt.Sprintf("%s run %s", verb, run.ID)}
	if err != nil {
		e.Reason = err.Error()
	}
	s.record(r, e)
}
