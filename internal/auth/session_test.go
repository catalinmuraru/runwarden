package auth

import (
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

func TestSessionCookieAttributes(t *testing.T) {
	s := testSessions(t)
	rec := httptest.NewRecorder()
	s.setToken(rec, "tok")
	c := rec.Result().Cookies()[0]
	if c.Name != sessionCookie || c.Value != "tok" || !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 3600 {
		t.Errorf("cookie attributes: %+v", c)
	}
}

// loginRoundTrip sets a login state and returns a request carrying the resulting cookie.
func loginRoundTrip(t *testing.T, s *Sessions, st loginState) *http.Request {
	t.Helper()
	rec := httptest.NewRecorder()
	if err := s.setLogin(rec, st); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/auth/callback", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}

func TestLoginStateRejectsTamperingExpiryAndOtherKeys(t *testing.T) {
	s := testSessions(t)
	req := loginRoundTrip(t, s, loginState{State: "s", Method: "gitlab", Next: "/x"})
	if st, ok := s.takeLogin(httptest.NewRecorder(), req); !ok || st.Method != "gitlab" || st.Next != "/x" {
		t.Fatalf("round trip: %+v %v", st, ok)
	}
	c, _ := req.Cookie(loginCookie)
	if strings.Contains(c.Value, "gitlab") {
		t.Error("login state is not encrypted")
	}

	tampered := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	b := []byte(c.Value)
	b[len(b)/2] ^= 1
	tampered.AddCookie(&http.Cookie{Name: loginCookie, Value: string(b)}) //nolint:gosec // test cookie
	if _, ok := s.takeLogin(httptest.NewRecorder(), tampered); ok {
		t.Error("tampered cookie accepted")
	}

	other, _ := NewSessions([]byte(strings.Repeat("x", 32)), time.Hour, true)
	if _, ok := other.takeLogin(httptest.NewRecorder(), req); ok {
		t.Error("cookie accepted with another key")
	}

	// A value sealed for another cookie name must not work as login state.
	swapped := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	value, _ := s.seal(sessionCookie, loginState{State: "s", Exp: time.Now().Add(time.Hour).Unix()})
	swapped.AddCookie(&http.Cookie{Name: loginCookie, Value: value}) //nolint:gosec // test cookie
	if _, ok := s.takeLogin(httptest.NewRecorder(), swapped); ok {
		t.Error("cookie sealed for another purpose accepted")
	}

	s.now = func() time.Time { return time.Now().Add(time.Hour) }
	if _, ok := s.takeLogin(httptest.NewRecorder(), req); ok {
		t.Error("expired login state accepted")
	}
}

func TestNewSessionsKeyLength(t *testing.T) {
	if _, err := NewSessions([]byte("short"), time.Hour, true); err == nil {
		t.Error("short key accepted")
	}
}
