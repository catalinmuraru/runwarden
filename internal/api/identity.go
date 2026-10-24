package api

import (
	"context"
	"net/http"
)

// User is the identity behind a request.
type User struct {
	Name   string
	Groups []string
}

type userKey struct{}

// anonymous is used until authentication is configured.
var anonymous = User{Name: "anonymous"}

// withUser attaches the requesting user to the context.
func withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userKey{}, anonymous)))
	})
}

func userFrom(ctx context.Context) User {
	if u, ok := ctx.Value(userKey{}).(User); ok {
		return u
	}
	return anonymous
}
