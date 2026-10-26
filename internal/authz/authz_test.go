package authz

import (
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/policy"
)

func newAuthorizer(t *testing.T) *Authorizer {
	t.Helper()
	store, err := policy.Open(t.TempDir(), policy.Options{
		Environments: []policy.Environment{
			{Name: "development", Match: []string{"dev*"}},
			{Name: "acceptance", Match: []string{"acc*"}},
			{Name: "production", Match: []string{"prod*"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	_, _, err = store.Update(1, func(p *policy.Policy) error {
		p.Users = []policy.UserRoles{{Name: "dev", Roles: []string{policy.RoleDeveloper}}, {Name: "Boss", Roles: []string{policy.RoleDevops}}}
		p.Rules = []policy.Rule{
			{Role: policy.RoleDeveloper, Scripts: []string{"tag:developer"}, Environments: []string{"development"}, Access: policy.Run},
			{Role: policy.RoleDeveloper, Scripts: []string{"tag:developer"}, Environments: []string{"acceptance", "production"}, Access: policy.View},
			{Role: policy.RoleDeveloper, Scripts: []string{"Report"}, Environments: []string{policy.Any}, Access: policy.Run},
		}
		p.Exceptions = []policy.Exception{{User: "dev", Reason: "incident", Scripts: []string{"Copy"}, Environments: []string{"acceptance"},
			Access: policy.Run, Created: now, Expires: now.Add(time.Hour)}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Authorizer{Store: store}
}

var (
	copyDB = &config.Script{Name: "Copy", Tags: []string{"developer"},
		EnvironmentParameters: map[string]string{"From": config.EnvSource, "To": config.EnvTarget}}
	deploy = &config.Script{Name: "Deploy", Tags: []string{"developer"}, EnvironmentParameters: map[string]string{"Env": config.EnvTarget}}
	report = &config.Script{Name: "Report"} // no environment parameters
	keys   = &config.Script{Name: "Keys", Tags: []string{"devops"}}
)

func values(m map[string]string) func(string) []string {
	return func(name string) []string {
		if v, ok := m[name]; ok {
			return []string{v}
		}
		return nil
	}
}

func TestRolesAndAdmins(t *testing.T) {
	a := newAuthorizer(t)
	tests := []struct {
		user  auth.User
		admin bool
	}{
		{auth.User{Name: "dev"}, false},
		{auth.User{Name: "boss"}, true}, // devops in the policy, case-insensitive
		{auth.User{Name: auth.LocalAdminName, Method: auth.MethodLocal}, true},
		{auth.User{Name: auth.LocalAdminName, Method: "gitlab"}, false},         // only the built-in method
		{auth.User{Name: "guest", Groups: []string{"devops", "admins"}}, false}, // groups never grant rights
	}
	for _, tt := range tests {
		if got := a.IsAdmin(tt.user); got != tt.admin {
			t.Errorf("IsAdmin(%+v) = %v", tt.user, got)
		}
	}
	if !(&Authorizer{Everyone: true}).IsAdmin(auth.User{Name: "anyone"}) {
		t.Error("Everyone does not make an admin")
	}
	if (&Authorizer{}).CanSee(auth.User{Name: "x"}, report) {
		t.Error("without a policy a user sees scripts")
	}
}

func TestScriptAccess(t *testing.T) {
	a := newAuthorizer(t)
	dev, guest := auth.User{Name: "dev"}, auth.User{Name: "guest"}
	got := a.ScriptAccess(dev, deploy)
	if len(got) != 3 || got["development"] != policy.Run || got["production"] != policy.View {
		t.Errorf("deploy access = %v", got)
	}
	if got := a.ScriptAccess(dev, report); len(got) != 1 || got[policy.NoEnvironment] != policy.Run {
		t.Errorf("report access = %v", got)
	}
	if a.CanSee(dev, keys) || a.CanSee(guest, deploy) || !a.CanSee(dev, copyDB) {
		t.Error("CanSee wrong")
	}
}

func TestCheckStart(t *testing.T) {
	a := newAuthorizer(t)
	dev := auth.User{Name: "dev"}
	tests := []struct {
		name   string
		s      *config.Script
		values map[string]string
		want   string // "" means allowed
	}{
		{"run on development", deploy, map[string]string{"Env": "dev-1"}, ""},
		{"view only on production", deploy, map[string]string{"Env": "prod"}, "view-only access to environment production"},
		{"unknown value is the most sensitive", deploy, map[string]string{"Env": "staging"}, "view-only access to environment production"},
		{"copy from production to development", copyDB, map[string]string{"From": "prod", "To": "dev"}, ""},
		{"copy into production", copyDB, map[string]string{"From": "dev", "To": "prod"}, "view-only access to environment production"},
		{"exception: copy into acceptance", copyDB, map[string]string{"From": "dev", "To": "acc"}, ""},
		{"no environment selected", deploy, map[string]string{}, "no access to this script"},
		{"script without environments", report, nil, ""},
		{"not allowed at all", keys, nil, "no access to this script"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := a.CheckStart(dev, tt.s, a.Uses(tt.s, values(tt.values)))
			switch {
			case tt.want == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tt.want != "" && (err == nil || !strings.Contains(err.Error(), tt.want)):
				t.Errorf("err = %v, want %q", err, tt.want)
			}
		})
	}
	uses := a.Uses(copyDB, values(map[string]string{"From": "prod", "To": "dev"}))
	if envs := Environments(uses); len(envs) != 2 || envs[0] != "production" || envs[1] != "development" {
		t.Errorf("environments = %v (uses %+v)", envs, uses)
	}
	if envs := Environments(nil); len(envs) != 1 || envs[0] != policy.NoEnvironment {
		t.Errorf("no uses = %v", envs)
	}
}

func TestRuns(t *testing.T) {
	a := newAuthorizer(t)
	dev, other, boss := auth.User{Name: "dev"}, auth.User{Name: "other"}, auth.User{Name: "boss"}
	tests := []struct {
		name  string
		u     auth.User
		owner string
		s     *config.Script
		envs  []string
		want  bool
	}{
		{"owner", other, "OTHER", keys, nil, true},
		{"viewer of production", dev, "other", deploy, []string{"production"}, true},
		{"needs all environments", dev, "other", copyDB, []string{"production", "development"}, true},
		{"no access to the script", dev, "other", keys, nil, false},
		{"script gone", dev, "other", nil, nil, false},
		{"devops", boss, "other", nil, nil, true},
		{"guest", other, "dev", deploy, []string{"development"}, false},
	}
	for _, tt := range tests {
		if got := a.CanSeeRun(tt.u, tt.owner, tt.s, tt.envs); got != tt.want {
			t.Errorf("%s: CanSeeRun = %v", tt.name, got)
		}
	}
	if a.CanControlRun(dev, "other") || !a.CanControlRun(dev, "Dev") || !a.CanControlRun(boss, "dev") {
		t.Error("CanControlRun wrong")
	}
}
