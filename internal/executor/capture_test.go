//go:build unix

package executor

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCapture(t *testing.T) {
	t.Setenv("APP_SECRET", "must-not-leak")
	e := New(Limits{}, EnvPolicy{})
	dir := t.TempDir()

	out, err := e.Capture(t.Context(), []string{"bash", "-c", `printf 'a\nb\n'; pwd; env`}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); !strings.HasPrefix(s, "a\nb\n"+dir+"\n") || strings.Contains(s, "APP_SECRET") {
		t.Errorf("output = %q", s)
	}

	if _, err := e.Capture(t.Context(), []string{"bash", "-c", "echo oops >&2; exit 4"}, dir); err == nil || !strings.Contains(err.Error(), "oops") {
		t.Errorf("failure error = %v", err)
	}
	if _, err := e.Capture(t.Context(), []string{"bash", "-c", "head -c 2000000 /dev/zero"}, dir); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("overflow error = %v", err)
	}
	if _, err := e.Capture(t.Context(), nil, dir); err == nil {
		t.Error("empty command accepted")
	}
}

func TestCaptureTimeoutKillsGroup(t *testing.T) {
	e := New(Limits{}, EnvPolicy{})
	ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := e.Capture(ctx, []string{"bash", "-c", "sleep 30 & sleep 30"}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v; the background child kept it alive", elapsed)
	}
}
