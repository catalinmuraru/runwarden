//go:build integration

// Integration tests against the identity providers in deploy/dev/compose.yaml.
// Run them with deploy/dev/test-auth.sh.
package auth

import (
	"encoding/json"
	"errors"
	"html"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
)

const appAddr = "127.0.0.1:18089" // registered as a redirect URI in the dev realm

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

// serve runs the auth handlers on appAddr for the duration of the test.
func serve(t *testing.T, h *Handler) {
	t.Helper()
	mux := http.NewServeMux()
	h.Register(mux)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", appAddr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
}

func devSessions(t *testing.T) *Sessions {
	s, err := NewSessions([]byte(strings.Repeat("d", 32)), time.Hour, false)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func me(t *testing.T, client *http.Client) (meResponse, int) {
	t.Helper()
	resp, err := send(t, client, http.MethodGet, "http://"+appAddr+"/api/me", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var m meResponse
	_ = json.NewDecoder(resp.Body).Decode(&m)
	return m, resp.StatusCode
}

var loginForm = regexp.MustCompile(`<form[^>]+action="([^"]+)"`)

// browserLogin signs in like a browser: Runwarden → Keycloak login form → callback.
func browserLogin(t *testing.T, user, password string) *http.Client {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 30 * time.Second}

	resp, err := send(t, client, http.MethodGet, "http://"+appAddr+"/auth/login", "", "")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := loginForm.FindSubmatch(page)
	if m == nil {
		t.Fatalf("no login form on %s:\n%.500s", resp.Request.URL, page)
	}
	form := url.Values{"username": {user}, "password": {password}}.Encode()
	resp, err = send(t, client, http.MethodPost, html.UnescapeString(string(m[1])), "application/x-www-form-urlencoded", form)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return client
}

func TestIntegrationOIDCKeycloak(t *testing.T) {
	p, err := NewOIDC(t.Context(), config.OIDC{ //nolint:gosec // client secret of the local dev realm
		IssuerURL:     envOr("RW_IT_OIDC_ISSUER", "http://localhost:8180/realms/runwarden"),
		ClientID:      "runwarden",
		ClientSecret:  "dev-client-secret",
		Scopes:        []string{"openid", "profile", "email"},
		UsernameClaim: "preferred_username",
		GroupsClaims:  []string{"groups"},
	}, "http://"+appAddr+"/auth/callback", "http://"+appAddr)
	if err != nil {
		t.Fatal(err)
	}
	serve(t, New(Options{Type: "oidc", Sessions: devSessions(t), Redirect: p}))

	for _, tc := range []struct {
		user, password string
		groups         []string
	}{
		{"alice", "alice-password", []string{"ops"}},
		{"bob", "bob-password", []string{}},
	} {
		t.Run(tc.user, func(t *testing.T) {
			m, code := me(t, browserLogin(t, tc.user, tc.password))
			if code != http.StatusOK || m.Name != tc.user || !slices.Equal(m.Groups, tc.groups) {
				t.Errorf("me = %d %+v", code, m)
			}
		})
	}
	t.Run("wrong password", func(t *testing.T) {
		if _, code := me(t, browserLogin(t, "alice", "wrong")); code != http.StatusUnauthorized {
			t.Errorf("me = %d, want 401", code)
		}
	})
}

func TestIntegrationLDAP(t *testing.T) {
	p := NewLDAP(config.LDAP{
		URL:           envOr("RW_IT_LDAP_URL", "ldap://127.0.0.1:1389"),
		BindDN:        "cn=admin,dc=example,dc=org",
		BindPassword:  "admin-password",
		UserBaseDN:    "ou=people,dc=example,dc=org",
		UserFilter:    "(uid={username})",
		GroupBaseDN:   "ou=groups,dc=example,dc=org",
		GroupFilter:   "(member={dn})",
		GroupNameAttr: "cn",
	})
	if u, err := p.Authenticate(t.Context(), "alice", "alice-password"); err != nil || !slices.Equal(u.Groups, []string{"ops"}) {
		t.Errorf("alice = %+v, %v", u, err)
	}
	if u, err := p.Authenticate(t.Context(), "bob", "bob-password"); err != nil || len(u.Groups) != 0 {
		t.Errorf("bob = %+v, %v", u, err)
	}
	for _, c := range [][2]string{{"alice", "wrong"}, {"nobody", "x"}, {"alice", ""}} {
		if _, err := p.Authenticate(t.Context(), c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%s/%q: err = %v", c[0], c[1], err)
		}
	}

	// Through the HTTP handler: POST /auth/login, then /api/me with the session cookie.
	serve(t, New(Options{Type: "ldap", Sessions: devSessions(t), Password: p}))
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 10 * time.Second}
	resp, err := send(t, client, http.MethodPost, "http://"+appAddr+"/auth/login", "application/json", `{"username":"alice","password":"alice-password"}`)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if m, code := me(t, client); code != http.StatusOK || m.Name != "alice" || !slices.Equal(m.Groups, []string{"ops"}) {
		t.Errorf("me = %d %+v", code, m)
	}
}

func send(t *testing.T, client *http.Client, method, target, contentType, body string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	return client.Do(req)
}
