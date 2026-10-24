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
	app, err := LoadApp(env(nil))
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
