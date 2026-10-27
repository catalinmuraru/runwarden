//go:build unix

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

// secretsDir lays out secrets like mounted Kubernetes Secrets.
func secretsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	put := func(env, name, value string) {
		if err := os.MkdirAll(filepath.Join(dir, env), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, env, name), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	put("development", "DB_PASSWORD", "dev-pw")       // 6 characters
	put("production", "DB_PASSWORD", "production-pw") // 13 characters
	put("production", "OTHER_SECRET", "not-declared")
	put(secrets.Common, "PRIVATE_TOKEN", "gitlab-token")
	return dir
}

func TestRunSecrets(t *testing.T) {
	var (
		events []audit.Record
		mu     sync.Mutex
	)
	audit.SetSink(func(r audit.Record) { mu.Lock(); events = append(events, r); mu.Unlock() })
	t.Cleanup(func() { audit.SetSink(nil) })

	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := secretsDir(t)
	alice := setup(t, executor.Limits{}, Options{Activity: acts, Secrets: secrets.Store{Dir: dir}, Isolation: sandbox.Status{Mode: "auto", Active: true, ABI: 7, SignalScoping: true}}) // ops: runs Copy everywhere
	root := alice.as("root", "")

	// Copy production → development: the target's secret, the source's with SOURCE_, common ones,
	// nothing undeclared, and every value masked in the output.
	id := alice.start("Copy", map[string]any{"From": "prod", "To": "dev"})
	meta := alice.finish(id)
	out := alice.output(id)
	for _, want := range []string{"db=****** len=6", "source=****** len=13", "token=****** other=unset"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	for _, leaked := range []string{"dev-pw", "production-pw", "gitlab-token", "not-declared"} {
		if strings.Contains(out, leaked) {
			t.Errorf("output leaks %q:\n%s", leaked, out)
		}
	}
	if strings.Join(meta.Secrets, ",") != "DB_PASSWORD,PRIVATE_TOKEN,SOURCE_DB_PASSWORD" {
		t.Errorf("history secrets = %v", meta.Secrets)
	}

	// Rotation applies to the next run.
	if err := os.WriteFile(filepath.Join(dir, "development", "DB_PASSWORD"), []byte("rotated-dev-pw"), 0o600); err != nil {
		t.Fatal(err)
	}
	id = alice.start("Copy", map[string]any{"From": "dev", "To": "dev"})
	alice.finish(id)
	if out := alice.output(id); !strings.Contains(out, "db=****** len=14") {
		t.Errorf("rotated secret not used:\n%s", out)
	}

	// Helpers get the secrets of the environment their dependency selects; values are masked.
	code, body := alice.do(http.MethodGet, "/api/scripts/Copy/parameters/Check/values?To=prod", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"len-13"`) || strings.Contains(string(body), "production-pw") {
		t.Errorf("helper values = %d %s", code, body)
	}

	// A declared secret missing in the environment refuses the run (audited, no values).
	if err := os.Remove(filepath.Join(dir, "production", "DB_PASSWORD")); err != nil {
		t.Fatal(err)
	}
	code, body = alice.do(http.MethodPost, "/api/scripts/Copy/runs", map[string]any{"values": map[string]any{"From": "dev", "To": "prod"}})
	if code != http.StatusConflict || !strings.Contains(string(body), "secret DB_PASSWORD is not available in production") {
		t.Errorf("missing secret = %d %s", code, body)
	}
	mu.Lock()
	var refused, started *audit.Record
	for i := range events {
		e := &events[i]
		if e.Action == audit.RunStart && e.Script == "Copy" {
			if e.Outcome == "failure" {
				refused = e
			} else if started == nil {
				started = e
			}
		}
	}
	mu.Unlock()
	if refused == nil || !strings.Contains(refused.Reason, "DB_PASSWORD") {
		t.Errorf("refusal audit = %+v", refused)
	}
	if started == nil || strings.Join(started.Secrets, ",") != "DB_PASSWORD,PRIVATE_TOKEN,SOURCE_DB_PASSWORD" {
		t.Errorf("start audit = %+v", started)
	}

	// Scripts that declare nothing get nothing.
	echo := alice.start("Echo", map[string]any{"Name": "abc"})
	if m := alice.finish(echo); len(m.Secrets) != 0 {
		t.Errorf("Echo got secrets %v", m.Secrets)
	}

	// The Admin page shows names and gaps, never values; devops only.
	if code, _ := alice.do(http.MethodGet, "/api/admin/secrets", nil); code != http.StatusNotFound {
		t.Errorf("non-admin secrets page = %d", code)
	}
	code, body = root.do(http.MethodGet, "/api/admin/secrets", nil)
	if code != http.StatusOK || strings.Contains(string(body), "-pw") || strings.Contains(string(body), "gitlab-token") {
		t.Fatalf("admin secrets = %d %s", code, body)
	}
	var page struct {
		Available map[string][]string `json:"available"`
		Scripts   []secretsScript     `json:"scripts"`
		Isolation sandbox.Status      `json:"isolation"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Scripts) != 1 || strings.Join(page.Scripts[0].Missing["production"], ",") != "DB_PASSWORD" ||
		len(page.Scripts[0].Missing["development"]) != 0 || strings.Join(page.Available[secrets.Common], ",") != "PRIVATE_TOKEN" {
		t.Errorf("admin secrets page = %+v", page)
	}
	if !page.Isolation.Active || page.Isolation.ABI != 7 || !page.Isolation.SignalScoping {
		t.Errorf("admin secrets isolation = %+v", page.Isolation)
	}
}
