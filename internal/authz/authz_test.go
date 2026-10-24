package authz

import (
	"testing"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/config"
)

func TestPolicy(t *testing.T) {
	p := Policy{AdminUsers: []string{"Root"}, AdminGroups: []string{"admins"}}
	open := &config.Script{Name: "open"}                               // no allowed_users
	closed := &config.Script{Name: "closed", AllowedUsers: []string{}} // explicitly nobody
	star := &config.Script{Name: "star", AllowedUsers: []string{"*"}}  // any signed-in user
	team := &config.Script{Name: "team", AllowedUsers: []string{"@Ops", " bob "}, AdminUsers: []string{"carol", "@leads"}}

	alice := auth.User{Name: "alice", Groups: []string{"ops"}}
	bob := auth.User{Name: "BOB"}
	carol := auth.User{Name: "carol"}
	lead := auth.User{Name: "dave", Groups: []string{"LEADS"}}
	root := auth.User{Name: "root"}
	admin := auth.User{Name: "erin", Groups: []string{"admins"}}
	mallory := auth.User{Name: "mallory", Groups: []string{"ops-not"}}

	run := []struct {
		user   auth.User
		script *config.Script
		want   bool
	}{
		{mallory, open, true},
		{mallory, star, true},
		{mallory, closed, false},
		{root, closed, true},
		{alice, team, true},  // group, case-insensitive
		{bob, team, true},    // name, case-insensitive and trimmed
		{carol, team, false}, // admin_users alone doesn't grant running
		{mallory, team, false},
		{admin, team, true}, // global admin group
	}
	for _, tt := range run {
		if got := p.CanRun(tt.user, tt.script); got != tt.want {
			t.Errorf("CanRun(%s, %s) = %v, want %v", tt.user.Name, tt.script.Name, got, tt.want)
		}
	}

	scriptAdmin := []struct {
		user   auth.User
		script *config.Script
		want   bool
	}{
		{carol, team, true},
		{lead, team, true},
		{alice, team, false},
		{alice, open, false}, // no admin_users: global admins only
		{root, open, true},
		{carol, nil, false},
		{root, nil, true},
	}
	for _, tt := range scriptAdmin {
		if got := p.IsScriptAdmin(tt.user, tt.script); got != tt.want {
			t.Errorf("IsScriptAdmin(%s, %v) = %v, want %v", tt.user.Name, tt.script, got, tt.want)
		}
	}

	if !p.CanSeeRun(alice, "Alice", team) || p.CanSeeRun(bob, "alice", team) || !p.CanSeeRun(carol, "alice", team) {
		t.Error("CanSeeRun: owner, other user and script admin")
	}
	if !p.CanSeeRun(alice, "alice", nil) || p.CanSeeRun(carol, "alice", nil) {
		t.Error("CanSeeRun for a removed script: only the owner and global admins")
	}
	if !(Policy{Everyone: true}).IsAdmin(mallory) {
		t.Error("Everyone policy must make every user an admin")
	}
}
