// Package auth signs users in (GitLab, OIDC, LDAP or the built-in administrator), keeps them
// signed in with server-side sessions, and attaches the user to API requests.
package auth

import (
	"context"
	"errors"
)

// User is a signed-in user.
type User struct {
	Name   string   `json:"name"`
	Groups []string `json:"groups,omitempty"`
	Method string   `json:"method,omitempty"` // sign-in method: gitlab, oidc, ldap or local
}

// Anonymous is the user when authentication is disabled (AUTH_TYPE=none).
var Anonymous = User{Name: "anonymous"}

// ErrInvalidCredentials is returned for a wrong user name or password.
var ErrInvalidCredentials = errors.New("invalid user name or password")

type userKey struct{}

// WithUser returns ctx carrying u.
func WithUser(ctx context.Context, u User) context.Context {
	return context.WithValue(ctx, userKey{}, u)
}

// UserFrom returns the user attached by the middleware.
func UserFrom(ctx context.Context) (User, bool) {
	u, ok := ctx.Value(userKey{}).(User)
	return u, ok
}

// RedirectProvider signs users in through a browser redirect (OIDC, GitLab).
type RedirectProvider interface {
	// AuthURL is where the browser is sent to sign in.
	AuthURL(state, nonce, verifier string) string
	// Exchange turns the code from the callback into a verified user.
	Exchange(ctx context.Context, code, verifier, nonce string) (User, error)
	// LogoutURL ends the session at the provider; empty if unsupported.
	LogoutURL() string
}

// PasswordProvider checks a user name and password (LDAP).
type PasswordProvider interface {
	Authenticate(ctx context.Context, username, password string) (User, error)
}
