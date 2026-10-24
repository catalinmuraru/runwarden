package auth

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-ldap/ldap/v3"

	"github.com/catalinmuraru/runwarden/internal/config"
)

const ldapTimeout = 10 * time.Second

// ldapConn is the part of *ldap.Conn the provider uses; tests substitute a fake.
type ldapConn interface {
	Bind(username, password string) error
	Search(req *ldap.SearchRequest) (*ldap.SearchResult, error)
	Close() error
}

// LDAPProvider checks passwords by binding as the user, after finding the user's DN with a
// service account.
type LDAPProvider struct {
	cfg  config.LDAP
	dial func() (ldapConn, error)
}

// NewLDAP returns a provider for cfg.
func NewLDAP(cfg config.LDAP) *LDAPProvider {
	p := &LDAPProvider{cfg: cfg}
	p.dial = func() (ldapConn, error) {
		conn, err := ldap.DialURL(cfg.URL, ldap.DialWithDialer(&net.Dialer{Timeout: ldapTimeout}))
		if err != nil {
			return nil, err
		}
		conn.SetTimeout(ldapTimeout)
		if cfg.StartTLS {
			host := cfg.URL
			if u, err := url.Parse(cfg.URL); err == nil {
				host = u.Hostname()
			}
			if err := conn.StartTLS(&tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}); err != nil {
				_ = conn.Close()
				return nil, err
			}
		}
		return conn, nil
	}
	return p
}

// Authenticate implements PasswordProvider.
func (p *LDAPProvider) Authenticate(_ context.Context, username, password string) (User, error) {
	username = strings.TrimSpace(username)
	// An empty password would be an "unauthenticated bind", which many servers accept.
	if username == "" || password == "" {
		return User{}, ErrInvalidCredentials
	}
	conn, err := p.dial()
	if err != nil {
		return User{}, fmt.Errorf("ldap connect: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := p.serviceBind(conn); err != nil {
		return User{}, err
	}
	attrs := []string{"dn"}
	if p.cfg.UserGroupsAttr != "" {
		attrs = append(attrs, p.cfg.UserGroupsAttr)
	}
	filter := strings.ReplaceAll(p.cfg.UserFilter, "{username}", ldap.EscapeFilter(username))
	res, err := conn.Search(ldap.NewSearchRequest(p.cfg.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
		2, int(ldapTimeout.Seconds()), false, filter, attrs, nil))
	if err != nil {
		return User{}, fmt.Errorf("ldap user search: %w", err)
	}
	if len(res.Entries) != 1 {
		if len(res.Entries) > 1 {
			slog.Warn("ldap: user filter matches several entries", "user", username)
		}
		return User{}, ErrInvalidCredentials
	}
	entry := res.Entries[0]

	if err := conn.Bind(entry.DN, password); err != nil {
		var lerr *ldap.Error
		if errors.As(err, &lerr) && lerr.ResultCode == ldap.LDAPResultInvalidCredentials {
			return User{}, ErrInvalidCredentials
		}
		return User{}, fmt.Errorf("ldap user bind: %w", err)
	}

	u := User{Name: username}
	switch {
	case p.cfg.UserGroupsAttr != "":
		for _, dn := range entry.GetAttributeValues(p.cfg.UserGroupsAttr) {
			u.Groups = append(u.Groups, firstRDNValue(dn))
		}
	case p.cfg.GroupBaseDN != "":
		if err := p.serviceBind(conn); err != nil {
			return User{}, err
		}
		filter := strings.NewReplacer("{dn}", ldap.EscapeFilter(entry.DN), "{username}", ldap.EscapeFilter(username)).Replace(p.cfg.GroupFilter)
		res, err := conn.Search(ldap.NewSearchRequest(p.cfg.GroupBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases,
			0, int(ldapTimeout.Seconds()), false, filter, []string{p.cfg.GroupNameAttr}, nil))
		if err != nil {
			return User{}, fmt.Errorf("ldap group search: %w", err)
		}
		for _, g := range res.Entries {
			if name := g.GetAttributeValue(p.cfg.GroupNameAttr); name != "" {
				u.Groups = append(u.Groups, name)
			}
		}
	}
	return u, nil
}

func (p *LDAPProvider) serviceBind(conn ldapConn) error {
	if p.cfg.BindDN == "" {
		return nil
	}
	if err := conn.Bind(p.cfg.BindDN, p.cfg.BindPassword); err != nil {
		return fmt.Errorf("ldap service bind: %w", err)
	}
	return nil
}

// firstRDNValue turns "cn=ops,ou=groups,dc=example,dc=org" into "ops".
func firstRDNValue(dn string) string {
	parsed, err := ldap.ParseDN(dn)
	if err != nil || len(parsed.RDNs) == 0 || len(parsed.RDNs[0].Attributes) == 0 {
		return dn
	}
	return parsed.RDNs[0].Attributes[0].Value
}
