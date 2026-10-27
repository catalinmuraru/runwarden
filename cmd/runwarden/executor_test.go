package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

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
