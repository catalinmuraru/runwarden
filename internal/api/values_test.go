//go:build unix

package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/executor"
)

func TestHelperLimits(t *testing.T) {
	h := newHelpers(2, 3, 0)
	for i := range 2 {
		if !h.acquire("alice") {
			t.Fatalf("alice's helper %d refused", i+1)
		}
	}
	if h.acquire("alice") {
		t.Error("third helper for alice allowed")
	}
	if !h.acquire("bob") {
		t.Error("bob refused while under the global limit")
	}
	if h.acquire("carol") {
		t.Error("global limit not applied")
	}
	h.release("alice")
	if !h.acquire("carol") {
		t.Error("released slot not reusable")
	}
}

func TestHelperCache(t *testing.T) {
	h := newHelpers(0, 0, time.Minute)
	now := time.Now()
	h.put("k", []byte("v"), now)
	if out, ok := h.get("k", now.Add(59*time.Second)); !ok || string(out) != "v" {
		t.Error("cached value missing")
	}
	if _, ok := h.get("k", now.Add(61*time.Second)); ok {
		t.Error("expired value returned")
	}
	if off := newHelpers(0, 0, 0); func() bool { off.put("k", []byte("v"), now); _, ok := off.get("k", now); return ok }() {
		t.Error("cache with TTL 0 must be disabled")
	}
}

func TestValuesCachedAcrossCalls(t *testing.T) {
	count := filepath.Join(t.TempDir(), "calls")
	t.Setenv("RUNNER_ENV_COUNT_FILE", count)
	e := setup(t, executor.Limits{}, Options{ValuesCacheTTL: time.Minute})
	for range 3 {
		if code, body := e.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=prod", nil); code != http.StatusOK {
			t.Fatalf("values = %d %s", code, body)
		}
	}
	e.do(http.MethodGet, "/api/scripts/Echo/parameters/Target/values?Env=dev", nil)
	data, _ := os.ReadFile(count) //nolint:gosec // test temp file
	if n := strings.Count(string(data), "call"); n != 2 {
		t.Errorf("helper ran %d times, want 2 (one per distinct environment)", n)
	}
}

func TestOneRunPerUser(t *testing.T) {
	e := setup(t, executor.Limits{MaxPerUser: 1, KillGrace: 200 * time.Millisecond}, Options{})
	first := e.start("Sleep", nil)
	code, body := e.do(http.MethodPost, "/api/scripts/Sleep/runs", map[string]any{})
	var resp struct {
		Error   string
		Details []string
	}
	_ = json.Unmarshal(body, &resp)
	if code != http.StatusTooManyRequests || !strings.Contains(resp.Error, "already have a run") || len(resp.Details) != 1 || resp.Details[0] != first {
		t.Errorf("second run = %d %s", code, body)
	}
	other := e.as("bob", "").start("Sleep", nil) // other users are not affected
	for _, id := range []string{first, other} {
		e.as("root", "").do(http.MethodPost, "/api/runs/"+id+"/kill", nil)
		e.finish(id)
	}
	again := e.start("Sleep", nil) // the slot is free again
	e.do(http.MethodPost, "/api/runs/"+again+"/kill", nil)
	e.finish(again)
}
