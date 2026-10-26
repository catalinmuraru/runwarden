package policy

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
)

var seedEnvs = []Environment{
	{Name: "development", Match: []string{"dev*", "test*"}},
	{Name: "acceptance", Match: []string{"acc*", "uat*"}},
	{Name: "production", Match: []string{"prod*", "prd*"}},
}

func open(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	if opts.Environments == nil {
		opts.Environments = seedEnvs
	}
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEnvironment(t *testing.T) {
	p := &Policy{Environments: seedEnvs}
	for value, want := range map[string]string{
		"development": "development", "Dev-01": "development", "test": "development",
		"uat2": "acceptance", "PRODUCTION": "production", "prd-west": "production",
		"staging": "production", // unknown values are treated as the most sensitive
		"  ":      "",
	} {
		if got := p.Environment(value); got != want {
			t.Errorf("Environment(%q) = %q, want %q", value, got, want)
		}
	}
	p.UnknownEnvironment = "acceptance"
	if got := p.Environment("staging"); got != "acceptance" {
		t.Errorf("unknown environment = %q", got)
	}
	if got := (&Policy{}).Environment("x"); got != NoEnvironment {
		t.Errorf("without environments = %q", got)
	}
}

func TestAccess(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	sql := &config.Script{Name: "Upload SQL", Tags: []string{"developer", "consultant"}}
	keys := &config.Script{Name: "Rotate keys", Tags: []string{"devops"}}
	p := &Policy{
		Environments: seedEnvs,
		Roles:        append(append([]Role{}, BuiltInRoles...), Role{Name: "support"}),
		Rules: []Rule{
			{ID: "r1", Role: RoleDeveloper, Scripts: []string{"tag:developer"}, Environments: []string{"development"}, Access: Run},
			{ID: "r2", Role: RoleDeveloper, Scripts: []string{"tag:Developer"}, Environments: []string{"acceptance", "production"}, Access: View},
			{ID: "r3", Role: RoleConsultant, Scripts: []string{"tag:consultant"}, Environments: []string{Any}, Access: Run},
			{ID: "r4", Role: "support", Scripts: []string{"upload sql"}, Environments: []string{"production"}, Access: View},
		},
		Exceptions: []Exception{
			{ID: "e1", User: "jdoe", Scripts: []string{"Upload SQL"}, Environments: []string{"acceptance"}, Access: Run, Expires: now.Add(time.Hour)},
			{ID: "e2", User: "jdoe", Scripts: []string{Any}, Environments: []string{Any}, Access: Run, Expires: now.Add(-time.Hour)},
		},
	}
	dev := Subject{User: "jdoe", Roles: []string{RoleDeveloper}}
	tests := []struct {
		name string
		sub  Subject
		s    *config.Script
		env  string
		want Access
	}{
		{"developer runs on development", dev, sql, "development", Run},
		{"developer views production", dev, sql, "production", View},
		{"exception gives run on acceptance", dev, sql, "acceptance", Run},
		{"other developer views acceptance", Subject{User: "ann", Roles: []string{"Developer"}}, sql, "acceptance", View},
		{"expired exception grants nothing", dev, keys, "development", None},
		{"consultant everywhere", Subject{User: "c", Roles: []string{RoleConsultant}}, sql, "production", Run},
		{"consultant on environment-less runs", Subject{User: "c", Roles: []string{RoleConsultant}}, sql, NoEnvironment, Run},
		{"union of roles", Subject{User: "x", Roles: []string{"support", RoleDeveloper}}, sql, "production", View},
		{"named rule", Subject{User: "x", Roles: []string{"support"}}, sql, "production", View},
		{"named rule other env", Subject{User: "x", Roles: []string{"support"}}, sql, "acceptance", None},
		{"guest", Subject{User: "g"}, sql, "development", None},
		{"devops", Subject{User: "d", Roles: []string{"DEVOPS"}}, keys, "production", Run},
		{"tag rule needs the tag", dev, keys, "development", None},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := p.Access(tt.sub, tt.s, tt.env, now); got.Access != tt.want {
				t.Errorf("access = %v (%s), want %v", got.Access, got.By, tt.want)
			}
		})
	}
	if g := p.Access(dev, sql, "acceptance", now); !strings.Contains(g.By, "exception e1") {
		t.Errorf("granted by %q, want the exception", g.By)
	}
}

func TestStoreSeedPersistAndConflict(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, Options{})
	p := s.Snapshot()
	if p.Revision != 1 || len(p.Environments) != 3 || len(p.Roles) != len(BuiltInRoles) || len(p.Users) != 0 {
		t.Fatalf("seeded policy = %+v", p)
	}
	if info, err := os.Stat(filepath.Join(dir, policyFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("policy file: %v %v", info, err)
	}
	if sub := s.Subject("anyone"); len(sub.Roles) != 0 {
		t.Errorf("new user is not a guest: %+v", sub)
	}

	_, after, err := s.Update(1, func(p *Policy) error {
		p.Roles = append(p.Roles, Role{Name: "support"})
		p.Users = append(p.Users, UserRoles{Name: "ann", Roles: []string{"support"}}, UserRoles{Name: "nobody"})
		p.Rules = append(p.Rules, Rule{Role: "support", Scripts: []string{Any}, Environments: []string{"development"}, Access: View})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != 2 || len(after.Users) != 1 || after.Rules[0].ID == "" {
		t.Errorf("after update = %+v", after)
	}
	if _, _, err := s.Update(1, func(*Policy) error { return nil }); !errors.Is(err, ErrConflict) {
		t.Errorf("stale revision: err = %v", err)
	}
	if p.Revision != 1 || len(p.Rules) != 0 {
		t.Errorf("snapshot changed after update: %+v", p)
	}

	// Reopened: the stored policy wins over a different seed.
	s2 := open(t, dir, Options{Environments: []Environment{{Name: "other"}}})
	if got := s2.Snapshot(); got.Revision != 2 || len(got.Environments) != 3 || len(got.Rules) != 1 {
		t.Errorf("reopened = %+v", got)
	}
	if sub := s2.Subject("ANN"); len(sub.Roles) != 1 || sub.Roles[0] != "support" {
		t.Errorf("subject = %+v", sub)
	}
}

func TestValidate(t *testing.T) {
	s := open(t, t.TempDir(), Options{MaxExceptionDays: 30})
	now := time.Now()
	tests := map[string]func(p *Policy){
		"duplicate environment": func(p *Policy) { p.Environments = append(p.Environments, Environment{Name: "Production"}) },
		"bad pattern":           func(p *Policy) { p.Environments[0].Match = []string{"[dev"} },
		"unknown env not found": func(p *Policy) { p.UnknownEnvironment = "staging" },
		"rule unknown role": func(p *Policy) {
			p.Rules = []Rule{{Role: "x", Scripts: []string{Any}, Environments: []string{Any}, Access: Run}}
		},
		"rule on devops": func(p *Policy) {
			p.Rules = []Rule{{Role: RoleDevops, Scripts: []string{Any}, Environments: []string{Any}, Access: Run}}
		},
		"rule unknown env": func(p *Policy) {
			p.Rules = []Rule{{Role: RoleDeveloper, Scripts: []string{Any}, Environments: []string{"staging"}, Access: Run}}
		},
		"rule without access": func(p *Policy) {
			p.Rules = []Rule{{Role: RoleDeveloper, Scripts: []string{Any}, Environments: []string{Any}}}
		},
		"guest assigned": func(p *Policy) { p.Users = []UserRoles{{Name: "a", Roles: []string{RoleGuest}}} },
		"exception without reason": func(p *Policy) {
			p.Exceptions = []Exception{{User: "a", Scripts: []string{Any}, Environments: []string{Any}, Access: Run, Created: now, Expires: now.Add(time.Hour)}}
		},
		"exception too long": func(p *Policy) {
			p.Exceptions = []Exception{{User: "a", Reason: "r", Scripts: []string{Any}, Environments: []string{Any}, Access: Run, Created: now, Expires: now.Add(31 * 24 * time.Hour)}}
		},
		"role name": func(p *Policy) { p.Roles = append(p.Roles, Role{Name: "two words"}) },
	}
	for name, change := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := s.Update(s.Snapshot().Revision, func(p *Policy) error { change(p); return nil })
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Errorf("err = %v, want a validation error", err)
			}
		})
	}
	// Built-in roles come back when removed.
	_, after, err := s.Update(s.Snapshot().Revision, func(p *Policy) error { p.Roles = nil; return nil })
	if err != nil || len(after.Roles) != len(BuiltInRoles) {
		t.Errorf("roles after removing all = %+v, %v", after, err)
	}
}

func TestSweep(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	now := time.Now()
	ex := func(id string, expires time.Time) Exception {
		return Exception{ID: id, User: "a", Reason: "r", Scripts: []string{Any}, Environments: []string{Any}, Access: Run,
			Created: expires.Add(-time.Hour), Expires: expires}
	}
	if _, _, err := s.Update(1, func(p *Policy) error {
		p.Exceptions = []Exception{ex("active", now.Add(time.Hour)), ex("expired", now.Add(-time.Minute)), ex("old", now.Add(-40*24*time.Hour))}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	expired, err := s.Sweep()
	if err != nil || len(expired) != 2 {
		t.Fatalf("first sweep = %+v, %v", expired, err)
	}
	p := s.Snapshot()
	if len(p.Exceptions) != 2 || p.Revision != 2 {
		t.Errorf("after sweep = %+v", p)
	}
	if expired, _ := s.Sweep(); len(expired) != 0 {
		t.Errorf("second sweep audits again: %+v", expired)
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	s := open(t, t.TempDir(), Options{})
	now := time.Now().UTC().Truncate(time.Second)
	_, before, err := s.Update(1, func(p *Policy) error {
		p.Users = []UserRoles{{Name: "ann", Roles: []string{RoleDeveloper}}}
		p.Rules = []Rule{{Role: RoleDeveloper, Scripts: []string{"tag:developer"}, Environments: []string{"development"}, Access: Run}}
		p.Exceptions = []Exception{{User: "ann", Reason: "incident 123", Scripts: []string{"Upload SQL"}, Environments: []string{"acceptance"},
			Access: View, Created: now, Expires: now.Add(24 * time.Hour), CreatedBy: "boss"}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := ExportYAML(before)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "access: run") || strings.Contains(string(data), "revision") {
		t.Errorf("export:\n%s", data)
	}
	imported, err := ImportYAML(data, now)
	if err != nil {
		t.Fatal(err)
	}
	imported.Revision = before.Revision
	if a, b := mustJSON(t, before), mustJSON(t, imported); a != b {
		t.Errorf("round trip differs:\n%s\n%s", a, b)
	}
	if sum := Summarize(before, &Policy{}); sum.RemovedRules != 1 || sum.RemovedExceptions != 1 || sum.RemovedUsers != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if _, err := ImportYAML([]byte("rulez: []\n"), now); err == nil {
		t.Error("unknown field accepted")
	}
}

func mustJSON(t *testing.T, p *Policy) string {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestKeepDevops(t *testing.T) {
	setDevops := func(s *Store, users ...string) error {
		_, _, err := s.Update(s.Snapshot().Revision, func(p *Policy) error {
			p.Users = nil
			for _, u := range users {
				p.Users = append(p.Users, UserRoles{Name: u, Roles: []string{RoleDevops}})
			}
			return nil
		})
		return err
	}
	guarded := open(t, t.TempDir(), Options{KeepDevops: true})
	if err := setDevops(guarded); err != nil {
		t.Errorf("no devops before and after: %v", err)
	}
	if err := setDevops(guarded, "ann", "bob"); err != nil {
		t.Fatal(err)
	}
	if err := setDevops(guarded, "bob"); err != nil {
		t.Errorf("removing one of two devops: %v", err)
	}
	var verr *ValidationError
	if err := setDevops(guarded); !errors.As(err, &verr) || !strings.Contains(err.Error(), "built-in administrator") {
		t.Errorf("removing the last devops: %v", err)
	}
	if got := guarded.Snapshot().DevopsUsers(); len(got) != 1 || got[0] != "bob" {
		t.Errorf("devops after the refusal = %v", got)
	}

	// While the built-in administrator is enabled it can always step in.
	unguarded := open(t, t.TempDir(), Options{})
	if err := setDevops(unguarded, "ann"); err != nil {
		t.Fatal(err)
	}
	if err := setDevops(unguarded); err != nil {
		t.Errorf("unguarded store refused: %v", err)
	}
}
