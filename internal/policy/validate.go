package policy

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// ValidationError lists every problem of a policy that cannot be saved.
type ValidationError struct{ Problems []string }

func (e *ValidationError) Error() string { return "invalid policy: " + strings.Join(e.Problems, "; ") }

var namePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// IsBuiltIn reports whether role is one of the built-in roles.
func IsBuiltIn(role string) bool {
	for _, r := range BuiltInRoles {
		if strings.EqualFold(r.Name, role) {
			return true
		}
	}
	return false
}

// normalize fills defaults: built-in roles, IDs, trimmed names, and drops users without roles.
func (p *Policy) normalize() {
	for i := len(BuiltInRoles) - 1; i >= 0; i-- {
		if !slices.ContainsFunc(p.Roles, func(r Role) bool { return strings.EqualFold(r.Name, BuiltInRoles[i].Name) }) {
			p.Roles = append([]Role{BuiltInRoles[i]}, p.Roles...)
		}
	}
	users := p.Users[:0]
	for _, u := range p.Users {
		u.Name = strings.TrimSpace(u.Name)
		if len(u.Roles) > 0 {
			users = append(users, u)
		}
	}
	p.Users = users
	for i := range p.Rules {
		if p.Rules[i].ID == "" {
			p.Rules[i].ID = NewID()
		}
	}
	for i := range p.Exceptions {
		if p.Exceptions[i].ID == "" {
			p.Exceptions[i].ID = NewID()
		}
	}
	if p.Environments == nil {
		p.Environments = []Environment{}
	}
	if p.Users == nil {
		p.Users = []UserRoles{}
	}
	if p.Rules == nil {
		p.Rules = []Rule{}
	}
	if p.Exceptions == nil {
		p.Exceptions = []Exception{}
	}
}

// NewID returns a short random ID for rules and exceptions.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Validate checks the policy. maxExceptionDays bounds how long an exception may last.
func (p *Policy) Validate(maxExceptionDays int) error {
	var probs []string
	add := func(format string, args ...any) { probs = append(probs, fmt.Sprintf(format, args...)) }

	envs := map[string]bool{}
	for i, e := range p.Environments {
		switch {
		case !namePattern.MatchString(e.Name):
			add("environment %d: name %q must be letters, digits, '.', '_' or '-'", i+1, e.Name)
		case envs[strings.ToLower(e.Name)]:
			add("environment %q is defined twice", e.Name)
		case strings.EqualFold(e.Name, "common"):
			add("environment name %q is reserved for secrets shared by all environments", e.Name)
		}
		envs[strings.ToLower(e.Name)] = true
		for _, m := range e.Match {
			if strings.TrimSpace(m) == "" {
				add("environment %q: empty pattern", e.Name)
			} else if _, err := path.Match(m, ""); err != nil {
				add("environment %q: invalid pattern %q", e.Name, m)
			}
		}
	}
	if p.UnknownEnvironment != "" && !envs[strings.ToLower(p.UnknownEnvironment)] {
		add("unknown environment %q is not an environment", p.UnknownEnvironment)
	}

	roles := map[string]bool{}
	for _, r := range p.Roles {
		switch {
		case !namePattern.MatchString(r.Name):
			add("role %q: name must be letters, digits, '.', '_' or '-'", r.Name)
		case roles[strings.ToLower(r.Name)]:
			add("role %q is defined twice", r.Name)
		}
		roles[strings.ToLower(r.Name)] = true
	}

	users := map[string]bool{}
	for _, u := range p.Users {
		if u.Name == "" {
			add("a user without a name has roles")
			continue
		}
		if users[strings.ToLower(u.Name)] {
			add("user %q is listed twice", u.Name)
		}
		users[strings.ToLower(u.Name)] = true
		for _, r := range u.Roles {
			switch {
			case strings.EqualFold(r, RoleGuest):
				add("user %q: guest cannot be assigned (users without roles are guests)", u.Name)
			case !roles[strings.ToLower(r)]:
				add("user %q: unknown role %q", u.Name, r)
			}
		}
	}

	checkTarget := func(what string, scripts, environments []string, access Access) {
		if len(scripts) == 0 {
			add("%s: no scripts", what)
		}
		for _, s := range scripts {
			switch {
			case strings.TrimSpace(s) == "":
				add("%s: empty script name", what)
			case strings.HasPrefix(s, TagPrefix) && !namePattern.MatchString(s[len(TagPrefix):]):
				add("%s: invalid tag %q", what, s)
			}
		}
		if len(environments) == 0 {
			add("%s: no environments", what)
		}
		for _, e := range environments {
			if e != Any && e != NoEnvironment && !envs[strings.ToLower(e)] {
				add("%s: unknown environment %q", what, e)
			}
		}
		if access != View && access != Run {
			add("%s: access must be view or run", what)
		}
	}
	ids := map[string]bool{}
	for _, r := range p.Rules {
		what := "rule " + r.ID
		if ids[r.ID] {
			add("%s: duplicate ID", what)
		}
		ids[r.ID] = true
		switch {
		case strings.EqualFold(r.Role, RoleGuest), strings.EqualFold(r.Role, RoleDevops):
			add("%s: role %s cannot have rules", what, r.Role)
		case !roles[strings.ToLower(r.Role)]:
			add("%s: unknown role %q", what, r.Role)
		}
		checkTarget(what, r.Scripts, r.Environments, r.Access)
	}
	maxLen := time.Duration(maxExceptionDays)*24*time.Hour + time.Hour // an hour of slack for date pickers
	for _, e := range p.Exceptions {
		what := "exception " + e.ID
		if ids[e.ID] {
			add("%s: duplicate ID", what)
		}
		ids[e.ID] = true
		if strings.TrimSpace(e.User) == "" {
			add("%s: no user", what)
		}
		if strings.TrimSpace(e.Reason) == "" {
			add("%s: a reason is required", what)
		}
		switch {
		case e.Expires.IsZero() || e.Created.IsZero():
			add("%s: created and expires are required", what)
		case !e.Expires.After(e.Created):
			add("%s: expires before it starts", what)
		case maxExceptionDays > 0 && e.Expires.Sub(e.Created) > maxLen:
			add("%s: may last at most %d days", what, maxExceptionDays)
		}
		checkTarget(what, e.Scripts, e.Environments, e.Access)
	}
	if len(probs) > 0 {
		return &ValidationError{Problems: probs}
	}
	return nil
}

// Warnings lists selectors that match no script in the catalogue. They are allowed (scripts come
// and go) but usually typos.
func (p *Policy) Warnings(cat *config.Catalog) []string {
	if cat == nil {
		return nil
	}
	var out []string
	check := func(what string, scripts []string) {
		for _, sel := range scripts {
			if sel == Any {
				continue
			}
			found := slices.ContainsFunc(cat.Scripts, func(s *config.Script) bool { return matchScript([]string{sel}, s) })
			if !found {
				out = append(out, fmt.Sprintf("%s: %q matches no script", what, sel))
			}
		}
	}
	for _, r := range p.Rules {
		check("rule "+r.ID, r.Scripts)
	}
	for _, e := range p.Exceptions {
		check("exception "+e.ID, e.Scripts)
	}
	return out
}

// ErrConflict means the policy changed since the revision a change was based on.
var ErrConflict = errors.New("the access policy was changed by someone else; reload and try again")
