// Package authz decides what a signed-in user may do, from runner configs (allowed_users,
// admin_users) and global admin settings. Names and groups match case-insensitively.
package authz

import (
	"strings"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/config"
)

// Policy holds the global settings.
type Policy struct {
	AdminUsers  []string
	AdminGroups []string
	// Everyone makes every user an admin (AUTH_TYPE=none, local development only).
	Everyone bool
}

// IsAdmin reports a global admin: may run every script and see and control every run.
func (p Policy) IsAdmin(u auth.User) bool {
	if p.Everyone {
		return true
	}
	if u.Method == auth.MethodLocal && u.Name == auth.LocalAdminName {
		return true // the built-in administrator (INITIAL_ADMIN)
	}
	return containsFold(p.AdminUsers, u.Name) || anyFold(p.AdminGroups, u.Groups)
}

// CanRun reports whether u may see and run s. Configs without allowed_users are open to every
// signed-in user; an empty list allows nobody but admins.
func (p Policy) CanRun(u auth.User, s *config.Script) bool {
	if s.AllowedUsers == nil || p.IsAdmin(u) {
		return true
	}
	return matches(u, s.AllowedUsers)
}

// IsScriptAdmin reports whether u may see and control every run of s. Without admin_users,
// only global admins can.
func (p Policy) IsScriptAdmin(u auth.User, s *config.Script) bool {
	if p.IsAdmin(u) {
		return true
	}
	return s != nil && matches(u, s.AdminUsers)
}

// CanSeeRun reports whether u may see and control a run started by owner. s may be nil when
// the script no longer exists.
func (p Policy) CanSeeRun(u auth.User, owner string, s *config.Script) bool {
	return strings.EqualFold(owner, u.Name) || p.IsScriptAdmin(u, s)
}

// matches checks entries of the form "*", "@group" or a user name.
func matches(u auth.User, entries []string) bool {
	for _, e := range entries {
		e = strings.TrimSpace(e)
		switch {
		case e == "*":
			return true
		case strings.HasPrefix(e, "@"):
			if containsFold(u.Groups, e[1:]) {
				return true
			}
		case strings.EqualFold(e, u.Name):
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

func anyFold(list, values []string) bool {
	for _, v := range values {
		if containsFold(list, v) {
			return true
		}
	}
	return false
}
