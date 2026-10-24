package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// fakeIDP is a minimal OpenID provider: discovery, JWKS, token and userinfo endpoints, with
// RS256-signed ID tokens.
type fakeIDP struct {
	*httptest.Server
	t        *testing.T
	key      *rsa.PrivateKey
	clientID string

	mu    sync.Mutex
	codes map[string]grant

	// Knobs for a test case.
	idClaims map[string]any // merged into the ID token
	userinfo map[string]any
	aud      string
	expIn    time.Duration
}

type grant struct{ challenge, nonce string }

func newFakeIDP(t *testing.T) *fakeIDP {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeIDP{t: t, key: key, clientID: "runwarden", codes: map[string]grant{}, expIn: time.Hour}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"issuer":                                f.URL,
			"authorization_endpoint":                f.URL + "/authorize",
			"token_endpoint":                        f.URL + "/token",
			"userinfo_endpoint":                     f.URL + "/userinfo",
			"jwks_uri":                              f.URL + "/jwks",
			"end_session_endpoint":                  f.URL + "/logout",
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, _ *http.Request) {
		b64 := base64.RawURLEncoding.EncodeToString
		writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
			"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256",
			"n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	mux.HandleFunc("POST /token", f.token)
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer at-") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		writeJSON(w, http.StatusOK, f.userinfo)
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// login simulates the user signing in at the provider: it returns the code the provider would
// send back to the callback.
func (f *fakeIDP) login(authURL string) string {
	u, err := url.Parse(authURL)
	if err != nil {
		f.t.Fatal(err)
	}
	q := u.Query()
	if q.Get("code_challenge_method") != "S256" || q.Get("client_id") != f.clientID {
		f.t.Fatalf("authorization request without PKCE or wrong client: %s", authURL)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	code := fmt.Sprintf("code-%d", len(f.codes))
	f.codes[code] = grant{challenge: q.Get("code_challenge"), nonce: q.Get("nonce")}
	return code
}

func (f *fakeIDP) token(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	g, ok := f.codes[r.PostForm.Get("code")]
	delete(f.codes, r.PostForm.Get("code"))
	f.mu.Unlock()
	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != g.challenge {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
		return
	}
	aud := f.aud
	if aud == "" {
		aud = f.clientID
	}
	claims := map[string]any{
		"iss": f.URL, "sub": "user-1", "aud": aud, "nonce": g.nonce,
		"iat": time.Now().Unix(), "exp": time.Now().Add(f.expIn).Unix(),
	}
	for k, v := range f.idClaims {
		claims[k] = v
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": "at-" + r.PostForm.Get("code"), "token_type": "Bearer", "expires_in": 3600,
		"id_token": f.sign(claims),
	})
}

func (f *fakeIDP) sign(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	input := enc(map[string]string{"alg": "RS256", "kid": "k1", "typ": "JWT"}) + "." + enc(claims)
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (f *fakeIDP) provider(t *testing.T, cfg config.OIDC) *OIDCProvider {
	t.Helper()
	cfg.IssuerURL, cfg.ClientID, cfg.ClientSecret = f.URL, f.clientID, "secret"
	if cfg.Scopes == nil {
		cfg.Scopes = []string{"openid"}
	}
	p, err := NewOIDC(t.Context(), cfg, "https://rw.example/auth/callback", "https://rw.example")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// signIn runs the code flow and returns the result of Exchange.
func signIn(t *testing.T, f *fakeIDP, p *OIDCProvider, exchangeNonce string) (User, error) {
	t.Helper()
	verifier := oauth2.GenerateVerifier()
	code := f.login(p.AuthURL("state-1", "nonce-1", verifier))
	if exchangeNonce == "" {
		exchangeNonce = "nonce-1"
	}
	return p.Exchange(t.Context(), code, verifier, exchangeNonce)
}

var oidcDefaults = config.OIDC{UsernameClaim: "preferred_username", GroupsClaims: []string{"groups"}}

func TestOIDCSignIn(t *testing.T) {
	f := newFakeIDP(t)
	f.idClaims = map[string]any{"preferred_username": "alice", "groups": []any{"ops", "dev"}}
	p := f.provider(t, oidcDefaults)

	u, err := signIn(t, f, p, "")
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "alice" || !slices.Equal(u.Groups, []string{"ops", "dev"}) {
		t.Errorf("user = %+v", u)
	}
	if lu := p.LogoutURL(); !strings.HasPrefix(lu, f.URL+"/logout?") || !strings.Contains(lu, "client_id=runwarden") {
		t.Errorf("logout url = %q", lu)
	}
}

func TestOIDCClaimsFromUserinfo(t *testing.T) {
	f := newFakeIDP(t)
	f.idClaims = map[string]any{"preferred_username": "alice"}
	f.userinfo = map[string]any{"sub": "user-1", "groups": []any{"ops"}}
	u, err := signIn(t, f, f.provider(t, oidcDefaults), "")
	if err != nil || !slices.Equal(u.Groups, []string{"ops"}) {
		t.Errorf("user = %+v, err = %v", u, err)
	}
}

func TestGitLabClaims(t *testing.T) {
	f := newFakeIDP(t)
	f.idClaims = map[string]any{"nickname": "alice", "groups_direct": []any{"platform"}}
	f.userinfo = map[string]any{"sub": "user-1", "groups": []any{"platform", "platform/ops"}}
	p := f.provider(t, config.OIDC{UsernameClaim: "nickname", GroupsClaims: []string{"groups", "groups_direct"}})
	u, err := signIn(t, f, p, "")
	if err != nil || u.Name != "alice" || !slices.Equal(u.Groups, []string{"platform", "platform/ops"}) {
		t.Errorf("user = %+v, err = %v", u, err)
	}
}

func TestOIDCRejects(t *testing.T) {
	tests := []struct {
		name  string
		setup func(f *fakeIDP)
		nonce string
	}{
		{"nonce mismatch", nil, "other-nonce"},
		{"wrong audience", func(f *fakeIDP) { f.aud = "someone-else" }, ""},
		{"expired token", func(f *fakeIDP) { f.expIn = -time.Minute }, ""},
		{"missing user name", func(f *fakeIDP) { f.idClaims = map[string]any{"groups": []any{"ops"}} }, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFakeIDP(t)
			f.idClaims = map[string]any{"preferred_username": "alice"}
			if tt.setup != nil {
				tt.setup(f)
			}
			if u, err := signIn(t, f, f.provider(t, oidcDefaults), tt.nonce); err == nil {
				t.Errorf("signed in as %+v", u)
			}
		})
	}

	t.Run("wrong PKCE verifier", func(t *testing.T) {
		f := newFakeIDP(t)
		p := f.provider(t, oidcDefaults)
		code := f.login(p.AuthURL("s", "n", oauth2.GenerateVerifier()))
		if _, err := p.Exchange(t.Context(), code, oauth2.GenerateVerifier(), "n"); err == nil {
			t.Error("exchange succeeded with another verifier")
		}
	})
}
