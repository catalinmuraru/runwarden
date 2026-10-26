// Package policy holds who may do what: roles, environments, rules and time-limited exceptions.
// It is stored as one JSON file on the data volume and edited in the Admin area.
//
// Rules only grant access; the highest access granted by any matching rule or exception wins.
// Users without a role are guests and see nothing. devops may do everything.
package policy

import (
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/config"
)

// Built-in roles.
const (
	RoleGuest      = "guest" // every user without a role: signed in, no access
	RoleDeveloper  = "developer"
	RoleConsultant = "consultant"
	RoleDevops     = "devops" // everything, including the Admin area
)

// Selectors.
const (
	Any           = "*"    // every script or every environment
	TagPrefix     = "tag:" // scripts carrying a tag
	NoEnvironment = "-"    // runs of scripts without environment parameters
)

// BuiltInRoles are always present and cannot be deleted.
var BuiltInRoles = []Role{
	{Name: RoleGuest, Description: "Signed in, no access. Every user without a role."},
	{Name: RoleDeveloper, Description: "Develops and tests: usually runs on development only."},
	{Name: RoleConsultant, Description: "Supports customers: runs the scripts tagged for consultants."},
	{Name: RoleDevops, Description: "Full access to every script and environment, and the Admin area."},
}

// Access is what a user may do with a script in an environment.
type Access int

// Access levels; each includes the ones before it.
const (
	None Access = iota
	View        // see the script, its form and its runs, but not start it
	Run         // start it
)

func (a Access) String() string {
	switch a {
	case View:
		return "view"
	case Run:
		return "run"
	}
	return "none"
}

// ParseAccess parses "view" or "run".
func ParseAccess(s string) (Access, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "view":
		return View, nil
	case "run":
		return Run, nil
	}
	return None, fmt.Errorf("access must be view or run, got %q", s)
}

// MarshalText encodes the access as "none", "view" or "run".
func (a Access) MarshalText() ([]byte, error) { return []byte(a.String()), nil }

// UnmarshalText accepts "view" or "run".
func (a *Access) UnmarshalText(b []byte) error {
	v, err := ParseAccess(string(b))
	*a = v
	return err
}

// Environment is a group of target values (namespaces, clusters, stages) with the same rights.
type Environment struct {
	Name string `json:"name" yaml:"name"`
	// Match are glob patterns (case-insensitive) for parameter values in this environment.
	// The name itself always matches.
	Match []string `json:"match,omitempty" yaml:"match,omitempty"`
}

// Role is a set of rules assigned to users.
type Role struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// UserRoles are the roles assigned to one user.
type UserRoles struct {
	Name  string   `json:"name" yaml:"name"`
	Roles []string `json:"roles" yaml:"roles"`
}

// Rule grants a role access to scripts in environments.
type Rule struct {
	ID           string   `json:"id" yaml:"id"`
	Role         string   `json:"role" yaml:"role"`
	Scripts      []string `json:"scripts" yaml:"scripts"`           // "*", script names or "tag:<tag>"
	Environments []string `json:"environments" yaml:"environments"` // "*", "-" or environment names
	Access       Access   `json:"access" yaml:"access"`
}

// Exception grants one user access until it expires.
type Exception struct {
	ID           string    `json:"id" yaml:"id"`
	User         string    `json:"user" yaml:"user"`
	Scripts      []string  `json:"scripts" yaml:"scripts"`
	Environments []string  `json:"environments" yaml:"environments"`
	Access       Access    `json:"access" yaml:"access"`
	Reason       string    `json:"reason" yaml:"reason"`
	Expires      time.Time `json:"expires" yaml:"expires"`
	CreatedBy    string    `json:"created_by" yaml:"created_by"`
	Created      time.Time `json:"created" yaml:"created"`
	// ExpiryAudited is set once the expiry has been written to the audit log.
	ExpiryAudited bool `json:"expiry_audited,omitempty" yaml:"-"`
}

// Active reports whether the exception grants access at now.
func (e Exception) Active(now time.Time) bool { return now.Before(e.Expires) }

// Policy is the whole access configuration.
type Policy struct {
	// Revision increases with every change; a change based on an older revision is refused.
	Revision int `json:"revision" yaml:"-"`
	// Environments are ordered by sensitivity, the most sensitive last.
	Environments []Environment `json:"environments" yaml:"environments"`
	// UnknownEnvironment receives values that match no environment; empty means the last one.
	UnknownEnvironment string      `json:"unknown_environment,omitempty" yaml:"unknown_environment,omitempty"`
	Roles              []Role      `json:"roles" yaml:"roles"`
	Users              []UserRoles `json:"users" yaml:"users"`
	Rules              []Rule      `json:"rules" yaml:"rules"`
	Exceptions         []Exception `json:"exceptions" yaml:"exceptions"`
}

// Clone returns a deep copy.
func (p *Policy) Clone() *Policy {
	data, err := json.Marshal(p)
	if err != nil {
		panic(err) // plain data: cannot fail
	}
	var c Policy
	if err := json.Unmarshal(data, &c); err != nil {
		panic(err)
	}
	return &c
}

// Environment returns the environment a parameter value belongs to: the first environment whose
// name or patterns match, else the unknown environment. Empty values belong to none ("").
func (p *Policy) Environment(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	if v == "" {
		return ""
	}
	for _, e := range p.Environments {
		if strings.EqualFold(e.Name, v) {
			return e.Name
		}
		for _, m := range e.Match {
			if ok, _ := path.Match(strings.ToLower(m), v); ok {
				return e.Name
			}
		}
	}
	return p.Unknown()
}

// Unknown returns the environment of values that match nothing.
func (p *Policy) Unknown() string {
	if p.UnknownEnvironment != "" {
		return p.UnknownEnvironment
	}
	if n := len(p.Environments); n > 0 {
		return p.Environments[n-1].Name
	}
	return NoEnvironment
}

// EnvironmentNames lists the environments, least sensitive first.
func (p *Policy) EnvironmentNames() []string {
	out := make([]string, len(p.Environments))
	for i, e := range p.Environments {
		out[i] = e.Name
	}
	return out
}

// RolesOf returns the roles assigned to user in the policy.
func (p *Policy) RolesOf(user string) []string {
	for _, u := range p.Users {
		if strings.EqualFold(u.Name, user) {
			return u.Roles
		}
	}
	return nil
}

// Subject is who asks: a user name and the user's roles.
type Subject struct {
	User  string
	Roles []string
}

// IsDevops reports whether the subject has the devops role.
func (s Subject) IsDevops() bool { return containsFold(s.Roles, RoleDevops) }

// Grant is the access a subject has and what grants it.
type Grant struct {
	Access Access `json:"access"`
	By     string `json:"by,omitempty"` // e.g. "role devops", "rule 1a2b (developer)", "exception 3c4d"
}

// Access returns the highest access granted to sub for script s in environment env (an
// environment name, or NoEnvironment for scripts without environment parameters).
func (p *Policy) Access(sub Subject, s *config.Script, env string, now time.Time) Grant {
	if sub.IsDevops() {
		return Grant{Access: Run, By: "role " + RoleDevops}
	}
	best := Grant{}
	consider := func(a Access, by string) {
		if a > best.Access {
			best = Grant{Access: a, By: by}
		}
	}
	for _, r := range p.Rules {
		if containsFold(sub.Roles, r.Role) && matchScript(r.Scripts, s) && matchEnv(r.Environments, env) {
			consider(r.Access, fmt.Sprintf("rule %s (%s)", r.ID, r.Role))
		}
	}
	for _, e := range p.Exceptions {
		if strings.EqualFold(e.User, sub.User) && e.Active(now) && matchScript(e.Scripts, s) && matchEnv(e.Environments, env) {
			consider(e.Access, fmt.Sprintf("exception %s until %s", e.ID, e.Expires.UTC().Format("2006-01-02 15:04")))
		}
	}
	return best
}

func matchScript(selectors []string, s *config.Script) bool {
	for _, sel := range selectors {
		switch {
		case sel == Any:
			return true
		case strings.HasPrefix(sel, TagPrefix):
			if s.HasTag(sel[len(TagPrefix):]) {
				return true
			}
		case strings.EqualFold(sel, s.Name):
			return true
		}
	}
	return false
}

func matchEnv(selectors []string, env string) bool {
	for _, sel := range selectors {
		if sel == Any || strings.EqualFold(sel, env) {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
