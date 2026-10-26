// Package authz decides what a signed-in user may do, from the access policy (roles, rules and
// exceptions per script and environment). Names match case-insensitively.
package authz

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/policy"
)

// Authorizer answers access questions from the current policy.
type Authorizer struct {
	// Store holds the policy. Without one only devops users (Everyone, the built-in
	// administrator) have access.
	Store *policy.Store
	// Everyone makes every user devops (AUTH_TYPE=none, local development only).
	Everyone bool
	// Now is the clock for exceptions; nil means time.Now.
	Now func() time.Time
}

func (a *Authorizer) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Policy returns the current policy snapshot (not to be modified).
func (a *Authorizer) Policy() *policy.Policy {
	if a.Store == nil {
		return &policy.Policy{}
	}
	return a.Store.Snapshot()
}

// Subject returns u with the roles that apply to it.
func (a *Authorizer) Subject(u auth.User) policy.Subject {
	if a.Everyone || (u.Method == auth.MethodLocal && u.Name == auth.LocalAdminName) {
		return policy.Subject{User: u.Name, Roles: []string{policy.RoleDevops}}
	}
	if a.Store == nil {
		return policy.Subject{User: u.Name}
	}
	return a.Store.Subject(u.Name)
}

// Roles returns u's roles; empty means guest.
func (a *Authorizer) Roles(u auth.User) []string { return a.Subject(u).Roles }

// IsAdmin reports devops: every script, every run and the Admin area.
func (a *Authorizer) IsAdmin(u auth.User) bool { return a.Subject(u).IsDevops() }

// scriptEnvironments are the environments a script can run in: every environment if it has
// environment parameters, else only NoEnvironment.
func scriptEnvironments(p *policy.Policy, s *config.Script) []string {
	if len(s.EnvironmentParameters) == 0 {
		return []string{policy.NoEnvironment}
	}
	return p.EnvironmentNames()
}

// ScriptAccess returns u's access to s per environment, leaving out environments without
// access.
func (a *Authorizer) ScriptAccess(u auth.User, s *config.Script) map[string]policy.Access {
	p, sub, now := a.Policy(), a.Subject(u), a.now()
	out := map[string]policy.Access{}
	for _, env := range scriptEnvironments(p, s) {
		if g := p.Access(sub, s, env, now); g.Access > policy.None {
			out[env] = g.Access
		}
	}
	return out
}

// CanSee reports whether u may see s at all: view access in at least one environment.
func (a *Authorizer) CanSee(u auth.User, s *config.Script) bool {
	return len(a.ScriptAccess(u, s)) > 0
}

// EnvUse is an environment a run touches through one of its parameters.
type EnvUse struct {
	Param       string
	Environment string
	Use         string // config.EnvTarget or config.EnvSource
}

// Uses returns the environments selected by the values of s's environment parameters. values
// returns a parameter's value(s); empty values select nothing.
func (a *Authorizer) Uses(s *config.Script, values func(param string) []string) []EnvUse {
	p := a.Policy()
	var out []EnvUse
	for _, name := range slices.Sorted(maps.Keys(s.EnvironmentParameters)) {
		for _, v := range values(name) {
			if env := p.Environment(v); env != "" {
				out = append(out, EnvUse{Param: name, Environment: env, Use: s.EnvironmentParameters[name]})
			}
		}
	}
	return out
}

// Environments returns the distinct environments of uses, or NoEnvironment when there are none.
func Environments(uses []EnvUse) []string {
	var out []string
	for _, u := range uses {
		if !slices.Contains(out, u.Environment) {
			out = append(out, u.Environment)
		}
	}
	if len(out) == 0 {
		return []string{policy.NoEnvironment}
	}
	return out
}

// CheckStart returns nil if u may start s with these environment uses: run access on every
// target environment and view access on every source. The error says what is missing.
func (a *Authorizer) CheckStart(u auth.User, s *config.Script, uses []EnvUse) error {
	p, sub, now := a.Policy(), a.Subject(u), a.now()
	if len(uses) == 0 {
		uses = []EnvUse{{Environment: policy.NoEnvironment, Use: config.EnvTarget}}
	}
	for _, use := range uses {
		need, got := policy.Run, p.Access(sub, s, use.Environment, now).Access
		if use.Use == config.EnvSource {
			need = policy.View
		}
		if got >= need {
			continue
		}
		where := "environment " + use.Environment
		if use.Environment == policy.NoEnvironment {
			where = "this script"
		}
		if got == policy.View {
			return fmt.Errorf("you have view-only access to %s", where)
		}
		return fmt.Errorf("you have no access to %s", where)
	}
	return nil
}

// CanSeeRun reports whether u may see a run of s started by owner in envs: the owner, devops,
// or anyone with view access to all of its environments. s is nil when the script is gone.
func (a *Authorizer) CanSeeRun(u auth.User, owner string, s *config.Script, envs []string) bool {
	if strings.EqualFold(owner, u.Name) || a.IsAdmin(u) {
		return true
	}
	if s == nil {
		return false
	}
	if len(envs) == 0 {
		envs = []string{policy.NoEnvironment}
	}
	p, sub, now := a.Policy(), a.Subject(u), a.now()
	for _, env := range envs {
		if p.Access(sub, s, env, now).Access < policy.View {
			return false
		}
	}
	return true
}

// CanControlRun reports whether u may send input to, stop or kill a run started by owner.
func (a *Authorizer) CanControlRun(u auth.User, owner string) bool {
	return strings.EqualFold(owner, u.Name) || a.IsAdmin(u)
}
