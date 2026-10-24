package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// OIDCProvider signs users in with the OpenID Connect authorization code flow and PKCE.
// GitLab uses it with GitLab's claim names.
type OIDCProvider struct {
	provider      *oidc.Provider
	verifier      *oidc.IDTokenVerifier
	oauth         oauth2.Config
	client        *http.Client
	usernameClaim string
	groupsClaims  []string
	logoutURL     string
}

// NewOIDC discovers the issuer's endpoints. redirectURL is Runwarden's /auth/callback.
func NewOIDC(ctx context.Context, cfg config.OIDC, redirectURL, publicURL string) (*OIDCProvider, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	ctx = oidc.ClientContext(ctx, client)
	p, err := oidc.NewProvider(ctx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("oidc discovery for %s: %w", cfg.IssuerURL, err)
	}
	var meta struct {
		EndSession string `json:"end_session_endpoint"`
	}
	_ = p.Claims(&meta)

	o := &OIDCProvider{
		provider: p,
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.ClientID}),
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     p.Endpoint(),
			RedirectURL:  redirectURL,
			Scopes:       cfg.Scopes,
		},
		client:        client,
		usernameClaim: cfg.UsernameClaim,
		groupsClaims:  cfg.GroupsClaims,
	}
	if meta.EndSession != "" {
		q := url.Values{"client_id": {cfg.ClientID}, "post_logout_redirect_uri": {publicURL + "/"}}
		o.logoutURL = meta.EndSession + "?" + q.Encode()
	}
	return o, nil
}

// AuthURL implements RedirectProvider.
func (o *OIDCProvider) AuthURL(state, nonce, verifier string) string {
	return o.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier))
}

// LogoutURL implements RedirectProvider.
func (o *OIDCProvider) LogoutURL() string { return o.logoutURL }

// Exchange implements RedirectProvider: it redeems the code, verifies the ID token (signature,
// issuer, audience, expiry, nonce) and reads the user name and groups from its claims, falling
// back to the userinfo endpoint for claims the ID token lacks.
func (o *OIDCProvider) Exchange(ctx context.Context, code, verifier, nonce string) (User, error) {
	ctx = oidc.ClientContext(ctx, o.client)
	tok, err := o.oauth.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return User{}, fmt.Errorf("code exchange: %w", err)
	}
	raw, ok := tok.Extra("id_token").(string)
	if !ok || raw == "" {
		return User{}, errors.New("no id_token in token response")
	}
	idt, err := o.verifier.Verify(ctx, raw)
	if err != nil {
		return User{}, fmt.Errorf("id token: %w", err)
	}
	if idt.Nonce != nonce {
		return User{}, errors.New("id token: nonce mismatch")
	}
	var idClaims map[string]any
	if err := idt.Claims(&idClaims); err != nil {
		return User{}, err
	}

	var infoClaims map[string]any
	userinfo := func() map[string]any {
		if infoClaims == nil {
			infoClaims = map[string]any{}
			if info, err := o.provider.UserInfo(ctx, oauth2.StaticTokenSource(tok)); err == nil {
				_ = info.Claims(&infoClaims)
			}
		}
		return infoClaims
	}
	claim := func(name string) (any, bool) {
		if v, ok := idClaims[name]; ok {
			return v, true
		}
		v, ok := userinfo()[name]
		return v, ok
	}

	u := User{}
	if v, ok := claim(o.usernameClaim); ok {
		u.Name, _ = v.(string)
	}
	if u.Name == "" {
		return User{}, fmt.Errorf("claim %q with the user name is missing", o.usernameClaim)
	}
	for _, name := range o.groupsClaims {
		if v, ok := claim(name); ok {
			u.Groups = stringList(v)
			break
		}
	}
	return u, nil
}

func stringList(v any) []string {
	switch t := v.(type) {
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	case string:
		if t != "" {
			return []string{t}
		}
	}
	return nil
}
