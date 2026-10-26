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

// Method is one way to sign in, offered on the sign-in page.
type Method struct {
	ID       string // gitlab, oidc, ldap or local
	Label    string // shown on the sign-in page
	Redirect RedirectProvider
	Password PasswordProvider
	// RequiredGroup, if set, lets only members of this group (or its subgroups) sign in.
	RequiredGroup string
}

func (m *Method) kind() string {
	if m.Redirect != nil {
		return "redirect"
	}
	return "password"
}

// Options configure the auth handlers.
type Options struct {
	// NoAuth disables sign-in: every request is Anonymous (local development only).
	NoAuth         bool
	Methods        []Method
	Cookies        *Sessions // session and login-state cookies
	Store          *Store    // server-side sessions
	AllowedOrigins []string  // extra hosts trusted for state-changing requests
	ClientIP       clientip.Resolver
	IsAdmin        func(User) bool
	Roles          func(User) []string // the user's roles, shown in the UI
	// Failed sign-ins allowed per client address (15 minutes) and in total (1 minute), on top of
	// the per user+address limit. Zero means no limit.
	MaxFailuresPerIP  int
	MaxFailuresGlobal int
}

// Handler serves sign-in, callback, sign-out and /api/me, and authenticates API requests.
type Handler struct {
	opts   Options
	limit  *limiter // failures per user name + client address
	perIP  *limiter // failures per client address
	global *limiter // failures from everywhere
	noted  *limiter // refusals already audited, per client address
}

// New returns a Handler.
func New(opts Options) *Handler {
	if opts.Roles == nil {
		opts.Roles = func(User) []string { return nil }
	}
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

// method returns the enabled method with this ID, or nil.
func (h *Handler) method(id string) *Method {
	for i := range h.opts.Methods {
		if h.opts.Methods[i].ID == id {
			return &h.opts.Methods[i]
		}
	}
	return nil
}

// Register adds the auth routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /auth/login", h.loginRedirect)
	mux.Handle("POST /auth/login", h.sameOrigin(http.HandlerFunc(h.loginPassword)))
	mux.HandleFunc("GET /auth/callback", h.callback)
	mux.Handle("POST /auth/logout", h.sameOrigin(http.HandlerFunc(h.logout)))
	mux.Handle("GET /api/me", h.Middleware(http.HandlerFunc(h.me)))
	mux.HandleFunc("GET /auth/config", h.config)
}

type methodView struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // redirect or password
}

// config lists the enabled sign-in methods; it is all a signed-out visitor can learn.
func (h *Handler) config(w http.ResponseWriter, _ *http.Request) {
	methods := []methodView{}
	for i := range h.opts.Methods {
		m := &h.opts.Methods[i]
		methods = append(methods, methodView{ID: m.ID, Label: m.Label, Kind: m.kind()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"methods": methods, "no_auth": h.opts.NoAuth})
}

// user returns the user of the request's session, if it is valid and its method still enabled.
func (h *Handler) user(r *http.Request) (User, bool) {
	if h.opts.NoAuth {
		return Anonymous, true
	}
	u, ok := h.opts.Store.Get(token(r))
	if !ok || h.method(u.Method) == nil {
		return User{}, false
	}
	return u, true
}

// SignedIn reports whether the request carries a valid session (always true without sign-in).
func (h *Handler) SignedIn(r *http.Request) bool {
	_, ok := h.user(r)
	return ok
}

// Middleware requires a signed-in user and attaches it to the request context. It also rejects
// state-changing requests coming from other sites.
func (h *Handler) Middleware(next http.Handler) http.Handler {
	return h.sameOrigin(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := h.user(r)
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

// Sign-in errors are shown by the sign-in page, which reads them from ?sign_in_error=.
const (
	errExpired     = "expired"
	errRefused     = "refused"
	errFailed      = "failed"
	errThrottled   = "throttled"
	errNotMember   = "not-member"
	errUnavailable = "unavailable"
	errUnknown     = "unknown-method"
)

func signInError(w http.ResponseWriter, r *http.Request, code string) {
	http.Redirect(w, r, "/?sign_in_error="+code, http.StatusFound)
}

func (h *Handler) loginRedirect(w http.ResponseWriter, r *http.Request) {
	if h.opts.NoAuth {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	q := r.URL.Query()
	m := h.method(q.Get("method"))
	if m == nil || m.Redirect == nil {
		signInError(w, r, errUnknown)
		return
	}
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(),
		Next: safeNext(q.Get("next")), Method: m.ID}
	if err := h.opts.Cookies.setLogin(w, st); err != nil {
		slog.Error("auth: login state", "err", err)
		signInError(w, r, errUnavailable)
		return
	}
	http.Redirect(w, r, m.Redirect.AuthURL(st.State, st.Nonce, st.Verifier), http.StatusFound)
}

func (h *Handler) callback(w http.ResponseWriter, r *http.Request) {
	if h.opts.NoAuth {
		http.NotFound(w, r)
		return
	}
	if h.throttled(r, User{}, "") {
		signInError(w, r, errThrottled)
		return
	}
	st, ok := h.opts.Cookies.takeLogin(w, r)
	if !ok {
		signInError(w, r, errExpired)
		return
	}
	m := h.method(st.Method)
	if m == nil || m.Redirect == nil {
		signInError(w, r, errUnknown)
		return
	}
	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		slog.Warn("auth: provider refused sign-in", "method", m.ID, "error", e, "description", q.Get("error_description"))
		h.failed(r)
		h.auditLogin(r, User{}, m.ID, false, "refused by the identity provider: "+e)
		signInError(w, r, errRefused)
		return
	}
	if subtle.ConstantTimeCompare([]byte(q.Get("state")), []byte(st.State)) != 1 {
		h.failed(r)
		h.auditLogin(r, User{}, m.ID, false, "state mismatch")
		signInError(w, r, errExpired)
		return
	}
	u, err := m.Redirect.Exchange(r.Context(), q.Get("code"), st.Verifier, st.Nonce)
	if err != nil {
		slog.Warn("auth: sign-in failed", "method", m.ID, "err", err)
		h.failed(r)
		h.auditLogin(r, User{}, m.ID, false, "sign-in with the identity provider failed")
		signInError(w, r, errFailed)
		return
	}
	u.Method = m.ID
	if m.RequiredGroup != "" && !memberOf(u.Groups, m.RequiredGroup) {
		h.auditLogin(r, u, m.ID, false, "not a member of "+m.RequiredGroup)
		signInError(w, r, errNotMember)
		return
	}
	if err := h.startSession(w, u); err != nil {
		slog.Error("auth: session", "user", u.Name, "err", err)
		h.auditLogin(r, u, m.ID, false, err.Error())
		signInError(w, r, errFailed)
		return
	}
	h.auditLogin(r, u, m.ID, true, "")
	http.Redirect(w, r, st.Next, http.StatusFound)
}

type credentials struct {
	Method   string `json:"method"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (h *Handler) loginPassword(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&c); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": `send {"method": ..., "username": ..., "password": ...}`})
		return
	}
	m := h.method(c.Method)
	if m == nil || m.Password == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "this sign-in method is not enabled"})
		return
	}
	if m.ID == MethodLocal {
		c.Username = LocalAdminName
	}
	ip := h.opts.ClientIP.IP(r)
	key := m.ID + "|" + strings.ToLower(strings.TrimSpace(c.Username)) + "|" + ip
	if !h.limit.allowed(key) {
		if h.noted.first(key) {
			h.auditLogin(r, User{Name: c.Username}, m.ID, false, "too many failed sign-ins")
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins, try again later"})
		return
	}
	if h.throttled(r, User{Name: c.Username}, m.ID) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many failed sign-ins, try again later"})
		return
	}
	u, err := m.Password.Authenticate(r.Context(), c.Username, c.Password)
	switch {
	case errors.Is(err, ErrInvalidCredentials):
		h.limit.fail(key)
		h.failed(r)
		h.auditLogin(r, User{Name: c.Username}, m.ID, false, "invalid user name or password")
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": ErrInvalidCredentials.Error()})
		return
	case err != nil:
		slog.Error("auth: sign-in service unavailable", "method", m.ID, "err", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "sign-in service unavailable"})
		return
	}
	u.Method = m.ID
	h.limit.reset(key)
	if err := h.startSession(w, u); err != nil {
		slog.Error("auth: session", "user", u.Name, "err", err)
		h.auditLogin(r, u, m.ID, false, err.Error())
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "sign-in failed"})
		return
	}
	h.auditLogin(r, u, m.ID, true, "")
	writeJSON(w, http.StatusOK, h.meBody(u))
}

// errReservedName refuses accounts that would impersonate the built-in administrator.
var errReservedName = errors.New("the user name " + LocalAdminName + " is reserved for the built-in administrator")

// startSession creates a server-side session and sets its cookie.
func (h *Handler) startSession(w http.ResponseWriter, u User) error {
	if u.Method != MethodLocal && strings.EqualFold(u.Name, LocalAdminName) {
		return errReservedName
	}
	t, err := h.opts.Store.Create(u)
	if err != nil {
		return err
	}
	h.opts.Cookies.setToken(w, t)
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	body := map[string]string{}
	if !h.opts.NoAuth {
		if u, ok := h.opts.Store.Revoke(token(r)); ok {
			h.record(r, u, audit.Event{Action: audit.Logout, Category: "authentication", Type: "end", Success: true,
				Message: "signed out: " + u.Name})
			if m := h.method(u.Method); m != nil && m.Redirect != nil {
				if l := m.Redirect.LogoutURL(); l != "" {
					body["logout_url"] = l
				}
			}
		}
		h.opts.Cookies.Clear(w)
	}
	writeJSON(w, http.StatusOK, body)
}

type meResponse struct {
	Name     string   `json:"name"`
	Groups   []string `json:"groups"`
	Admin    bool     `json:"admin"`
	Roles    []string `json:"roles"`     // empty: guest
	AuthType string   `json:"auth_type"` // sign-in method, or "none"
}

func (h *Handler) meBody(u User) meResponse {
	groups := u.Groups
	if groups == nil {
		groups = []string{}
	}
	method := u.Method
	if h.opts.NoAuth {
		method = "none"
	}
	roles := h.opts.Roles(u)
	if roles == nil {
		roles = []string{}
	}
	return meResponse{Name: u.Name, Groups: groups, Admin: h.opts.IsAdmin(u), Roles: roles, AuthType: method}
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) {
	u, _ := UserFrom(r.Context())
	writeJSON(w, http.StatusOK, h.meBody(u))
}

// memberOf reports whether groups contain required or one of its subgroups (GitLab full paths).
func memberOf(groups []string, required string) bool {
	req := strings.ToLower(strings.Trim(required, "/"))
	for _, g := range groups {
		g = strings.ToLower(strings.Trim(g, "/"))
		if g == req || strings.HasPrefix(g, req+"/") {
			return true
		}
	}
	return false
}

// failed counts a failed sign-in against the per-address and global limits.
func (h *Handler) failed(r *http.Request) {
	h.perIP.fail(h.opts.ClientIP.IP(r))
	h.global.fail("")
}

// throttled reports whether sign-ins are refused for this address or for everyone. Only the
// first refusal per address and window is audited, so a flood cannot fill the logs.
func (h *Handler) throttled(r *http.Request, u User, method string) bool {
	ip := h.opts.ClientIP.IP(r)
	if h.perIP.allowed(ip) && h.global.allowed("") {
		return false
	}
	if h.noted.first(ip) {
		h.auditLogin(r, u, method, false, "too many failed sign-ins; further refusals from this address are not logged for 15 minutes")
	}
	return true
}

// auditLogin records a sign-in attempt. Passwords are never passed here.
func (h *Handler) auditLogin(r *http.Request, u User, method string, success bool, reason string) {
	msg := "signed in: " + u.Name
	if !success {
		msg = "sign-in failed: " + reason
	}
	u.Method = method
	h.record(r, u, audit.Event{Action: audit.Login, Category: "authentication", Type: "start",
		Success: success, Reason: reason, Message: msg})
}

func (h *Handler) record(r *http.Request, u User, e audit.Event) {
	e.User, e.Groups, e.AuthMethod = u.Name, u.Groups, u.Method
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
