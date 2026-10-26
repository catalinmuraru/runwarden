//go:build unix

package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/history"
)

func (e *testEnv) runs(query string) []history.Meta {
	e.t.Helper()
	code, body := e.do(http.MethodGet, "/api/runs"+query, nil)
	if code != http.StatusOK {
		e.t.Fatalf("GET /api/runs%s = %d %s", query, code, body)
	}
	var resp struct{ Runs []history.Meta }
	_ = json.Unmarshal(body, &resp)
	return resp.Runs
}

func ids(runs []history.Meta) []string {
	var out []string
	for _, m := range runs {
		out = append(out, m.ID)
	}
	return out
}

func TestRunningAndAdmin(t *testing.T) {
	acts, err := activity.Open(t.TempDir(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	audit.SetSink(acts.Add)
	t.Cleanup(func() { audit.SetSink(nil) })

	alice := setup(t, executor.Limits{KillGrace: 200 * time.Millisecond}, Options{Activity: acts})
	bob := alice.as("bob", "")
	root := alice.as("root", "")

	a := alice.start("Sleep", nil)
	b := bob.start("Sleep", nil)
	done := alice.start("Echo", map[string]any{"Name": "x"})
	alice.finish(done)

	// alice may view Sleep, so she sees bob's run too; erin may not.
	if got := ids(alice.runs("?status=running")); len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("alice running = %q, want both", got)
	}
	if got := alice.as("erin", "").runs("?status=running"); len(got) != 0 {
		t.Errorf("erin running = %q, want none", ids(got))
	}
	if got := ids(root.runs("?status=running")); len(got) != 2 || got[0] != b || got[1] != a {
		t.Errorf("admin running = %q, want both, newest first", got)
	}
	if got := ids(root.runs("?status=running&user=BOB")); len(got) != 1 || got[0] != b {
		t.Errorf("admin running for bob = %q", got)
	}
	if got := ids(bob.runs("?user=alice")); len(got) != 2 || got[0] != done || got[1] != a {
		t.Errorf("bob filtering by alice sees %q", got)
	}
	if code, _ := bob.do(http.MethodPost, "/api/runs/"+a+"/stop", nil); code != http.StatusForbidden {
		t.Errorf("bob stops alice's run = %d, want 403", code)
	}
	if got := ids(root.runs("?user=alice&status=finished")); len(got) != 1 || got[0] != done {
		t.Errorf("admin finished runs of alice = %q", got)
	}

	for _, path := range []string{"/api/admin/users", "/api/admin/activity"} {
		if code, _ := alice.do(http.MethodGet, path, nil); code != http.StatusNotFound {
			t.Errorf("non-admin GET %s = %d, want 404", path, code)
		}
	}

	code, body := root.do(http.MethodGet, "/api/admin/users", nil)
	if code != http.StatusOK {
		t.Fatalf("users = %d %s", code, body)
	}
	var users struct{ Users []adminUser }
	_ = json.Unmarshal(body, &users)
	byName := map[string]adminUser{}
	for _, u := range users.Users {
		byName[u.Name] = u
	}
	if u := byName["alice"]; u.Runs != 2 || u.Running != 1 || u.Status != activity.StatusActive || u.Admin {
		t.Errorf("alice = %+v", u)
	}
	if u := byName["root"]; !u.Admin {
		t.Errorf("root = %+v", u)
	}

	code, body = root.do(http.MethodGet, "/api/admin/activity?user=bob&action=run-start", nil)
	var acts2 struct{ Events []audit.Record }
	_ = json.Unmarshal(body, &acts2)
	if code != http.StatusOK || len(acts2.Events) != 1 || acts2.Events[0].RunID != b {
		t.Errorf("bob's activity = %d %s", code, body)
	}
	if code, _ := root.do(http.MethodGet, "/api/admin/activity?since=yesterday", nil); code != http.StatusBadRequest {
		t.Errorf("bad since = %d, want 400", code)
	}

	for _, id := range []string{a, b} {
		root.do(http.MethodPost, "/api/runs/"+id+"/kill", nil)
		root.finish(id)
	}
}

func TestScriptTimeout(t *testing.T) {
	e := setup(t, executor.Limits{Timeout: time.Hour, KillGrace: 200 * time.Millisecond}, Options{})
	e.script("Sleep").Timeout = 300 * time.Millisecond

	_, body := e.do(http.MethodGet, "/api/scripts/Sleep", nil)
	if !strings.Contains(string(body), `"timeout_seconds":0`) {
		t.Errorf("sub-second timeout shown as %s", body)
	}
	_, body = e.do(http.MethodGet, "/api/scripts/Echo", nil)
	if !strings.Contains(string(body), `"timeout_seconds":3600`) {
		t.Errorf("default timeout not shown: %s", body)
	}

	if m := e.finish(e.start("Sleep", nil)); m.Status != string(executor.StatusTimedOut) {
		t.Errorf("status = %s, want %s", m.Status, executor.StatusTimedOut)
	}
}

type fakeSessions struct{ revoked []string }

func (f *fakeSessions) Summary() map[string]auth.SessionSummary {
	return map[string]auth.SessionSummary{"alice": {Count: 2, Methods: []string{"gitlab"}, LastSeen: time.Now()}}
}

func (f *fakeSessions) RevokeUser(name string) int {
	f.revoked = append(f.revoked, name)
	return 2
}

func TestAdminSessions(t *testing.T) {
	acts, _ := activity.Open(t.TempDir(), time.Hour)
	audit.SetSink(acts.Add)
	t.Cleanup(func() { audit.SetSink(nil) })
	sessions := &fakeSessions{}
	alice := setup(t, executor.Limits{}, Options{Activity: acts, Sessions: sessions})
	root := alice.as("root", "")
	alice.do(http.MethodGet, "/api/scripts", nil) // alice is known to the users index
	root.do(http.MethodGet, "/api/scripts", nil)

	_, body := root.do(http.MethodGet, "/api/admin/users", nil)
	var users struct{ Users []adminUser }
	_ = json.Unmarshal(body, &users)
	for _, u := range users.Users {
		switch u.Name {
		case "alice":
			if u.Sessions != 2 || u.Status != activity.StatusActive || len(u.Methods) != 1 {
				t.Errorf("alice = %+v", u)
			}
		case "root":
			if u.Sessions != 0 || u.Status != activity.StatusSignedOut {
				t.Errorf("root without sessions = %+v", u)
			}
		}
	}

	if code, _ := alice.do(http.MethodPost, "/api/admin/users/bob/sign-out", nil); code != http.StatusNotFound {
		t.Errorf("non-admin sign-out = %d, want 404", code)
	}
	code, body := root.do(http.MethodPost, "/api/admin/users/alice/sign-out", nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"revoked":2`) || len(sessions.revoked) != 1 || sessions.revoked[0] != "alice" {
		t.Errorf("sign-out = %d %s %v", code, body, sessions.revoked)
	}
	events, _ := acts.Query(activity.Filter{Action: audit.SessionRevoke})
	if len(events) != 1 || events[0].User != "root" || !strings.Contains(events[0].Reason, "alice") {
		t.Errorf("revoke not audited: %+v", events)
	}
}
