package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testSessions(t *testing.T) *Sessions {
	t.Helper()
	s, err := NewSessions([]byte(strings.Repeat("k", 32)), time.Hour, true)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// roundTrip sets a session and returns a request carrying the resulting cookie.
func roundTrip(t *testing.T, s *Sessions, u User) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.Set(rec, u); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestSessionRoundTrip(t *testing.T) {
	s := testSessions(t)
	rec := httptest.NewRecorder()
	if err := s.Set(rec, User{Name: "alice", Groups: []string{"ops"}}); err != nil {
		t.Fatal(err)
	}
	c := rec.Result().Cookies()[0]
	if c.Name != sessionCookie || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" {
		t.Errorf("cookie attributes: %+v", c)
	}
	if strings.Contains(c.Value, "alice") {
		t.Error("cookie is not encrypted")
	}
	u, ok := s.Get(roundTrip(t, s, User{Name: "alice", Groups: []string{"ops"}}))
	if !ok || u.Name != "alice" || len(u.Groups) != 1 || u.Groups[0] != "ops" {
		t.Errorf("got %+v %v", u, ok)
	}
}

func TestSessionRejectsTamperingExpiryAndOtherKeys(t *testing.T) {
	s := testSessions(t)
	req := roundTrip(t, s, User{Name: "alice"})
	c, _ := req.Cookie(sessionCookie)

	tampered := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	b := []byte(c.Value)
	b[len(b)/2] ^= 1
	tampered.AddCookie(&http.Cookie{Name: sessionCookie, Value: string(b)}) //nolint:gosec // test cookie
	if _, ok := s.Get(tampered); ok {
		t.Error("tampered cookie accepted")
	}

	other, _ := NewSessions([]byte(strings.Repeat("x", 32)), time.Hour, true)
	if _, ok := other.Get(req); ok {
		t.Error("cookie accepted with another key")
	}

	// A login-state cookie must not work as a session cookie.
	swapped := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	value, _ := s.seal(loginCookie, sessionData{User: User{Name: "mallory"}, Exp: time.Now().Add(time.Hour).Unix()})
	swapped.AddCookie(&http.Cookie{Name: sessionCookie, Value: value}) //nolint:gosec // test cookie
	if _, ok := s.Get(swapped); ok {
		t.Error("cookie sealed for another purpose accepted")
	}

	s.now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	if _, ok := s.Get(req); ok {
		t.Error("expired session accepted")
	}
}

func TestSessionTooLarge(t *testing.T) {
	s := testSessions(t)
	var groups []string
	for range 300 {
		groups = append(groups, strings.Repeat("g", 20))
	}
	if err := s.Set(httptest.NewRecorder(), User{Name: "alice", Groups: groups}); !errors.Is(err, errCookieTooLarge) {
		t.Errorf("err = %v, want errCookieTooLarge", err)
	}
}

func TestNewSessionsKeyLength(t *testing.T) {
	if _, err := NewSessions([]byte("short"), time.Hour, true); err == nil {
		t.Error("short key accepted")
	}
}
