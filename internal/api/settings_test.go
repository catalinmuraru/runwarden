package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/settings"
)

func TestUISettings(t *testing.T) {
	var (
		mu     sync.Mutex
		events []audit.Record
	)
	audit.SetSink(func(r audit.Record) { mu.Lock(); events = append(events, r); mu.Unlock() })
	t.Cleanup(func() { audit.SetSink(nil) })

	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store, err := settings.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	alice := setup(t, executor.Limits{}, Options{Activity: acts, Settings: store})
	root := alice.as("root", "")

	// The theme is public: no session needed.
	publicTheme := func() string {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, alice.ts.URL+"/api/settings/ui", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("public GET = %d %s", resp.StatusCode, body)
		}
		var v uiSettings
		if err := json.Unmarshal(body, &v); err != nil {
			t.Fatal(err)
		}
		return v.Theme
	}
	if got := publicTheme(); got != settings.ThemeSystem {
		t.Errorf("default theme = %q", got)
	}

	if code, _ := alice.do(http.MethodPut, "/api/admin/settings/ui", map[string]any{"theme": "dark"}); code != http.StatusNotFound {
		t.Errorf("non-admin PUT = %d, want 404", code)
	}
	if code, body := root.do(http.MethodPut, "/api/admin/settings/ui", map[string]any{"theme": "purple"}); code != http.StatusBadRequest || !strings.Contains(string(body), "system, light or dark") {
		t.Errorf("bad theme = %d %s", code, body)
	}
	if code, body := root.do(http.MethodPut, "/api/admin/settings/ui", map[string]any{"theme": "dark"}); code != http.StatusOK || !strings.Contains(string(body), `"theme":"dark"`) {
		t.Errorf("admin PUT = %d %s", code, body)
	}
	if got := publicTheme(); got != settings.ThemeDark {
		t.Errorf("theme after PUT = %q", got)
	}

	mu.Lock()
	defer mu.Unlock()
	var changes []audit.Record
	for _, e := range events {
		if e.Action == audit.SettingsChange {
			changes = append(changes, e)
		}
	}
	if len(changes) != 1 || changes[0].User != "root" || changes[0].Message != "settings: theme: system → dark" {
		t.Errorf("audit events = %+v", changes)
	}
}

func TestUISettingsWithoutStore(t *testing.T) {
	root := policyEnv(t).as("root", "")
	if code, body := root.do(http.MethodGet, "/api/settings/ui", nil); code != http.StatusOK || !strings.Contains(string(body), `"theme":"system"`) {
		t.Errorf("GET = %d %s", code, body)
	}
	if code, _ := root.do(http.MethodPut, "/api/admin/settings/ui", map[string]any{"theme": "dark"}); code != http.StatusNotFound {
		t.Errorf("PUT without a store = %d, want 404", code)
	}
}
