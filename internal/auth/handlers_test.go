package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/clientip"
)

type fakeRedirect struct{ fail bool }

func (fakeRedirect) AuthURL(state, nonce, verifier string) string {
	return "https://idp.example/authorize?" + url.Values{"state": {state}, "nonce": {nonce}, "v": {verifier}}.Encode()
}

func (f fakeRedirect) Exchange(_ context.Context, code, verifier, nonce string) (User, error) {
	if f.fail || code != "good" || verifier == "" || nonce == "" {
		return User{}, errors.New("exchange failed")
	}
	return User{Name: "alice", Groups: []string{"ops"}}, nil
}

func (fakeRedirect) LogoutURL() string { return "https://idp.example/logout" }

type fakePassword struct{}

func (fakePassword) Authenticate(_ context.Context, user, pw string) (User, error) {
	if user == "alice" && pw == "secret" {
		return User{Name: "alice"}, nil
	}
	if user == "down" {
		return User{}, errors.New("connection refused")
	}
	return User{}, ErrInvalidCredentials
}

type authEnv struct {
	t   *testing.T
	mux *http.ServeMux
	h   *Handler
}

func newAuthEnv(t *testing.T, opts Options) *authEnv {
	t.Helper()
	if opts.Sessions == nil && opts.Type != "none" {
		opts.Sessions = testSessions(t)
	}
	opts.IsAdmin = func(u User) bool { return u.Name == "root" || opts.Type == "none" }
	h := New(opts)
	mux := http.NewServeMux()
	h.Register(mux)
	return &authEnv{t: t, mux: mux, h: h}
}

func (e *authEnv) do(method, target string, body string, cookies []*http.Cookie, header ...string) *result {
	e.t.Helper()
	req := httptest.NewRequestWithContext(e.t.Context(), method, "http://rw.example"+target, strings.NewReader(body))
	for _, c := range cookies {
		req.AddCookie(c)
	}
	for i := 0; i+1 < len(header); i += 2 {
		if header[i] == "RemoteAddr" {
			req.RemoteAddr = header[i+1]
			continue
		}
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	return &result{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies(), Body: data}
}

// result is a fully read response.
type result struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	cookies    []*http.Cookie
}

func cookie(resp *result, name string) *http.Cookie {
	for _, c := range resp.cookies {
		if c.Name == name {
			return c
		}
	}
	return nil
}

// recorded reads a recorder's response.
func recorded(rec *httptest.ResponseRecorder) *result {
	resp := rec.Result()
	defer func() { _ = resp.Body.Close() }()
	return &result{StatusCode: resp.StatusCode, Header: resp.Header, cookies: resp.Cookies()}
}

func TestRedirectLoginFlow(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "oidc", Redirect: fakeRedirect{}})

	resp := e.do(http.MethodGet, "/auth/login?next=/runs/42", "", nil)
	login := cookie(resp, loginCookie)
	if resp.StatusCode != http.StatusFound || login == nil || login.Path != "/auth/" {
		t.Fatalf("login: %d, cookie %+v", resp.StatusCode, login)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")

	resp = e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login})
	session := cookie(resp, sessionCookie)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/runs/42" || session == nil {
		t.Fatalf("callback: %d → %q, session %v", resp.StatusCode, resp.Header.Get("Location"), session)
	}

	resp = e.do(http.MethodGet, "/api/me", "", []*http.Cookie{session})
	var me meResponse
	_ = json.Unmarshal(resp.Body, &me)
	if resp.StatusCode != http.StatusOK || me.Name != "alice" || me.Admin || me.AuthType != "oidc" || len(me.Groups) != 1 {
		t.Errorf("me: %d %+v", resp.StatusCode, me)
	}

	// The login cookie is single use.
	if resp := e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login}); cookie(resp, loginCookie).MaxAge >= 0 {
		t.Error("login cookie not cleared after use")
	}
}

func TestRedirectLoginFailures(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "oidc", Redirect: fakeRedirect{}})
	start := func() (*http.Cookie, string) {
		resp := e.do(http.MethodGet, "/auth/login", "", nil)
		loc, _ := url.Parse(resp.Header.Get("Location"))
		return cookie(resp, loginCookie), loc.Query().Get("state")
	}

	if resp := e.do(http.MethodGet, "/auth/callback?code=good&state=x", "", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("without login cookie = %d", resp.StatusCode)
	}
	login, _ := start()
	if resp := e.do(http.MethodGet, "/auth/callback?code=good&state=forged", "", []*http.Cookie{login}); resp.StatusCode != http.StatusBadRequest || cookie(resp, sessionCookie) != nil {
		t.Errorf("wrong state = %d", resp.StatusCode)
	}
	login, state := start()
	if resp := e.do(http.MethodGet, "/auth/callback?error=access_denied&state="+state, "", []*http.Cookie{login}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("provider error = %d", resp.StatusCode)
	}
	login, state = start()
	if resp := e.do(http.MethodGet, "/auth/callback?code=bad&state="+state, "", []*http.Cookie{login}); resp.StatusCode != http.StatusUnauthorized || cookie(resp, sessionCookie) != nil {
		t.Errorf("failed exchange = %d", resp.StatusCode)
	}
}

func TestSafeNext(t *testing.T) {
	for in, want := range map[string]string{
		"/runs/1?x=y":          "/runs/1?x=y",
		"":                     "/",
		"//evil.example":       "/",
		"/\\evil.example":      "/",
		"https://evil.example": "/",
		"runs":                 "/",
		"/ok\r\nSet-Cookie:x":  "/",
	} {
		if got := safeNext(in); got != want {
			t.Errorf("safeNext(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPasswordLogin(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "ldap", Password: fakePassword{}})
	post := func(user, pw string, extra ...string) *result {
		body, _ := json.Marshal(credentials{ //nolint:gosec // test credentials
			Username: user, Password: pw})
		return e.do(http.MethodPost, "/auth/login", string(body), nil, extra...)
	}

	resp := post("alice", "secret")
	if resp.StatusCode != http.StatusOK || cookie(resp, sessionCookie) == nil {
		t.Fatalf("good login = %d", resp.StatusCode)
	}
	if resp := post("alice", "wrong"); resp.StatusCode != http.StatusUnauthorized || cookie(resp, sessionCookie) != nil {
		t.Errorf("bad password = %d", resp.StatusCode)
	}
	if resp := post("down", "x"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("directory down = %d", resp.StatusCode)
	}
	if resp := e.do(http.MethodGet, "/auth/login", "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET login with password auth = %d", resp.StatusCode)
	}
}

func TestPasswordLoginRateLimit(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "ldap", Password: fakePassword{}})
	post := func(pw, addr string) int {
		body, _ := json.Marshal(credentials{ //nolint:gosec // test credentials
			Username: "Alice", Password: pw})
		return e.do(http.MethodPost, "/auth/login", string(body), nil, "RemoteAddr", addr).StatusCode
	}
	// Usernames are case-insensitive: "Alice" fails as "alice" would.
	for range 5 {
		if code := post("wrong", "192.0.2.1:1000"); code != http.StatusUnauthorized {
			t.Fatalf("failure = %d", code)
		}
	}
	if code := post("secret", "192.0.2.1:1001"); code != http.StatusTooManyRequests {
		t.Errorf("after 5 failures = %d, want 429", code)
	}
	if code := post("wrong", "198.51.100.7:1000"); code != http.StatusUnauthorized {
		t.Errorf("another client = %d, want 401 (not locked)", code)
	}
}

func TestMiddlewareAndOrigin(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "oidc", Redirect: fakeRedirect{}, AllowedOrigins: []string{"*.trusted.example"}})
	rec := httptest.NewRecorder()
	if err := e.h.opts.Sessions.Set(rec, User{Name: "alice"}); err != nil {
		t.Fatal(err)
	}
	session := cookie(recorded(rec), sessionCookie)

	if resp := e.do(http.MethodGet, "/api/me", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session = %d", resp.StatusCode)
	}
	garbage := &http.Cookie{Name: sessionCookie, Value: "garbage"} //nolint:gosec // test cookie
	if resp := e.do(http.MethodGet, "/api/me", "", []*http.Cookie{garbage}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage session = %d", resp.StatusCode)
	}

	logout := func(header ...string) int {
		return e.do(http.MethodPost, "/auth/logout", "", []*http.Cookie{session}, header...).StatusCode
	}
	for _, h := range [][]string{
		{"Origin", "https://evil.example"},
		{"Sec-Fetch-Site", "cross-site"},
		{"Origin", "null"},
	} {
		if code := logout(h...); code != http.StatusForbidden {
			t.Errorf("logout with %v = %d, want 403", h, code)
		}
	}
	for _, h := range [][]string{
		{},
		{"Origin", "http://rw.example"},
		{"Origin", "https://ops.trusted.example"},
		{"Sec-Fetch-Site", "same-origin"},
	} {
		if code := logout(h...); code != http.StatusOK {
			t.Errorf("logout with %v = %d, want 200", h, code)
		}
	}

	resp := e.do(http.MethodPost, "/auth/logout", "", []*http.Cookie{session})
	body := resp.Body
	if c := cookie(resp, sessionCookie); c == nil || c.MaxAge >= 0 || !strings.Contains(string(body), "idp.example/logout") {
		t.Errorf("logout response: cookie %+v body %s", c, body)
	}
}

func TestLoginAudit(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	proxies, _ := clientip.New([]string{"192.0.2.0/24"})
	e := newAuthEnv(t, Options{Type: "ldap", Password: fakePassword{}, ClientIP: proxies})
	login := func(user, pw string) {
		body, _ := json.Marshal(credentials{Username: user, Password: pw}) //nolint:gosec // test credentials
		e.do(http.MethodPost, "/auth/login", string(body), nil, "RemoteAddr", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.23")
	}
	login("alice", "wrong-pass")
	login("alice", "secret")

	logs := buf.String()
	for _, want := range []string{
		`"action":"login","outcome":"failure","dataset":"runwarden.audit","reason":"invalid user name or password"`,
		`"action":"login","outcome":"success"`,
		`"user":{"name":"alice"}`,
		`"source":{"ip":"198.51.100.23"}`,
	} {
		if !strings.Contains(logs, want) {
			t.Errorf("audit log missing %s:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "wrong-pass") || strings.Contains(logs, `"secret"`) {
		t.Error("password in the logs")
	}
}

func TestAuthConfigIsPublic(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "ldap", Password: fakePassword{}})
	resp := e.do(http.MethodGet, "/auth/config", "", nil)
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(resp.Body)) != `{"type":"ldap"}` {
		t.Errorf("config = %d %s", resp.StatusCode, resp.Body)
	}
}

func TestAuthNone(t *testing.T) {
	e := newAuthEnv(t, Options{Type: "none"})
	resp := e.do(http.MethodGet, "/api/me", "", nil)
	var me meResponse
	_ = json.Unmarshal(resp.Body, &me)
	if resp.StatusCode != http.StatusOK || me.Name != "anonymous" || !me.Admin {
		t.Errorf("me = %d %+v", resp.StatusCode, me)
	}
}

func TestGroupsTrimmedWhenTooMany(t *testing.T) {
	var groups []string
	for i := range 300 {
		groups = append(groups, strings.Repeat("g", 18)+string(rune('a'+i%26))+strings.Repeat("x", i%7))
	}
	groups = append(groups, "ops")
	e := newAuthEnv(t, Options{Type: "ldap", RelevantGroups: func() []string { return []string{"OPS"} }})
	rec := httptest.NewRecorder()
	if err := e.h.startSession(rec, User{Name: "alice", Groups: groups}); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	req.AddCookie(cookie(recorded(rec), sessionCookie))
	u, ok := e.h.opts.Sessions.Get(req)
	if !ok || len(u.Groups) != 1 || u.Groups[0] != "ops" {
		t.Errorf("user = %+v", u)
	}
}

func TestLimiter(t *testing.T) {
	now := time.Now()
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	l.fail("k")
	l.fail("k")
	if l.allowed("k") {
		t.Error("allowed after max failures")
	}
	now = now.Add(2 * time.Minute)
	if !l.allowed("k") {
		t.Error("still blocked after the window")
	}
	l.fail("k")
	l.reset("k")
	if !l.allowed("k") || len(l.failures) != 0 {
		t.Error("reset did not clear")
	}
}
