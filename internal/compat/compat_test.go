package compat

import (
	"bytes"
	"log/slog"
	"maps"
	"strings"
	"testing"
)

// captureLog swaps the default logger for the test and resets the warn-once state.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	warned.Clear()
	t.Cleanup(func() { slog.SetDefault(prev); warned.Clear() })
	return &buf
}

func lookupIn(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := env[k]; return v, ok }
}

func TestLookupEnv(t *testing.T) {
	buf := captureLog(t)
	cases := []struct {
		env    map[string]string
		want   string
		wantOK bool
	}{
		{map[string]string{"WARD_X": "new", "RUNWARDEN_X": "old"}, "new", true},
		{map[string]string{"RUNWARDEN_X": "old"}, "old", true},
		{map[string]string{"WARD_X": ""}, "", true},
		{map[string]string{}, "", false},
	}
	for _, c := range cases {
		if v, ok := LookupEnv(lookupIn(c.env), "WARD_X"); v != c.want || ok != c.wantOK {
			t.Errorf("LookupEnv(%v) = %q, %v; want %q, %v", c.env, v, ok, c.want, c.wantOK)
		}
	}
	if n := strings.Count(buf.String(), "deprecated environment variable"); n != 1 {
		t.Errorf("warnings = %d, want 1:\n%s", n, buf)
	}
	if !strings.Contains(buf.String(), "old=RUNWARDEN_X new=WARD_X") {
		t.Errorf("warning does not name both variables:\n%s", buf)
	}
	// Twice more, still one warning.
	LookupEnv(lookupIn(map[string]string{"RUNWARDEN_X": "old"}), "WARD_X")
	if n := strings.Count(buf.String(), "deprecated"); n != 1 {
		t.Errorf("warned again: %d", n)
	}
	// A name without the prefix has no fallback.
	if _, ok := LookupEnv(lookupIn(map[string]string{"RUNWARDEN_PATH": "x"}), "PATH"); ok {
		t.Error("fallback for a non-WARD_ name")
	}
}

func TestGetenv(t *testing.T) {
	captureLog(t)
	t.Setenv("RUNWARDEN_TEST_GETENV", "old")
	if v := Getenv("WARD_TEST_GETENV"); v != "old" {
		t.Errorf("Getenv = %q", v)
	}
	t.Setenv("WARD_TEST_GETENV", "new")
	if v := Getenv("WARD_TEST_GETENV"); v != "new" {
		t.Errorf("Getenv = %q", v)
	}
}

func TestExportBoth(t *testing.T) {
	vars := map[string]string{
		"WARD_USER": "alice", "RUNWARDEN_KUBE_CONTEXT": "dev", // one of each direction
		"WARD_SHARED": "/new", "RUNWARDEN_SHARED": "/kept", // both set: unchanged
		"PATH": "/bin", "WARD_": "x", // not a ward variable / empty suffix
	}
	ExportBoth(vars)
	want := map[string]string{
		"WARD_USER": "alice", "RUNWARDEN_USER": "alice",
		"RUNWARDEN_KUBE_CONTEXT": "dev", "WARD_KUBE_CONTEXT": "dev",
		"WARD_SHARED": "/new", "RUNWARDEN_SHARED": "/kept",
		"PATH": "/bin", "WARD_": "x",
	}
	if !maps.Equal(vars, want) {
		t.Errorf("ExportBoth = %v\nwant %v", vars, want)
	}
}

func TestDir(t *testing.T) {
	buf := captureLog(t)
	exists := func(set ...string) func(string) bool {
		return func(p string) bool {
			for _, s := range set {
				if s == p {
					return true
				}
			}
			return false
		}
	}
	cases := []struct {
		name string
		have []string
		want string
	}{
		{"new only", []string{"/ward/kube"}, "/ward/kube"},
		{"both", []string{"/ward/kube", "/runwarden/kube"}, "/ward/kube"},
		{"neither", nil, "/ward/kube"},
		{"old only", []string{"/runwarden/kube"}, "/runwarden/kube"},
	}
	for _, c := range cases {
		if got := Dir("/ward/kube", "/runwarden/kube", exists(c.have...)); got != c.want {
			t.Errorf("%s: Dir = %q, want %q", c.name, got, c.want)
		}
	}
	if n := strings.Count(buf.String(), "deprecated default directory"); n != 1 {
		t.Errorf("warnings = %d, want 1:\n%s", n, buf)
	}
	if !DirExists(t.TempDir()) || DirExists(t.TempDir()+"/missing") {
		t.Error("DirExists")
	}
}

func TestHeader(t *testing.T) {
	buf := captureLog(t)
	get := func(h map[string]string) func(string) string { return func(k string) string { return h[k] } }
	const name = "X-Ward-Environment"
	if v := Header(get(map[string]string{name: "prod", LegacyEnvironmentHeader: "dev"}), name, LegacyEnvironmentHeader); v != "prod" {
		t.Errorf("new header: %q", v)
	}
	if buf.Len() != 0 {
		t.Errorf("warned for the new header: %s", buf)
	}
	if v := Header(get(map[string]string{LegacyEnvironmentHeader: "dev"}), name, LegacyEnvironmentHeader); v != "dev" {
		t.Errorf("legacy header: %q", v)
	}
	if !strings.Contains(buf.String(), "deprecated header") {
		t.Errorf("no warning: %s", buf)
	}
	if v := Header(get(nil), name, LegacyEnvironmentHeader); v != "" {
		t.Errorf("no header: %q", v)
	}
}
