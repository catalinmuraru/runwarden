package activity

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/catalinmuraru/runwarden/internal/audit"
)

var t0 = time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)

func TestQuery(t *testing.T) {
	s, err := Open(t.TempDir(), 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	add := func(at time.Time, user, action, outcome, script string) {
		s.Add(audit.Record{Time: at, User: user, Action: action, Outcome: outcome, Script: script})
	}
	add(t0.Add(-48*time.Hour), "alice", audit.RunStart, "success", "Backup")
	add(t0.Add(-time.Hour), "Bob", audit.RunStart, "failure", "Restore")
	add(t0, "alice", audit.RunEnd, "success", "Backup")
	add(t0.Add(time.Minute), "alice", audit.Logout, "success", "")

	all, err := s.Query(Filter{})
	if err != nil || len(all) != 4 || all[0].Action != audit.Logout || all[3].Time != t0.Add(-48*time.Hour) {
		t.Fatalf("all = %+v (%v)", all, err)
	}
	cases := map[string]struct {
		f    Filter
		want int
	}{
		"user (any case)": {Filter{User: "BOB"}, 1},
		"action":          {Filter{Action: audit.RunStart}, 2},
		"outcome":         {Filter{Outcome: "failure"}, 1},
		"script":          {Filter{Script: "Backup"}, 2},
		"since":           {Filter{Since: t0.Add(-2 * time.Hour)}, 3},
		"until":           {Filter{Until: t0}, 2},
		"limit":           {Filter{Limit: 2}, 2},
	}
	for name, c := range cases {
		got, err := s.Query(c.f)
		if err != nil || len(got) != c.want {
			t.Errorf("%s: got %d (%v), want %d", name, len(got), err, c.want)
		}
	}

	n, err := s.Cleanup(24*time.Hour, t0)
	if err != nil || n != 1 {
		t.Errorf("cleanup removed %d (%v), want 1", n, err)
	}
	if left, _ := s.Query(Filter{}); len(left) != 3 {
		t.Errorf("after cleanup %d events, want 3", len(left))
	}
}

func TestUsers(t *testing.T) {
	data := t.TempDir()
	s, _ := Open(data, 12*time.Hour)

	s.Add(audit.Record{Time: t0, User: "alice", Groups: []string{"ops"}, Action: audit.Login, Outcome: "success", SourceIP: "10.0.0.1"})
	s.Add(audit.Record{Time: t0, User: "mallory", Action: audit.Login, Outcome: "failure"})
	s.Seen("bob", []string{"dev"}, "10.0.0.2", t0)
	s.Add(audit.Record{Time: t0.Add(time.Minute), User: "carol", Action: audit.Login, Outcome: "success"})
	s.Add(audit.Record{Time: t0.Add(2 * time.Minute), User: "carol", Action: audit.Logout, Outcome: "success"})

	status := func(now time.Time) map[string]string {
		out := map[string]string{}
		for _, u := range s.Users(now) {
			out[u.Name] = u.Status
		}
		return out
	}
	got := status(t0.Add(3 * time.Minute))
	want := map[string]string{"alice": StatusActive, "bob": StatusActive, "carol": StatusSignedOut}
	if len(got) != len(want) {
		t.Fatalf("users = %v (failed sign-ins must not create users)", got)
	}
	for name, st := range want {
		if got[name] != st {
			t.Errorf("%s = %s, want %s", name, got[name], st)
		}
	}
	if got := status(t0.Add(time.Hour)); got["alice"] != StatusSignedIn || got["bob"] != StatusSignedIn {
		t.Errorf("idle users = %v, want signed-in", got)
	}
	if got := status(t0.Add(13 * time.Hour)); got["alice"] != StatusSignedOut || got["bob"] != StatusSignedOut {
		t.Errorf("expired sessions = %v, want signed-out", got)
	}

	s.Seen("Carol", nil, "10.0.0.3", t0.Add(4*time.Minute)) // a session in another browser
	if got := status(t0.Add(5 * time.Minute)); got["carol"] != StatusActive {
		t.Errorf("carol after new activity = %s", got["carol"])
	}

	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, "activity", usersFile)); err != nil {
		t.Fatal(err)
	}
	s2, _ := Open(data, 12*time.Hour)
	users := s2.Users(t0.Add(3 * time.Minute))
	if len(users) != 3 {
		t.Fatalf("reopened users = %+v", users)
	}
	for _, u := range users {
		if u.Name == "alice" && (u.LastIP != "10.0.0.1" || len(u.Groups) != 1 || u.LastLogin == nil) {
			t.Errorf("alice = %+v", u)
		}
	}
}

func TestSeenThrottled(t *testing.T) {
	s, _ := Open(t.TempDir(), time.Hour)
	s.Seen("alice", nil, "ip", t0)
	_ = s.Flush()
	s.Seen("alice", nil, "ip", t0.Add(10*time.Second))
	if s.dirty {
		t.Error("an update within the throttle window must be skipped")
	}
	s.Seen("alice", nil, "other-ip", t0.Add(11*time.Second))
	if !s.dirty {
		t.Error("a new address must be recorded")
	}
}

func TestDayCapDropsOnlyFailedSignIns(t *testing.T) {
	s, _ := Open(t.TempDir(), time.Hour)
	s.MaxDayBytes = 400
	for i := range 20 {
		s.Add(audit.Record{Time: t0.Add(time.Duration(i) * time.Second), User: "x", Action: audit.Login, Outcome: "failure", Reason: "invalid user name or password"})
	}
	s.Add(audit.Record{Time: t0.Add(time.Minute), User: "alice", Action: audit.RunStart, Outcome: "success", Script: "S"})
	all, _ := s.Query(Filter{Limit: 100})
	failed, markers := 0, 0
	for _, r := range all {
		switch {
		case r.Reason != "":
			failed++
		case r.Action == audit.Login:
			markers++
		}
	}
	if failed >= 20 || failed == 0 || markers != 1 || all[0].Action != audit.RunStart {
		t.Errorf("failed=%d markers=%d newest=%s", failed, markers, all[0].Action)
	}
}
