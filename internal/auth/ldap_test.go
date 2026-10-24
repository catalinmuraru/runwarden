package auth

import (
	"errors"
	"slices"
	"testing"

	"github.com/go-ldap/ldap/v3"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// fakeDirectory answers the searches the provider makes, keyed by exact filter strings.
type fakeDirectory struct {
	passwords map[string]string        // DN → password
	users     map[string][]*ldap.Entry // user filter → entries
	groups    map[string][]*ldap.Entry // group filter → entries
	filters   []string                 // filters seen, in order
	dials     int
}

const (
	serviceDN = "cn=svc,dc=example,dc=org"
	aliceDN   = "uid=alice,ou=people,dc=example,dc=org"
)

func newDirectory() *fakeDirectory {
	alice := ldap.NewEntry(aliceDN, map[string][]string{"memberOf": {"cn=ops,ou=groups,dc=example,dc=org", "cn=dev,ou=groups,dc=example,dc=org"}})
	return &fakeDirectory{
		passwords: map[string]string{serviceDN: "svc-pass", aliceDN: "alice-pass"},
		users:     map[string][]*ldap.Entry{"(uid=alice)": {alice}},
		groups: map[string][]*ldap.Entry{"(member=" + aliceDN + ")": {
			ldap.NewEntry("cn=ops,ou=groups,dc=example,dc=org", map[string][]string{"cn": {"ops"}}),
		}},
	}
}

type fakeConn struct{ d *fakeDirectory }

func (c fakeConn) Bind(dn, pw string) error {
	if want, ok := c.d.passwords[dn]; ok && pw == want {
		return nil
	}
	return &ldap.Error{ResultCode: ldap.LDAPResultInvalidCredentials, Err: errors.New("invalid credentials")}
}

func (c fakeConn) Search(req *ldap.SearchRequest) (*ldap.SearchResult, error) {
	c.d.filters = append(c.d.filters, req.Filter)
	if req.BaseDN == "ou=groups,dc=example,dc=org" {
		return &ldap.SearchResult{Entries: c.d.groups[req.Filter]}, nil
	}
	return &ldap.SearchResult{Entries: c.d.users[req.Filter]}, nil
}

func (fakeConn) Close() error { return nil }

func ldapProvider(d *fakeDirectory, mutate func(*config.LDAP)) *LDAPProvider {
	cfg := config.LDAP{
		BindDN: serviceDN, BindPassword: "svc-pass",
		UserBaseDN: "ou=people,dc=example,dc=org", UserFilter: "(uid={username})",
		GroupBaseDN: "ou=groups,dc=example,dc=org", GroupFilter: "(member={dn})", GroupNameAttr: "cn",
	}
	if mutate != nil {
		mutate(&cfg)
	}
	p := NewLDAP(cfg)
	p.dial = func() (ldapConn, error) {
		d.dials++
		return fakeConn{d}, nil
	}
	return p
}

func TestLDAPSignIn(t *testing.T) {
	d := newDirectory()
	u, err := ldapProvider(d, nil).Authenticate(t.Context(), " alice ", "alice-pass")
	if err != nil {
		t.Fatal(err)
	}
	if u.Name != "alice" || !slices.Equal(u.Groups, []string{"ops"}) {
		t.Errorf("user = %+v", u)
	}
}

func TestLDAPGroupsFromAttribute(t *testing.T) {
	d := newDirectory()
	p := ldapProvider(d, func(c *config.LDAP) { c.UserGroupsAttr = "memberOf"; c.GroupBaseDN = "" })
	u, err := p.Authenticate(t.Context(), "alice", "alice-pass")
	if err != nil || !slices.Equal(u.Groups, []string{"ops", "dev"}) {
		t.Errorf("user = %+v, err = %v", u, err)
	}
}

func TestLDAPRejects(t *testing.T) {
	tests := []struct {
		name, user, password string
		setup                func(d *fakeDirectory)
	}{
		{"wrong password", "alice", "nope", nil},
		{"unknown user", "mallory", "x", nil},
		{"several matches", "alice", "alice-pass", func(d *fakeDirectory) {
			d.users["(uid=alice)"] = append(d.users["(uid=alice)"], ldap.NewEntry("uid=alice,ou=other,dc=example,dc=org", nil))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newDirectory()
			if tt.setup != nil {
				tt.setup(d)
			}
			if _, err := ldapProvider(d, nil).Authenticate(t.Context(), tt.user, tt.password); !errors.Is(err, ErrInvalidCredentials) {
				t.Errorf("err = %v, want ErrInvalidCredentials", err)
			}
		})
	}
}

func TestLDAPEmptyPasswordNeverReachesServer(t *testing.T) {
	d := newDirectory()
	for _, c := range [][2]string{{"alice", ""}, {"", "x"}, {"  ", "x"}} {
		if _, err := ldapProvider(d, nil).Authenticate(t.Context(), c[0], c[1]); !errors.Is(err, ErrInvalidCredentials) {
			t.Errorf("%q/%q: err = %v", c[0], c[1], err)
		}
	}
	if d.dials != 0 {
		t.Errorf("connected %d times for empty credentials", d.dials)
	}
}

func TestLDAPFilterEscaping(t *testing.T) {
	d := newDirectory()
	_, _ = ldapProvider(d, nil).Authenticate(t.Context(), "*)(uid=*", "x")
	if len(d.filters) == 0 || d.filters[0] != `(uid=\2a\29\28uid=\2a)` {
		t.Errorf("filters = %q", d.filters)
	}
}

func TestLDAPServiceBindFailure(t *testing.T) {
	d := newDirectory()
	p := ldapProvider(d, func(c *config.LDAP) { c.BindPassword = "wrong" })
	_, err := p.Authenticate(t.Context(), "alice", "alice-pass")
	if err == nil || errors.Is(err, ErrInvalidCredentials) {
		t.Errorf("err = %v, want a service error (not a user error)", err)
	}
}
