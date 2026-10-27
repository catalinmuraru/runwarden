//go:build unix

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/remote"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

const executorToken = "production-executor-token" //nolint:gosec // test value

// connectExecutor starts an executor of production against e and waits until it is registered.
// Its own secrets differ from the server's, and it exports EXECUTOR_ENV_* to scripts.
func connectExecutor(t *testing.T, e *testEnv) *remote.Agent {
	t.Helper()
	sec := t.TempDir()
	for env, name := range map[string]string{"production": "DB_PASSWORD", secrets.Common: "PRIVATE_TOKEN"} {
		if err := os.MkdirAll(filepath.Join(sec, env), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sec, env, name), []byte("executor-value"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	a := &remote.Agent{
		URL: e.ts.URL, Token: executorToken, Environment: "production", Pod: "production-0", Version: "test",
		Exec:      executor.New(executor.Limits{}, executor.EnvPolicy{TempRoot: t.TempDir(), PrivateHome: true, Prefix: "EXECUTOR_ENV_"}),
		Capacity:  10,
		Secrets:   secrets.Store{Dir: sec},
		UploadDir: t.TempDir(),
		Heartbeat: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = a.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(testWait)
	for e.srv.opts.Executors.Pick("production") == nil {
		if time.Now().After(deadline) {
			t.Fatal("executor did not register")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return a
}

func TestExecutorEnvironment(t *testing.T) {
	hub, err := remote.NewHub(remote.HubOptions{Tokens: map[string]string{"production": remote.HashToken(executorToken)}})
	if err != nil {
		t.Fatal(err)
	}
	count := filepath.Join(t.TempDir(), "calls")
	t.Setenv("EXECUTOR_ENV_COUNT_FILE", count) // only the executor exports it to helpers
	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	alice := setup(t, executor.Limits{MaxPerUser: 1}, Options{Executors: hub, Activity: acts, ValuesCacheTTL: time.Minute,
		Secrets: secrets.Store{Dir: secretsDir(t)}})
	root := alice.as("root", "")

	// No executor connected: runs and helpers of production are refused, development runs here.
	code, body := alice.do(http.MethodPost, "/api/scripts/Echo/runs", map[string]any{"values": map[string]any{"Name": "abc", "Env": "prod"}})
	if code != http.StatusServiceUnavailable || !strings.Contains(string(body), "no executor is connected for environment production") {
		t.Errorf("start without executor = %d %s", code, body)
	}
	if code, body := alice.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=prod", nil); code != http.StatusServiceUnavailable {
		t.Errorf("helper without executor = %d %s", code, body)
	}
	alice.finish(alice.start("Echo", map[string]any{"Name": "abc", "Env": "dev"}))
	code, body = root.do(http.MethodGet, "/api/admin/secrets", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"executors":[{"environment":"production","pods":[]`) {
		t.Errorf("admin without executor = %d %s", code, body)
	}

	connectExecutor(t, alice)

	// A run of production executes on the executor, with an uploaded file and a masked secure value.
	r, ct := multipartBody(t, map[string]any{"Name": "abc", "Env": "prod"}, "Upload", "notes.txt", []byte("hello upload"))
	code, body = alice.do(http.MethodPost, "/api/scripts/Echo/runs", r, "Content-Type", ct)
	if code != http.StatusCreated {
		t.Fatalf("start on executor = %d %s", code, body)
	}
	var started struct{ ID string }
	_ = json.Unmarshal(body, &started)
	meta := alice.finish(started.ID)
	out := alice.output(started.ID)
	for _, want := range []string{"[--name]\n[abc]", "content=hello upload", "user=alice"} {
		if !strings.Contains(out, want) {
			t.Errorf("output misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "default-secret") || meta.Status != string(executor.StatusFinished) ||
		strings.Join(meta.Environments, ",") != "production" {
		t.Errorf("run on executor: %+v\n%s", meta, out)
	}

	// Helpers of production run on the executor too, and are cached per environment.
	for range 2 {
		code, body = alice.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=prod", nil)
		if code != http.StatusOK || !strings.Contains(string(body), "t-prod-2") {
			t.Errorf("helper on executor = %d %s", code, body)
		}
	}
	if calls, _ := os.ReadFile(count); string(calls) != "call\n" { //nolint:gosec // test temp file
		t.Errorf("helper calls on the executor = %q, want one (then cached)", calls)
	}

	// Copy uses two environments: never on an executor, which holds only its own credentials.
	code, body = alice.do(http.MethodPost, "/api/scripts/Copy/runs", map[string]any{"values": map[string]any{"From": "dev", "To": "prod"}})
	if code != http.StatusConflict || !strings.Contains(string(body), "cannot use a second environment") {
		t.Errorf("two environments with an executor = %d %s", code, body)
	}

	// The Admin page lists the connected pod and the secret names it reports instead of the server's.
	code, body = root.do(http.MethodGet, "/api/admin/secrets", nil)
	if code != http.StatusOK || strings.Contains(string(body), "executor-value") {
		t.Fatalf("admin secrets = %d %s", code, body)
	}
	var page struct {
		Available map[string][]string   `json:"available"`
		Scripts   []secretsScript       `json:"scripts"`
		Executors []executorEnvironment `json:"executors"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Executors) != 1 || len(page.Executors[0].Pods) != 1 || page.Executors[0].Pods[0].Pod != "production-0" ||
		page.Executors[0].Pods[0].Capacity != 10 || len(page.Executors[0].Secrets) != 2 {
		t.Errorf("executors = %+v", page.Executors)
	}
	if strings.Join(page.Available["production"], ",") != "DB_PASSWORD" || len(page.Scripts[0].Missing["production"]) != 0 {
		t.Errorf("production secrets = %v, missing %v", page.Available["production"], page.Scripts[0].Missing)
	}
}
