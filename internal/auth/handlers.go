package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"

	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/oauth2"

	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/clientip"
)

// Options configure the auth handlers.
type Options struct {
	Type           string // config.AuthOIDC, AuthGitLab, AuthLDAP or AuthNone
	Sessions       *Sessions
	Redirect       RedirectProvider // oidc, gitlab
	Password       PasswordProvider // ldap
	AllowedOrigins []string         // extra hosts trusted for state-changing requests
	ClientIP       clientip.Resolver
	IsAdmin        func(User) bool
	// RelevantGroups lists the groups that matter for authorization. When a user has too many
	// groups for the session cookie, only these are kept.
	RelevantGroups func() []string
	// Failed sign-ins allowed per client address (15 minutes) and in total (1 minute), on top of
	// the per user+address limit. Zero means no limit.
	MaxFailuresPerIP  int
	MaxFailuresGlobal int
}

// Handler serves login, callback, logout and /api/me, and authenticates API requests.
type Handler struct {
	opts   Options
	limit  *limiter // failures per user name + client address
	perIP  *limiter // failures per client address
	global *limiter // failures from everywhere
	noted  *limiter // refusals already audited, per client address
}

// New returns a Handler.
func New(opts Options) *Handler {
	if opts.IsAdmin == nil {
		opts.IsAdmin = func(User) bool { return false }
	}
	return &Handler{
		opts:   opts,
		limit:  newLimiter(5, 15*time.Minute),
		perIP:  newLimiter(opts.MaxFailuresPerIP, 15*time.Minute),
		global: newLimiter(opts.MaxFailuresGlobal, time.Minute),
		noted:  newLimiter(1, 15*time.Minute),
	}
}

// Register adds the auth routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", h.loginRedirect)
	mux.Handle("POST /auth/login", h.sameOrigin(http.HandlerFunc(h.loginPassword)))
	mux.HandleFunc("GET /auth/callback", h.callback)
	mux.Handle("POST /auth/logout", h.sameOrigin(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/me", h.Middleware(http.HandlerFunc(h.me)))
	mux.HandleFunc("GET /auth/config", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"type": h.opts.Type})
	})
}

// Middleware requires a signed-in user and attaches it to the request context. It also rejects
// state-changing requests coming from other sites.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return h.sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := Anonymous, true
		if h.opts.Type != "none" {
			u, ok = h.opts.Sessions.Get(r)
		}
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "sign in required"})
			return
		}
		next.ServeHTTP(w, r.WithContext(WithUser(r.Context(), u)))
	}))
}

// sameOrigin rejects cross-site POST/PUT/DELETE requests (CSRF), in addition to SameSite cookies.
func (h *Handler) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" || !h.originAllowed(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request refused"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) originAllowed(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true // not a browser cross-origin request
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Host)
	if host == strings.ToLower(r.Host) {
		return true
	}
	return slices.ContainsFunc(h.opts.AllowedOrigins, func(p string) bool {
		ok, _ := path.Match(strings.ToLower(p), host)
		return ok
	})
}

func (h *Handler) loginRedirect(w http.ResponseWriter, r *http.Request) {
	if h.opts.Redirect == nil {
		if h.opts.Type == "none" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "sign in with POST /auth/login"})
		return
	}
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Next: safeNext(r.URL.Query().Get("next"))}
	if err := h.opts.Sessions.setLogin(w, st); err != nil {
		slog.Error("auth: login state", "err", err)
		http.Error(w, "Sign-in is unavailable.", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, h.opts.Redirect.AuthURL(st.State, st.Nonce, st.Verifier), http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if h.opts.Redirect == nil {
		http.NotFound(w, r)
		return
	}
	if h.throttled(r, User{}) {
		http.Error(w, "Too many failed sign-ins, try again later.", http.StatusTooManyRequests)
		return
	}
	st, ok := h.opts.Sessions.takeLogin(w, r)
	if !ok {
		http.Error(w, "Sign-in expired or was started in another browser. Please sign in again.", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		slog.Warn("auth: provider refused sign-in", "error", e, "description", q.Get("error_description"))
		h.failed(r)
		h.auditLogin(r, User{}, false, "refused by the identity provider: "+e)
		http.Error(w, "Sign-in was cancelled or refused by the identity provider.", http.StatusUnauthorized)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		h.failed(r)
		h.auditLogin(r, User{}, false, "state mismatch")
		http.Error(w, "Sign-in state mismatch. Please sign in again.", http.StatusBadRequest)
		return
	}
	u, err := h.opts.Redirect.Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		slog.Warn("auth: sign-in failed", "err", err)
		h.failed(r)
		h.auditLogin(r, User{}, false, "sign-in with the identity provider failed")
		http.Error(w, "Sign-in failed.", http.StatusUnauthorized)
		return
	}
	if err := h.startSession(w, u); err != nil {
		slog.Error("auth: session", "user", u.Name, "err", err)
		http.Error(w, "Sign-in failed.", http.StatusInternalServerError)
		return
	}
	h.auditLogin(r, u, true, "")
	http.Redirect(w, r, st.Next, http.StatusFound)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) loginPassword(w http.ResponseWriter, r *http.Request) {
	if h.opts.Password == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "password sign-in is not enabled"})
		return
	}
	var c credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "send {\"username\": ..., \"password\": ...}"})
		return
	}
	ip := h.opts.ClientIP.IP(r)
	key := strings.ToLower(strings.TrimSpace(c.Username)) + "|" + ip
	if !h.limit.allowed(key) {
		if h.noted.first(key) {
			h.auditLogin(r, User{Name: c.Username}, false, "too many failed sign-ins")
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins, try again later"})
		return
	}
	if h.throttled(r, User{Name: c.Username}) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins, try again later"})
		return
	}
	u, err := h.opts.Password.Authenticate(r.Context(), c.Username, c.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		h.limit.fail(key)
		h.failed(r)
		h.auditLogin(r, User{Name: c.Username}, false, "invalid user name or password")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": ErrInvalidCredentials.Error()})
		return
	case err != nil:
		slog.Error("auth: directory unavailable", "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sign-in service unavailable"})
		return
	}
	h.limit.reset(key)
	if err := h.startSession(w, u); err != nil {
		slog.Error("auth: session", "user", u.Name, "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "sign-in failed"})
		return
	}
	h.auditLogin(r, u, true, "")
	writeJSON(w, http.StatusOK, h.meBody(u))
}

// startSession sets the session cookie, keeping only relevant groups if they don't all fit.
func (h *Handler) startSession(w http.ResponseWriter, u User) error {
	err := h.opts.Sessions.Set(w, u)
	if !errors.Is(err, errCookieTooLarge) || h.opts.RelevantGroups == nil {
		return err
	}
	relevant := h.opts.RelevantGroups()
	all := len(u.Groups)
	u.Groups = slices.DeleteFunc(slices.Clone(u.Groups), func(g string) bool {
		return !slices.ContainsFunc(relevant, func(r string) bool { return strings.EqualFold(r, g) })
	})
	slog.Info("auth: kept only groups used for authorization", "user", u.Name, "groups", len(u.Groups), "of", all)
	return h.opts.Sessions.Set(w, u)
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	if h.opts.Sessions != nil {
		if u, ok := h.opts.Sessions.Get(r); ok {
			h.record(r, u, audit.Event{Action: audit.Logout, Category: "authentication", Type: "end", Success: true, Message: "signed out: " + u.Name})
		}
		h.opts.Sessions.Clear(w)
	}
	body := map[string]string{}
	if h.opts.Redirect != nil {
		if u := h.opts.Redirect.LogoutURL(); u != "" {
			body["logout_url"] = u
		}
	}
	writeJSON(w, http.StatusOK, body)
}

type meResponse struct {
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	Admin    bool     `json:"admin"`
	AuthType string   `json:"auth_type"`
}

func (h *Handler) meBody(u User) meResponse {
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	return meResponse{Name: u.Name, Groups: groups, Admin: h.opts.IsAdmin(u), AuthType: h.opts.Type}
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	writeJSON(w, http.StatusOK, h.meBody(u))
}

// failed counts a failed sign-in against the per-address and global limits.
func (h *Handler) failed(r *http.Request) {
	h.perIP.fail(h.opts.ClientIP.IP(r))
	h.global.fail("")
}

// throttled reports whether sign-ins are refused for this address or for everyone. Only the
// first refusal per address and window is audited, so a flood cannot fill the logs.
func (h *Handler) throttled(r *http.Request, u User) bool {
	ip := h.opts.ClientIP.IP(r)
	if h.perIP.allowed(ip) && h.global.allowed("") {
		return false
	}
	if h.noted.first(ip) {
		h.auditLogin(r, u, false, "too many failed sign-ins; further refusals from this address are not logged for 15 minutes")
	}
	return true
}

// auditLogin records a sign-in attempt. Passwords are never passed here.
func (h *Handler) auditLogin(r *http.Request, u User, success bool, reason string) {
	msg := "signed in: " + u.Name
	if !success {
		msg = "sign-in failed: " + reason
	}
	h.record(r, u, audit.Event{Action: audit.Login, Category: "authentication", Type: "start",
		Success: success, Reason: reason, Message: msg})
}

func (h *Handler) record(r *http.Request, u User, e audit.Event) {
	e.User, e.Groups = u.Name, u.Groups
	e.SourceIP = h.opts.ClientIP.IP(r)
	e.UserAgent = r.UserAgent()
	audit.Log(r.Context(), e)
}

// safeNext allows only local paths, so the login can't be used as an open redirect.
func safeNext(next string) string {
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.ContainsAny(next, "\\\r\n\t") {
		return "/"
	}
	if u, err := url.Parse(next); err != nil || u.Host != "" || u.Scheme != "" {
		return "/"
	}
	return next
}

func randomString() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
