//go:build unix

package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/catalinmuraru/runwarden/internal/executor"
)

// lockedBuffer is a log sink safe for concurrent writes and reads.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func captureLogs(t *testing.T) *lockedBuffer {
	t.Helper()
	buf := &lockedBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

type auditLine struct {
	Event struct {
		Action, Outcome, Reason, Dataset string
		Duration                         int64
	}
	User struct {
		Name  string
		Roles []string
	}
	Source  struct{ IP string }
	Process struct {
		ExitCode *int     `json:"exit_code"`
		Args     []string `json:"args"`
	}
	Runwarden struct {
		Run    struct{ ID, Status, Parameters string }
		Script struct{ Name string }
	}
}

func auditEvents(t *testing.T, logs *lockedBuffer) []auditLine {
	t.Helper()
	var out []auditLine
	for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var a auditLine
		if json.Unmarshal([]byte(line), &a) == nil && a.Event.Dataset == "runwarden.audit" {
			out = append(out, a)
		}
	}
	return out
}

func find(events []auditLine, action, outcome, script string) *auditLine {
	for i := range events {
		e := &events[i]
		if e.Event.Action == action && e.Event.Outcome == outcome && e.Runwarden.Script.Name == script {
			return e
		}
	}
	return nil
}

func TestAuditTrail(t *testing.T) {
	logs := captureLogs(t)
	e := setup(t, executor.Limits{KillGrace: 200 * 1e6}, Options{})

	id := e.start("Echo", map[string]any{"Name": "abc", "Token": "s3cret-xyz"})
	e.finish(id)

	if code, _ := e.as("bob", "").do(http.MethodPost, "/api/scripts/Restricted/runs", map[string]any{}); code != http.StatusNotFound {
		t.Fatalf("bob start = %d", code)
	}
	if code, _ := e.do(http.MethodPost, "/api/scripts/Echo/runs", map[string]any{"values": map[string]any{"Name": "BAD"}}); code != http.StatusBadRequest {
		t.Fatalf("invalid start = %d", code)
	}
	prompt := e.start("Prompt", nil)
	e.waitOutput(prompt, "Continue? ")
	e.do(http.MethodPost, "/api/runs/"+prompt+"/input", map[string]any{"line": "yes"})
	e.finish(prompt)
	sleep := e.start("Sleep", nil)
	e.waitOutput(sleep, "started")
	e.do(http.MethodPost, "/api/runs/"+sleep+"/kill", nil)
	e.finish(sleep)

	events := auditEvents(t, logs)

	start := find(events, "run-start", "success", "Echo")
	if start == nil || start.User.Name != "alice" || start.User.Roles[0] != "developer" || start.Source.IP != "127.0.0.1" ||
		start.Runwarden.Run.ID != id || !strings.Contains(start.Runwarden.Run.Parameters, `"Token":"******"`) {
		t.Errorf("run-start = %+v", start)
	}
	end := find(events, "run-end", "success", "Echo")
	if end == nil || end.User.Name != "alice" || end.Process.ExitCode == nil || *end.Process.ExitCode != 0 ||
		end.Runwarden.Run.Status != "finished" || end.Event.Duration <= 0 || end.Source.IP != "127.0.0.1" {
		t.Errorf("run-end = %+v", end)
	}
	if f := find(events, "run-start", "failure", "Restricted"); f == nil || f.User.Name != "bob" || f.Event.Reason != "not allowed to run this script" {
		t.Errorf("forbidden attempt = %+v", f)
	}
	if f := find(events, "run-start", "failure", "Echo"); f == nil || !strings.Contains(f.Event.Reason, `"Name": must match`) {
		t.Errorf("invalid parameters = %+v", f)
	}
	if f := find(events, "run-input", "success", "Prompt"); f == nil || f.Runwarden.Run.ID != prompt {
		t.Errorf("input = %+v", f)
	}
	if f := find(events, "run-kill", "success", "Sleep"); f == nil {
		t.Error("kill not audited")
	}
	if f := find(events, "run-end", "failure", "Sleep"); f == nil || f.Event.Reason != "killed" || f.User.Name != "alice" {
		t.Errorf("killed run end = %+v", f)
	}
	if strings.Contains(logs.String(), "s3cret-xyz") || strings.Contains(logs.String(), "BAD") {
		t.Error("a submitted value leaked into the logs")
	}
}
