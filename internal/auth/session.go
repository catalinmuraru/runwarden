package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"
)

const (
	sessionCookie = "runwarden_session"
	loginCookie   = "runwarden_login"
	loginTTL      = 10 * time.Minute
)

var errBadCookie = errors.New("invalid or expired cookie")

// Sessions handles the cookies: the session cookie holds only the token of a server-side
// session (see Store); the short-lived login-state cookie is sealed with AES-256-GCM, so it can
// be neither read nor forged without the key. The cookie name is bound as additional data, so
// one cookie can't stand in for another.
type Sessions struct {
	aead   cipher.AEAD
	ttl    time.Duration
	secure bool
	now    func() time.Time
}

// NewSessions returns sessions sealed with a 32-byte key.
func NewSessions(key []byte, ttl time.Duration, secure bool) (*Sessions, error) {
	if len(key) != 32 {
		return nil, errors.New("session key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sessions{aead: aead, ttl: ttl, secure: secure, now: time.Now}, nil
}

// loginState travels through the provider round trip in its own short-lived cookie.
type loginState struct {
	State    string `json:"state"`
	Nonce    string `json:"nonce"`
	Verifier string `json:"verifier"`
	Next     string `json:"next"`
	Method   string `json:"method"` // the sign-in method that started the round trip
	Exp      int64  `json:"exp"`
}

func (s *Sessions) seal(name string, v any) (string, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(s.aead.Seal(nonce, nonce, plain, []byte(name))), nil
}

func (s *Sessions) open(name, value string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(data) < s.aead.NonceSize() {
		return errBadCookie
	}
	n := s.aead.NonceSize()
	plain, err := s.aead.Open(nil, data[:n], data[n:], []byte(name))
	if err != nil {
		return errBadCookie
	}
	return json.Unmarshal(plain, v)
}

func (s *Sessions) cookie(name, value, path string, maxAge time.Duration) *http.Cookie {
	// Secure is configurable only so plain-http local development works; it defaults to true.
	return &http.Cookie{ //nolint:gosec // HttpOnly and SameSite always set
		Name:     name,
		Value:    value,
		Path:     path,
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   s.secure,
		SameSite: http.SameSiteLaxMode,
	}
}

// setToken sets the session cookie.
func (s *Sessions) setToken(w http.ResponseWriter, token string) {
	http.SetCookie(w, s.cookie(sessionCookie, token, "/", s.ttl))
}

// token returns the session token sent by the browser, if any.
func token(r *http.Request) string {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return ""
	}
	return c.Value
}

// Clear removes the session cookie.
func (s *Sessions) Clear(w http.ResponseWriter) {
	http.SetCookie(w, s.cookie(sessionCookie, "", "/", -time.Second))
}

func (s *Sessions) setLogin(w http.ResponseWriter, st loginState) error {
	st.Exp = s.now().Add(loginTTL).Unix()
	value, err := s.seal(loginCookie, st)
	if err != nil {
		return err
	}
	http.SetCookie(w, s.cookie(loginCookie, value, "/auth/", loginTTL))
	return nil
}

// takeLogin reads and clears the login state; it can be used once.
func (s *Sessions) takeLogin(w http.ResponseWriter, r *http.Request) (loginState, bool) {
	http.SetCookie(w, s.cookie(loginCookie, "", "/auth/", -time.Second))
	c, err := r.Cookie(loginCookie)
	if err != nil {
		return loginState{}, false
	}
	var st loginState
	if err := s.open(loginCookie, c.Value, &st); err != nil || s.now().Unix() >= st.Exp {
		return loginState{}, false
	}
	return st, true
}
