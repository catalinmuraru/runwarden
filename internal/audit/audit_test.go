package audit

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// capture sends the default logger to a buffer for the duration of the test.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func TestRunEndEvent(t *testing.T) {
	buf := capture(t)
	code := 3
	Log(t.Context(), Event{
		Action: RunEnd, Category: "process", Type: "end", Success: false, Reason: "failed",
		User: "alice", Roles: []string{"developer"}, Groups: []string{"ops"}, Environments: []string{"production"}, SourceIP: "203.0.113.7", UserAgent: "curl/8",
		RunID: "r1", Script: "Backup", Status: "failed", ExitCode: &code, Duration: 1500 * time.Millisecond,
		Values: map[string]any{"Token": "******", "Name": "x"}, Args: []string{"--token", "******"},
	})

	var e map[string]any
	if err := json.Unmarshal(buf.Bytes(), &e); err != nil {
		t.Fatal(err)
	}
	event := e["event"].(map[string]any)
	checks := map[string]any{
		"kind": "event", "action": RunEnd, "outcome": "failure", "reason": "failed", "dataset": Dataset,
		"duration": float64(1500 * time.Millisecond),
	}
	for k, v := range checks {
		if event[k] != v {
			t.Errorf("event.%s = %v, want %v", k, event[k], v)
		}
	}
	if c := event["category"].([]any); len(c) != 1 || c[0] != "process" {
		t.Errorf("event.category = %v", c)
	}
	if e["level"] != "WARN" {
		t.Errorf("failures log at warn: %v", e["level"])
	}
	user := e["user"].(map[string]any)
	if user["name"] != "alice" || user["roles"].([]any)[0] != "developer" {
		t.Errorf("user = %v", user)
	}
	if e["source"].(map[string]any)["ip"] != "203.0.113.7" || e["user_agent"].(map[string]any)["original"] != "curl/8" {
		t.Errorf("source/user_agent = %v %v", e["source"], e["user_agent"])
	}
	process := e["process"].(map[string]any)
	if process["exit_code"] != float64(3) || process["args"].([]any)[1] != "******" {
		t.Errorf("process = %v", process)
	}
	rw := e["runwarden"].(map[string]any)
	run := rw["run"].(map[string]any)
	if run["id"] != "r1" || run["status"] != "failed" || run["parameters"] != `{"Name":"x","Token":"******"}` {
		t.Errorf("runwarden.run = %v", run)
	}
	if g := rw["auth"].(map[string]any)["groups"].([]any); g[0] != "ops" || rw["environments"].([]any)[0] != "production" {
		t.Errorf("runwarden.auth / environments = %v", rw)
	}
	if rw["script"].(map[string]any)["name"] != "Backup" {
		t.Errorf("runwarden.script = %v", rw["script"])
	}
}

func TestSuccessIsInfoAndOmitsEmptyFields(t *testing.T) {
	buf := capture(t)
	Log(t.Context(), Event{Action: Logout, Category: "authentication", Type: "end", Success: true, User: "bob"})
	var e map[string]any
	_ = json.Unmarshal(buf.Bytes(), &e)
	if e["level"] != "INFO" || e["event"].(map[string]any)["outcome"] != "success" || e["msg"] != "logout success" {
		t.Errorf("event = %v", e)
	}
	for _, absent := range []string{"process", "source", "runwarden", "user_agent"} {
		if _, ok := e[absent]; ok {
			t.Errorf("%s should be omitted when empty", absent)
		}
	}
}

func TestSink(t *testing.T) {
	var got []Record
	SetSink(func(r Record) { got = append(got, r) })
	t.Cleanup(func() { SetSink(nil) })
	code := 3
	Log(t.Context(), Event{Action: RunEnd, User: "alice", Script: "S", RunID: "r1", ExitCode: &code,
		Duration: 1500 * time.Millisecond, Values: map[string]any{"Token": "******"}})
	SetSink(nil)
	Log(t.Context(), Event{Action: RunStart, Success: true})
	if len(got) != 1 {
		t.Fatalf("sink got %d records, want 1", len(got))
	}
	r := got[0]
	if r.Outcome != "failure" || r.User != "alice" || r.DurationMS != 1500 || *r.ExitCode != 3 || r.Values["Token"] != "******" || r.Time.IsZero() || r.Message == "" {
		t.Errorf("record = %+v", r)
	}
}
