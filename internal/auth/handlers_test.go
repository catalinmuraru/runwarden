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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/clientip"
)

type fakeRedirect struct {
	fail   bool
	user   string
	groups []string
}

func (fakeRedirect) AuthURL(state, nonce, verifier string) string {
	return "https://idp.example/authorize?" + url.Values{"state": {state}, "nonce": {nonce}, "v": {verifier}}.Encode()
}

func (f fakeRedirect) Exchange(_ context.Context, code, verifier, nonce string) (User, error) {
	if f.fail || code != "good" || verifier == "" || nonce == "" {
		return User{}, errors.New("exchange failed")
	}
	name, groups := f.user, f.groups
	if name == "" {
		name, groups = "alice", []string{"ops"}
	}
	return User{Name: name, Groups: groups}, nil
}

func (fakeRedirect) LogoutURL() string { return "https://idp.example/logout" }

type fakePassword struct{}

func (fakePassword) Authenticate(_ context.Context, user, pw string) (User, error) {
	switch {
	case user == "alice" && pw == "secret":
		return User{Name: "alice"}, nil
	case user == "admin@local" && pw == "impostor":
		return User{Name: "admin@local"}, nil
	case user == "down":
		return User{}, errors.New("connection refused")
	}
	return User{}, ErrInvalidCredentials
}

var (
	oidcMethod = Method{ID: "oidc", Label: "Single sign-on", Redirect: fakeRedirect{}}
	ldapMethod = Method{ID: "ldap", Label: "Directory", Password: fakePassword{}}
)

type authEnv struct {
	t   *testing.T
	mux *http.ServeMux
	h   *Handler
}

func newAuthEnv(t *testing.T, opts Options) *authEnv {
	t.Helper()
	if !opts.NoAuth {
		if opts.Cookies == nil {
			opts.Cookies = testSessions(t)
		}
		if opts.Store == nil {
			store, err := OpenStore(t.TempDir(), time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			opts.Store = store
		}
	}
	noAuth := opts.NoAuth
	opts.IsAdmin = func(u User) bool { return u.Name == "root" || noAuth || u.Method == MethodLocal }
	h := New(opts)
	mux := http.NewServeMux()
	h.Register(mux)
	return &authEnv{t: t, mux: mux, h: h}
}

func (e *authEnv) do(method, target string, body string, cookies []*http.Cookie, header ...string) *result {
	e.t.Helper()
	req := httptest.NewRequestWithContext(e.t.Context(), method, "http://rw.example"+target, strings.NewReader(body))
	for _, c := range cookies {
		if c != nil {
			req.AddCookie(c)
		}
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

// post signs in with a password method.
func (e *authEnv) post(method, user, pw string, header ...string) *result {
	e.t.Helper()
	body, _ := json.Marshal(credentials{Method: method, Username: user, Password: pw}) //nolint:gosec // test credentials
	return e.do(http.MethodPost, "/auth/login", string(body), nil, header...)
}

func (e *authEnv) me(c *http.Cookie) (int, meResponse) {
	e.t.Helper()
	resp := e.do(http.MethodGet, "/api/me", "", []*http.Cookie{c})
	var me meResponse
	_ = json.Unmarshal(resp.Body, &me)
	return resp.StatusCode, me
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

// startRedirect begins a redirect sign-in and returns the login cookie and the state.
func (e *authEnv) startRedirect(method string) (*http.Cookie, string) {
	e.t.Helper()
	resp := e.do(http.MethodGet, "/auth/login?method="+method+"&next=/runs/42", "", nil)
	loc, _ := url.Parse(resp.Header.Get("Location"))
	return cookie(resp, loginCookie), loc.Query().Get("state")
}

func TestRedirectLoginFlow(t *testing.T) {
	gitlab := Method{ID: "gitlab", Label: "GitLab", Redirect: fakeRedirect{user: "gl-user"}}
	e := newAuthEnv(t, Options{Methods: []Method{gitlab, oidcMethod}})

	login, state := e.startRedirect("oidc")
	if login == nil || login.Path != "/auth/" {
		t.Fatalf("login cookie %+v", login)
	}
	resp := e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login})
	session := cookie(resp, sessionCookie)
	if resp.StatusCode != http.StatusFound || resp.Header.Get("Location") != "/runs/42" || session == nil {
		t.Fatalf("callback: %d → %q, session %v", resp.StatusCode, resp.Header.Get("Location"), session)
	}
	if code, me := e.me(session); code != http.StatusOK || me.Name != "alice" || me.Admin || me.AuthType != "oidc" || len(me.Groups) != 1 {
		t.Errorf("me: %d %+v", code, me)
	}
	if e.h.SignedIn(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)) {
		t.Error("request without cookie counted as signed in")
	}

	// The callback uses the method that started the sign-in.
	login, state = e.startRedirect("gitlab")
	resp = e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login})
	if code, me := e.me(cookie(resp, sessionCookie)); code != http.StatusOK || me.Name != "gl-user" || me.AuthType != "gitlab" {
		t.Errorf("gitlab me: %d %+v", code, me)
	}

	// The login cookie is single use.
	if resp := e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login}); cookie(resp, loginCookie).MaxAge >= 0 {
		t.Error("login cookie not cleared after use")
	}
}

func TestRedirectLoginFailures(t *testing.T) {
	e := newAuthEnv(t, Options{Methods: []Method{oidcMethod, ldapMethod}})
	errorOf := func(resp *result) string {
		loc, _ := url.Parse(resp.Header.Get("Location"))
		if resp.StatusCode != http.StatusFound || loc.Path != "/" || cookie(resp, sessionCookie) != nil {
			t.Errorf("failure response: %d %q", resp.StatusCode, resp.Header.Get("Location"))
		}
		return loc.Query().Get("sign_in_error")
	}
	for name, tc := range map[string]struct {
		target string
		login  bool
		want   string
	}{
		"unknown method":      {"/auth/login?method=kerberos", false, errUnknown},
		"password method":     {"/auth/login?method=ldap", false, errUnknown},
		"without login state": {"/auth/callback?code=good&state=x", false, errExpired},
		"forged state":        {"/auth/callback?code=good&state=forged", true, errExpired},
		"provider error":      {"/auth/callback?error=access_denied&state=STATE", true, errRefused},
		"failed exchange":     {"/auth/callback?code=bad&state=STATE", true, errFailed},
	} {
		var cookies []*http.Cookie
		target := tc.target
		if tc.login {
			login, state := e.startRedirect("oidc")
			cookies = []*http.Cookie{login}
			target = strings.Replace(target, "STATE", url.QueryEscape(state), 1)
		}
		if got := errorOf(e.do(http.MethodGet, target, "", cookies)); got != tc.want {
			t.Errorf("%s: sign_in_error = %q, want %q", name, got, tc.want)
		}
	}
}

func TestRequiredGroup(t *testing.T) {
	member := Method{ID: "gitlab", Redirect: fakeRedirect{user: "dev", groups: []string{"Company/Runwarden/devs"}}, RequiredGroup: "company/runwarden"}
	outsider := Method{ID: "oidc", Redirect: fakeRedirect{user: "guest", groups: []string{"company/other", "company/runwarden-x"}}, RequiredGroup: "company/runwarden"}
	e := newAuthEnv(t, Options{Methods: []Method{member, outsider}})

	login, state := e.startRedirect("gitlab")
	if resp := e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login}); cookie(resp, sessionCookie) == nil {
		t.Error("member of a subgroup refused")
	}
	login, state = e.startRedirect("oidc")
	resp := e.do(http.MethodGet, "/auth/callback?code=good&state="+url.QueryEscape(state), "", []*http.Cookie{login})
	if cookie(resp, sessionCookie) != nil || !strings.Contains(resp.Header.Get("Location"), errNotMember) {
		t.Errorf("outsider: %d %q", resp.StatusCode, resp.Header.Get("Location"))
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
	e := newAuthEnv(t, Options{Methods: []Method{oidcMethod, ldapMethod}})
	resp := e.post("ldap", "alice", "secret")
	if resp.StatusCode != http.StatusOK || cookie(resp, sessionCookie) == nil {
		t.Fatalf("good login = %d", resp.StatusCode)
	}
	if code, me := e.me(cookie(resp, sessionCookie)); code != http.StatusOK || me.AuthType != "ldap" {
		t.Errorf("me = %d %+v", code, me)
	}
	if resp := e.post("ldap", "alice", "wrong"); resp.StatusCode != http.StatusUnauthorized || cookie(resp, sessionCookie) != nil {
		t.Errorf("bad password = %d", resp.StatusCode)
	}
	if resp := e.post("ldap", "down", "x"); resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("directory down = %d", resp.StatusCode)
	}
	for _, m := range []string{"oidc", "local", "", "kerberos"} {
		if resp := e.post(m, "alice", "secret"); resp.StatusCode != http.StatusNotFound {
			t.Errorf("POST login with method %q = %d, want 404", m, resp.StatusCode)
		}
	}
	// No other method may impersonate the built-in administrator.
	if resp := e.post("ldap", "admin@local", "impostor"); resp.StatusCode != http.StatusForbidden || cookie(resp, sessionCookie) != nil {
		t.Errorf("reserved name = %d", resp.StatusCode)
	}
}

func TestPasswordLoginRateLimit(t *testing.T) {
	e := newAuthEnv(t, Options{Methods: []Method{ldapMethod}})
	post := func(pw, addr string) int { return e.post("ldap", "Alice", pw, "RemoteAddr", addr).StatusCode }
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

func TestSessionsAndOrigin(t *testing.T) {
	e := newAuthEnv(t, Options{Methods: []Method{oidcMethod}, AllowedOrigins: []string{"*.trusted.example"}})
	newSession := func() *http.Cookie {
		tok, err := e.h.opts.Store.Create(User{Name: "alice", Method: "oidc"})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Cookie{Name: sessionCookie, Value: tok} //nolint:gosec // test cookie
	}

	if resp := e.do(http.MethodGet, "/api/me", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("no session = %d", resp.StatusCode)
	}
	garbage := &http.Cookie{Name: sessionCookie, Value: "garbage"} //nolint:gosec // test cookie
	if resp := e.do(http.MethodGet, "/api/me", "", []*http.Cookie{garbage}); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("garbage session = %d", resp.StatusCode)
	}

	logout := func(c *http.Cookie, header ...string) *result {
		return e.do(http.MethodPost, "/auth/logout", "", []*http.Cookie{c}, header...)
	}
	session := newSession()
	for _, h := range [][]string{
		{"Origin", "https://evil.example"},
		{"Sec-Fetch-Site", "cross-site"},
		{"Origin", "null"},
	} {
		if code := logout(session, h...).StatusCode; code != http.StatusForbidden {
			t.Errorf("logout with %v = %d, want 403", h, code)
		}
	}
	for _, h := range [][]string{
		{},
		{"Origin", "http://rw.example"},
		{"Origin", "https://ops.trusted.example"},
		{"Sec-Fetch-Site", "same-origin"},
	} {
		if code := logout(newSession(), h...).StatusCode; code != http.StatusOK {
			t.Errorf("logout with %v = %d, want 200", h, code)
		}
	}

	resp := logout(session)
	if c := cookie(resp, sessionCookie); c == nil || c.MaxAge >= 0 || !strings.Contains(string(resp.Body), "idp.example/logout") {
		t.Errorf("logout response: cookie %+v body %s", c, resp.Body)
	}
	// Signing out ends the session on the server: a copy of the cookie stops working.
	if code, _ := e.me(session); code != http.StatusUnauthorized {
		t.Errorf("session after sign-out = %d, want 401", code)
	}
}

func TestLocalAdmin(t *testing.T) {
	dir := t.TempDir()
	admin, path, err := EnableLocalAdmin(dir)
	if err != nil {
		t.Fatal(err)
	}
	store, _ := OpenStore(dir, time.Hour)
	local := Method{ID: MethodLocal, Label: "Administrator", Password: admin}
	e := newAuthEnv(t, Options{Methods: []Method{ldapMethod, local}, Store: store})

	data, _ := readFile(path)
	if resp := e.post(MethodLocal, "", "wrong"); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("wrong password = %d", resp.StatusCode)
	}
	resp := e.post(MethodLocal, "ignored", strings.TrimSpace(data))
	session := cookie(resp, sessionCookie)
	if code, me := e.me(session); code != http.StatusOK || me.Name != LocalAdminName || !me.Admin || me.AuthType != MethodLocal {
		t.Fatalf("admin me = %d %+v", code, me)
	}

	// With the built-in admin disabled, its sessions are refused even if they were kept.
	off := newAuthEnv(t, Options{Methods: []Method{ldapMethod}, Store: store})
	if code, _ := off.me(session); code != http.StatusUnauthorized {
		t.Errorf("admin session after disabling = %d, want 401", code)
	}
	if resp := off.post(MethodLocal, "", strings.TrimSpace(data)); resp.StatusCode != http.StatusNotFound {
		t.Errorf("local sign-in after disabling = %d, want 404", resp.StatusCode)
	}
}

func TestLoginAudit(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	proxies, _ := clientip.New([]string{"192.0.2.0/24"})
	e := newAuthEnv(t, Options{Methods: []Method{ldapMethod}, ClientIP: proxies})
	e.post("ldap", "alice", "wrong-pass", "RemoteAddr", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.23")
	e.post("ldap", "alice", "secret", "RemoteAddr", "192.0.2.10:4000", "X-Forwarded-For", "198.51.100.23")

	logs := buf.String()
	for _, want := range []string{
		`"action":"login","outcome":"failure","dataset":"runwarden.audit","reason":"invalid user name or password"`,
		`"action":"login","outcome":"success"`,
		`"user":{"name":"alice"}`,
		`"source":{"ip":"198.51.100.23"}`,
		`"auth":{"method":"ldap"}`,
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
	e := newAuthEnv(t, Options{Methods: []Method{{ID: "gitlab", Label: "GitLab", Redirect: fakeRedirect{}}, ldapMethod}})
	resp := e.do(http.MethodGet, "/auth/config", "", nil)
	want := `{"methods":[{"id":"gitlab","label":"GitLab","kind":"redirect"},{"id":"ldap","label":"Directory","kind":"password"}],"no_auth":false}`
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(string(resp.Body)) != want {
		t.Errorf("config = %d %s", resp.StatusCode, resp.Body)
	}
}

func TestAuthNone(t *testing.T) {
	e := newAuthEnv(t, Options{NoAuth: true})
	code, me := e.me(nil)
	if code != http.StatusOK || me.Name != "anonymous" || !me.Admin || me.AuthType != "none" {
		t.Errorf("me = %d %+v", code, me)
	}
	if !e.h.SignedIn(httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)) {
		t.Error("without sign-in everyone counts as signed in")
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

func TestSignInThrottling(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	e := newAuthEnv(t, Options{Methods: []Method{ldapMethod}, MaxFailuresPerIP: 3, MaxFailuresGlobal: 5})
	login := func(user, addr string) int {
		return e.post("ldap", user, "wrong", "RemoteAddr", addr+":4000").StatusCode
	}
	// Rotating user names from one address: the per-address limit stops it.
	for i, want := range []int{401, 401, 401, 429, 429, 429} {
		if got := login("user"+string(rune('a'+i)), "198.51.100.1"); got != want {
			t.Errorf("attempt %d from one address = %d, want %d", i+1, got, want)
		}
	}
	refusals := strings.Count(buf.String(), `"reason":"too many failed sign-ins; further refusals`)
	if refusals != 1 {
		t.Errorf("throttled refusals audited %d times, want once", refusals)
	}
	// Other addresses: two more failures reach the global limit, then everyone is refused.
	login("x", "198.51.100.2")
	login("y", "198.51.100.3")
	if got := login("z", "198.51.100.4"); got != http.StatusTooManyRequests {
		t.Errorf("after the global limit = %d, want 429", got)
	}
	// A correct password is refused too while throttled (no directory call).
	if got := e.post("ldap", "alice", "secret", "RemoteAddr", "198.51.100.9:4000").StatusCode; got != http.StatusTooManyRequests {
		t.Errorf("valid sign-in during the global limit = %d, want 429", got)
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path) //nolint:gosec // test temp dir
	return string(b), err
}
