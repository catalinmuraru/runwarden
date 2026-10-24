package config

import (
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
	app, err := LoadApp(env(map[string]string{"AUTH_TYPE": "none"}))
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

const testKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=" // 32 bytes

func TestLoadAuth(t *testing.T) {
	tests := []struct {
		name    string
		vars    map[string]string
		wantErr []string
		check   func(t *testing.T, a Auth)
	}{
		{name: "missing type", vars: nil, wantErr: []string{"AUTH_TYPE is required"}},
		{name: "unknown type", vars: map[string]string{"AUTH_TYPE": "kerberos"}, wantErr: []string{"unknown value"}},
		{
			name:    "oidc missing settings",
			vars:    map[string]string{"AUTH_TYPE": "oidc"},
			wantErr: []string{"SESSION_KEY", "PUBLIC_URL", "OIDC_ISSUER_URL", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET"},
		},
		{
			name:    "bad session key",
			vars:    map[string]string{"AUTH_TYPE": "ldap", "SESSION_KEY": "c2hvcnQ=", "LDAP_URL": "ldap://x", "LDAP_USER_BASE_DN": "dc=x"},
			wantErr: []string{"SESSION_KEY must be 32"},
		},
		{
			name:    "bad ldap settings",
			vars:    map[string]string{"AUTH_TYPE": "ldap", "SESSION_KEY": testKey, "LDAP_URL": "http://x", "LDAP_USER_BASE_DN": "dc=x", "LDAP_USER_FILTER": "(uid=fixed)"},
			wantErr: []string{"LDAP_URL must start", "LDAP_USER_FILTER must contain"},
		},
		{
			name: "oidc",
			vars: map[string]string{
				"AUTH_TYPE": "OIDC", "SESSION_KEY": testKey, "PUBLIC_URL": "https://rw.example.com/",
				"OIDC_ISSUER_URL": "https://idp", "OIDC_CLIENT_ID": "rw", "OIDC_CLIENT_SECRET": "s",
				"OIDC_SCOPES": "groups openid", "ADMIN_GROUPS": "admins, ops", "SESSION_TTL": "1h",
			},
			check: func(t *testing.T, a Auth) {
				if a.Type != AuthOIDC || a.PublicURL != "https://rw.example.com" || !a.CookieSecure || len(a.SessionKey) != 32 ||
					a.SessionTTL != time.Hour || !slices.Equal(a.AdminGroups, []string{"admins", "ops"}) ||
					!slices.Equal(a.OIDC.Scopes, []string{"openid", "profile", "email", "groups"}) ||
					a.OIDC.UsernameClaim != "preferred_username" || !slices.Equal(a.OIDC.GroupsClaims, []string{"groups"}) {
					t.Errorf("auth = %+v", a)
				}
			},
		},
		{
			name: "gitlab over http",
			vars: map[string]string{
				"AUTH_TYPE": "gitlab", "SESSION_KEY": testKey, "PUBLIC_URL": "http://localhost:8080",
				"GITLAB_URL": "https://gitlab.example.com/", "GITLAB_CLIENT_ID": "rw", "GITLAB_CLIENT_SECRET": "s",
			},
			check: func(t *testing.T, a Auth) {
				if a.CookieSecure || a.OIDC.IssuerURL != "https://gitlab.example.com" || a.OIDC.UsernameClaim != "nickname" ||
					!slices.Equal(a.OIDC.GroupsClaims, []string{"groups", "groups_direct"}) {
					t.Errorf("auth = %+v", a)
				}
			},
		},
		{
			name: "ldap defaults",
			vars: map[string]string{"AUTH_TYPE": "ldap", "SESSION_KEY": testKey, "LDAP_URL": "ldaps://dir", "LDAP_USER_BASE_DN": "ou=people,dc=x"},
			check: func(t *testing.T, a Auth) {
				if a.LDAP.UserFilter != "(uid={username})" || a.LDAP.GroupFilter != "(member={dn})" || a.LDAP.GroupNameAttr != "cn" {
					t.Errorf("ldap = %+v", a.LDAP)
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
