package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/config"
	"github.com/catalinmuraru/runwarden/internal/remote"
)

func TestExecutorConfigErrors(t *testing.T) {
	t.Setenv("EXECUTOR_SERVER_URL", "")
	t.Setenv("EXECUTOR_TOKEN", "")
	var stderr bytes.Buffer
	if code := runExecutor([]string{"--environment", "prod"}, &stderr); code != 1 {
		t.Errorf("missing server URL and token: exit %d", code)
	}
	if code := runExecutor([]string{"--bogus"}, &stderr); code != 2 {
		t.Errorf("unknown flag: exit %d", code)
	}
}

func TestExecutorProbes(t *testing.T) {
	mux := newExecutorHealthMux(&remote.Agent{})
	for path, want := range map[string]int{"/healthz": http.StatusOK, "/readyz": http.StatusServiceUnavailable} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, nil))
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", path, rec.Code, want)
		}
	}
}

func TestNewHub(t *testing.T) {
	sec, kc := t.TempDir(), t.TempDir()
	cfg := config.App{SecretsDir: sec, KubeDir: kc}
	if hub, err := newHub(cfg); hub != nil || err != nil {
		t.Fatalf("no EXECUTORS: %v, %v", hub, err)
	}
	cfg.ExecutorTokens = map[string]string{"prod": remote.HashToken("t")}
	hub, err := newHub(cfg)
	if err != nil || !hub.Remote("prod") || hub.Remote("dev") {
		t.Fatalf("hub: %v", err)
	}
	if got := describeExecutors(hub); got != "executors prod: down (0 connected)\n" {
		t.Errorf("readiness line = %q", got)
	}
	// The environment's secrets or kubeconfig mounted on the server: refused.
	for _, dir := range []string{sec, kc} {
		if err := os.MkdirAll(filepath.Join(dir, "prod"), 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := newHub(cfg); err == nil || !strings.Contains(err.Error(), filepath.Join(dir, "prod")) {
			t.Errorf("mounted %s: %v", dir, err)
		}
		_ = os.RemoveAll(filepath.Join(dir, "prod"))
	}
}
