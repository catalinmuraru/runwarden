package config

import (
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestLoadAppDefaults(t *testing.T) {
	app, err := LoadApp(env(map[string]string{"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true"}))
	if err != nil {
		t.Fatal(err)
	}
	wd, _ := os.Getwd()
	want := App{
		ListenAddr:        ":8080",
		RunnersDir:        filepath.Join(wd, "runners"),
		BaseDir:           wd,
		DataDir:           filepath.Join(wd, "data"),
		MaxConcurrentRuns: 10,
		RunTimeout:        60 * time.Minute,
		KillGrace:         10 * time.Second,
		MaxOutputBytes:    10 << 20,
		MaxUploadBytes:    100 << 20,
	}
	if !equalApp(app, want) {
		t.Errorf("got %+v\nwant %+v", app, want)
	}
}

func TestLoadAppValues(t *testing.T) {
	app, err := LoadApp(env(map[string]string{
		"AUTH_TYPE":           "none",
		"ALLOW_NO_AUTH":       "true",
		"LISTEN_ADDR":         "127.0.0.1:9000",
		"RUNNERS_DIR":         "/etc/runwarden/runners",
		"DATA_DIR":            "/var/lib/runwarden",
		"MAX_CONCURRENT_RUNS": "3",
		"RUN_TIMEOUT":         "2h",
		"KILL_GRACE":          "5s",
		"MAX_OUTPUT_BYTES":    "64Ki",
		"MAX_UPLOAD_BYTES":    "1Gi",
		"ALLOWED_ORIGINS":     " ops.example.com, *.example.org ,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if app.ListenAddr != "127.0.0.1:9000" || app.RunnersDir != "/etc/runwarden/runners" || app.DataDir != "/var/lib/runwarden" ||
		app.MaxConcurrentRuns != 3 || app.RunTimeout != 2*time.Hour || app.KillGrace != 5*time.Second ||
		app.MaxOutputBytes != 64<<10 || app.MaxUploadBytes != 1<<30 ||
		!slices.Equal(app.AllowedOrigins, []string{"ops.example.com", "*.example.org"}) {
		t.Errorf("got %+v", app)
	}
}

func TestLoadAppRetentionAndProxies(t *testing.T) {
	app, err := LoadApp(env(map[string]string{"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true"}))
	if err != nil || app.HistoryMaxAge != 90*24*time.Hour || app.HistoryMaxRuns != 0 || app.TrustedProxies != nil {
		t.Errorf("defaults: %+v %v", app, err)
	}
	app, err = LoadApp(env(map[string]string{
		"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true", "HISTORY_MAX_AGE": "30d", "HISTORY_MAX_RUNS": "500", "TRUSTED_PROXIES": "10.0.0.0/8, 192.168.0.1",
	}))
	if err != nil || app.HistoryMaxAge != 30*24*time.Hour || app.HistoryMaxRuns != 500 ||
		!slices.Equal(app.TrustedProxies, []string{"10.0.0.0/8", "192.168.0.1"}) {
		t.Errorf("values: %+v %v", app, err)
	}
	if app, _ := LoadApp(env(map[string]string{"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true", "HISTORY_MAX_AGE": "0"})); app.HistoryMaxAge != 0 {
		t.Errorf("0 should keep history forever: %v", app.HistoryMaxAge)
	}
	_, err = LoadApp(env(map[string]string{"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true", "HISTORY_MAX_AGE": "soon", "HISTORY_MAX_RUNS": "-1"}))
	if err == nil || !strings.Contains(err.Error(), "HISTORY_MAX_AGE") || !strings.Contains(err.Error(), "HISTORY_MAX_RUNS") {
		t.Errorf("invalid values: %v", err)
	}
}

func TestLoadAppInvalid(t *testing.T) {
	_, err := LoadApp(env(map[string]string{
		"MAX_CONCURRENT_RUNS": "many",
		"RUN_TIMEOUT":         "-1m",
		"MAX_OUTPUT_BYTES":    "10MB",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{"MAX_CONCURRENT_RUNS", "RUN_TIMEOUT", "MAX_OUTPUT_BYTES"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

func TestIsolationSettings(t *testing.T) {
	base := map[string]string{"AUTH_METHODS": "none", "ALLOW_NO_AUTH": "true"}
	app, err := LoadApp(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if app.Isolation != IsolationAuto || app.WorkspaceDir != "" || app.RunMaxProcs != 0 ||
		app.SecretsDir != "/runwarden/secrets" || app.KubeDir != "/runwarden/kube" {
		t.Errorf("defaults: %+v", app)
	}
	set := maps.Clone(base)
	maps.Copy(set, map[string]string{
		"ISOLATION": "Required", "WORKSPACE_DIR": "/work", "RUN_MAX_PROCS": "512",
		"ISOLATION_READ_PATHS": "/opt/tools, /srv/ref/", "ISOLATION_WRITE_PATHS": "/dev/shm",
	})
	if app, err = LoadApp(env(set)); err != nil {
		t.Fatal(err)
	}
	if app.Isolation != IsolationRequired || app.WorkspaceDir != "/work" || app.RunMaxProcs != 512 ||
		!slices.Equal(app.IsolationRead, []string{"/opt/tools", "/srv/ref"}) || !slices.Equal(app.IsolationWrite, []string{"/dev/shm"}) {
		t.Errorf("set: %+v", app)
	}
	bad := maps.Clone(base)
	maps.Copy(bad, map[string]string{"ISOLATION": "maybe", "ISOLATION_READ_PATHS": "relative/dir", "RUN_MAX_PROCS": "-1"})
	_, err = LoadApp(env(bad))
	for _, name := range []string{"ISOLATION:", "ISOLATION_READ_PATHS", "RUN_MAX_PROCS"} {
		if err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("error does not mention %s: %v", name, err)
		}
	}
}

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func TestLoadAuth(t *testing.T) {
	gitlab := map[string]string{"GITLAB_URL": "https://gitlab.example.com/", "GITLAB_CLIENT_ID": "rw", "GITLAB_CLIENT_SECRET": "s"}
	oidc := map[string]string{"OIDC_ISSUER_URL": "https://idp", "OIDC_CLIENT_ID": "rw", "OIDC_CLIENT_SECRET": "s"}
	ldap := map[string]string{"LDAP_URL": "ldaps://dir", "LDAP_USER_BASE_DN": "ou=people,dc=x"}
	with := func(maps ...map[string]string) map[string]string {
		out := map[string]string{"SESSION_KEY": testKey, "PUBLIC_URL": "https://rw.example.com/"}
		for _, m := range maps {
			for k, v := range m {
				out[k] = v
			}
		}
		return out
	}
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr []string
		check   func(t *testing.T, a Auth)
	}{
		{name: "missing methods", vars: nil, wantErr: []string{"AUTH_METHODS is required"}},
		{name: "unknown method", vars: map[string]string{"AUTH_METHODS": "kerberos"}, wantErr: []string{`unknown method "kerberos"`}},
		{name: "duplicate", vars: with(gitlab, map[string]string{"AUTH_METHODS": "gitlab,gitlab"}), wantErr: []string{"listed twice"}},
		{name: "none with others", vars: map[string]string{"AUTH_METHODS": "none,ldap", "ALLOW_NO_AUTH": "true"}, wantErr: []string{"cannot be combined"}},
		{name: "none with admin", vars: map[string]string{"AUTH_METHODS": "none", "ALLOW_NO_AUTH": "true", "INITIAL_ADMIN": "true"}, wantErr: []string{"cannot be combined"}},
		{
			name:    "oidc missing settings",
			vars:    map[string]string{"AUTH_METHODS": "oidc"},
			wantErr: []string{"SESSION_KEY", "PUBLIC_URL", "OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET"},
		},
		{
			name:    "bad session key",
			vars:    map[string]string{"AUTH_METHODS": "ldap", "SESSION_KEY": "c2hvcnQ=", "LDAP_URL": "ldaps://x", "LDAP_USER_BASE_DN": "dc=x"},
			wantErr: []string{"SESSION_KEY must be 32"},
		},
		{
			name:    "bad ldap settings",
			vars:    with(map[string]string{"AUTH_METHODS": "ldap", "LDAP_URL": "http://x", "LDAP_USER_BASE_DN": "dc=x", "LDAP_USER_FILTER": "(uid=fixed)"}),
			wantErr: []string{"LDAP_URL must start", "LDAP_USER_FILTER must contain"},
		},
		{
			name: "several methods, in order",
			vars: with(gitlab, oidc, ldap, map[string]string{"AUTH_METHODS": " GitLab, ldap ,oidc", "OIDC_SCOPES": "groups openid",
				"SESSION_TTL": "1h", "GITLAB_REQUIRED_GROUP": "/company/runwarden/", "OIDC_LABEL": "Keycloak"}),
			check: func(t *testing.T, a Auth) {
				if !slices.Equal(a.Methods, []string{"gitlab", "ldap", "oidc"}) || a.NoAuth || a.InitialAdmin ||
					a.PublicURL != "https://rw.example.com" || !a.CookieSecure || len(a.SessionKey) != 32 || a.SessionTTL != time.Hour ||
					len(a.Warnings) != 0 {
					t.Errorf("auth = %+v", a)
				}
				if !slices.Equal(a.OIDC.Scopes, []string{"openid", "profile", "email", "groups"}) || a.OIDC.UsernameClaim != "preferred_username" ||
					!slices.Equal(a.OIDC.GroupsClaims, []string{"groups"}) || a.OIDC.Label != "Keycloak" || a.OIDC.RequiredGroup != "" {
					t.Errorf("oidc = %+v", a.OIDC)
				}
				if a.GitLab.IssuerURL != "https://gitlab.example.com" || a.GitLab.UsernameClaim != "nickname" || a.GitLab.Label != "GitLab" ||
					!slices.Equal(a.GitLab.GroupsClaims, []string{"groups", "groups_direct"}) || a.GitLab.RequiredGroup != "company/runwarden" {
					t.Errorf("gitlab = %+v", a.GitLab)
				}
				if a.LDAP.UserFilter != "(uid={username})" || a.LDAP.GroupFilter != "(member={dn})" || a.LDAP.GroupNameAttr != "cn" || a.LDAP.Label != "Directory" {
					t.Errorf("ldap = %+v", a.LDAP)
				}
			},
		},
		{
			name: "deprecated AUTH_TYPE over http",
			vars: with(gitlab, map[string]string{"AUTH_TYPE": "gitlab", "PUBLIC_URL": "http://localhost:8080"}),
			check: func(t *testing.T, a Auth) {
				if !slices.Equal(a.Methods, []string{"gitlab"}) || a.CookieSecure || len(a.Warnings) != 1 {
					t.Errorf("auth = %+v", a)
				}
			},
		},
		{
			name: "built-in admin only (no PUBLIC_URL needed)",
			vars: map[string]string{"INITIAL_ADMIN": "true", "SESSION_KEY": testKey},
			check: func(t *testing.T, a Auth) {
				if len(a.Methods) != 0 || !a.InitialAdmin {
					t.Errorf("auth = %+v", a)
				}
			},
		},
		{
			name: "none",
			vars: map[string]string{"AUTH_METHODS": "none", "ALLOW_NO_AUTH": "true"},
			check: func(t *testing.T, a Auth) {
				if !a.NoAuth || len(a.Methods) != 0 {
					t.Errorf("auth = %+v", a)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := LoadApp(env(tt.vars))
			for _, want := range tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("error %v does not mention %q", err, want)
				}
			}
			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, app.Auth)
			}
		})
	}
}

func equalApp(a, b App) bool {
	return a.ListenAddr == b.ListenAddr && a.RunnersDir == b.RunnersDir && a.BaseDir == b.BaseDir &&
		a.DataDir == b.DataDir && a.MaxConcurrentRuns == b.MaxConcurrentRuns && a.RunTimeout == b.RunTimeout &&
		a.KillGrace == b.KillGrace && a.MaxOutputBytes == b.MaxOutputBytes && a.MaxUploadBytes == b.MaxUploadBytes &&
		slices.Equal(a.AllowedOrigins, b.AllowedOrigins)
}

func TestExpandCommand(t *testing.T) {
	lookup := func(vals map[string]string) func(string) (string, bool) {
		return func(n string) (string, bool) { v, ok := vals[n]; return v, ok }
	}
	got, err := ExpandCommand(`list.sh ${Env} "prefix-${Env}" ${Name}`, lookup(map[string]string{
		"Env": "prod", "Name": "two words; rm -rf /",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"list.sh", "prod", "prefix-prod", "two words; rm -rf /"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, err := ExpandCommand("list.sh ${Missing}", lookup(nil)); err == nil || !strings.Contains(err.Error(), "Missing") {
		t.Errorf("missing value error = %v", err)
	}
	if got := Placeholders("a ${X} ${Y} ${X}"); !slices.Equal(got, []string{"X", "Y"}) {
		t.Errorf("placeholders = %q", got)
	}
}

func TestHardeningSettings(t *testing.T) {
	base := map[string]string{"AUTH_TYPE": "none", "ALLOW_NO_AUTH": "true"}
	with := func(extra map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range base {
			m[k] = v
		}
		for k, v := range extra {
			m[k] = v
		}
		return m
	}

	app, err := LoadApp(env(base))
	if err != nil {
		t.Fatal(err)
	}
	if app.HealthAddr != ":8081" || app.MaxRunsPerUser != 1 || app.RunHome != RunHomePerRun || app.ValuesMaxPerUser != 2 ||
		app.ValuesMaxGlobal != 10 || app.ValuesCacheTTL != time.Minute || app.ActivityMaxDay != 50<<20 ||
		app.Auth.MaxFailuresPerIP != 20 || app.Auth.MaxFailuresGlobal != 100 {
		t.Errorf("defaults = %+v", app)
	}

	skel := t.TempDir()
	app, err = LoadApp(env(with(map[string]string{"MAX_RUNS_PER_USER": "0", "VALUES_CACHE_TTL": "0", "RUN_HOME": "Shared",
		"HOME_SKELETON": skel, "LOGIN_MAX_FAILURES_PER_IP": "5", "HEALTH_ADDR": "127.0.0.1:9001"})))
	if err != nil {
		t.Fatal(err)
	}
	if app.MaxRunsPerUser != 0 || app.ValuesCacheTTL != 0 || app.RunHome != RunHomeShared || app.HomeSkeleton != skel ||
		app.Auth.MaxFailuresPerIP != 5 || app.HealthAddr != "127.0.0.1:9001" {
		t.Errorf("values = %+v", app)
	}

	_, err = LoadApp(env(with(map[string]string{"RUN_HOME": "elsewhere", "HOME_SKELETON": filepath.Join(skel, "nope"),
		"MAX_RUNS_PER_USER": "-1", "TRUSTED_PROXIES": "10.0.0.0/8,0.0.0.0/0,::/0,fd00::/16,nonsense"})))
	for _, want := range []string{"RUN_HOME", "HOME_SKELETON", "MAX_RUNS_PER_USER", `"0.0.0.0/0" is too wide`, `"::/0" is too wide`,
		`"fd00::/16" is too wide`, `"nonsense" is not`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error %v does not mention %q", err, want)
		}
	}
	if err != nil && strings.Contains(err.Error(), `"10.0.0.0/8"`) {
		t.Errorf("10.0.0.0/8 must be accepted: %v", err)
	}
}

func TestConfigTraps(t *testing.T) {
	if _, err := LoadApp(env(map[string]string{"AUTH_TYPE": "none"})); err == nil || !strings.Contains(err.Error(), "ALLOW_NO_AUTH") {
		t.Errorf("AUTH_TYPE=none without ALLOW_NO_AUTH: %v", err)
	}
	ldap := map[string]string{"AUTH_METHODS": "ldap", "SESSION_KEY": testKey, "LDAP_URL": "ldap://dir", "LDAP_USER_BASE_DN": "dc=x"}
	if _, err := LoadApp(env(ldap)); err == nil || !strings.Contains(err.Error(), "clear text") {
		t.Errorf("plain ldap:// accepted: %v", err)
	}
	for _, extra := range []map[string]string{{"LDAP_START_TLS": "true"}, {"LDAP_INSECURE": "true"}} {
		vars := map[string]string{}
		for k, v := range ldap {
			vars[k] = v
		}
		for k, v := range extra {
			vars[k] = v
		}
		if _, err := LoadApp(env(vars)); err != nil {
			t.Errorf("%v: %v", extra, err)
		}
	}
}

func TestAccessSettings(t *testing.T) {
	base := map[string]string{"AUTH_METHODS": "none", "ALLOW_NO_AUTH": "true"}
	app, err := LoadApp(env(base))
	if err != nil {
		t.Fatal(err)
	}
	a := app.Access
	if len(a.Environments) != 3 || a.Environments[2].Name != "production" || a.MaxExceptionDays != 90 || len(a.Warnings) != 0 {
		t.Errorf("defaults = %+v", a)
	}

	vars := map[string]string{"BOOTSTRAP_DEVOPS": "ann, bob", "ADMIN_USERS": "BOB,carl", "ADMIN_GROUPS": "admins",
		"POLICY_ENVIRONMENTS": `[{"name":"dev","match":["d-*"]},{"name":"prod"}]`, "POLICY_UNKNOWN_ENVIRONMENT": "dev", "POLICY_MAX_EXCEPTION_DAYS": "7"}
	for k, v := range base {
		vars[k] = v
	}
	app, err = LoadApp(env(vars))
	if err != nil {
		t.Fatal(err)
	}
	a = app.Access
	if len(a.Warnings) != 3 || !strings.Contains(a.Warnings[0], "built-in administrator") || a.MaxExceptionDays != 7 ||
		len(a.Environments) != 2 || a.Environments[0].Match[0] != "d-*" || a.UnknownEnvironment != "dev" {
		t.Errorf("access = %+v", a)
	}

	vars["POLICY_ENVIRONMENTS"] = "dev,prod"
	if _, err := LoadApp(env(vars)); err == nil || !strings.Contains(err.Error(), "POLICY_ENVIRONMENTS") {
		t.Errorf("non-JSON environments: %v", err)
	}
}
