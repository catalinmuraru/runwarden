package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/auth"
)

// adminOnly hides fn from everyone but global admins: others get the same 404 as for unknown
// paths.
func (s *Server) adminOnly(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Activity == nil || !s.opts.Policy.IsAdmin(currentUser(r)) {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		fn(w, r)
	}
}

// trackActivity records that the signed-in user made a request.
func (s *Server) trackActivity(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.opts.Activity != nil {
			u := currentUser(r)
			s.opts.Activity.Seen(u.Name, u.Groups, s.opts.ClientIP.IP(r), time.Now())
		}
		next(w, r)
	}
}

type adminUser struct {
	activity.User
	Runs     int      `json:"runs"`
	Running  int      `json:"running"`
	Admin    bool     `json:"admin"`
	Sessions int      `json:"sessions"`          // valid sign-in sessions
	Methods  []string `json:"methods,omitempty"` // how those sessions signed in
}

// SessionAdmin lists and revokes sign-in sessions (implemented by auth.Store).
type SessionAdmin interface {
	Summary() map[string]auth.SessionSummary
	RevokeUser(name string) int
}

func (s *Server) adminUsers(w http.ResponseWriter, _ *http.Request) {
	counts, err := s.hist.RunsByUser()
	if err != nil {
		slog.Error("count runs", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	running := map[string]int{}
	for _, id := range s.exec.Running() {
		if m, err := s.hist.Get(id); err == nil {
			running[strings.ToLower(m.User)]++
		}
	}
	now := time.Now()
	var sessions map[string]auth.SessionSummary
	if s.opts.Sessions != nil {
		sessions = s.opts.Sessions.Summary()
	}
	users := s.opts.Activity.Users(now)
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		key := strings.ToLower(u.Name)
		au := adminUser{User: u, Runs: counts[key], Running: running[key]}
		au.Admin = s.opts.Policy.IsAdmin(authUser(u))
		if sessions != nil {
			// Real sessions are known: they decide whether the user is signed in.
			sum := sessions[key]
			au.Sessions, au.Methods = sum.Count, sum.Methods
			seen := u.LastSeen
			if sum.LastSeen.After(seen) {
				seen = sum.LastSeen
			}
			switch {
			case sum.Count == 0:
				au.Status = activity.StatusSignedOut
			case now.Sub(seen) <= activity.ActiveWindow:
				au.Status = activity.StatusActive
			default:
				au.Status = activity.StatusSignedIn
			}
		}
		out = append(out, au)
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out, "active_window_seconds": int(activity.ActiveWindow.Seconds())})
}

func (s *Server) adminActivity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := activity.Filter{User: q.Get("user"), Action: q.Get("action"), Outcome: q.Get("outcome"), Script: q.Get("script")}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))
	for key, dst := range map[string]*time.Time{"since": &f.Since, "until": &f.Until} {
		if v := q.Get(key); v != "" {
			t, err := time.Parse(time.RFC3339, v)
			if err != nil {
				writeError(w, http.StatusBadRequest, key+" must be an RFC 3339 time")
				return
			}
			*dst = t
		}
	}
	events, err := s.opts.Activity.Query(f)
	if err != nil {
		slog.Error("read activity", "err", err)
		writeError(w, http.StatusInternalServerError, "could not read the activity log")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// authUser rebuilds enough of a user for authorization checks in listings.
func authUser(u activity.User) auth.User {
	au := auth.User{Name: u.Name, Groups: u.Groups}
	if u.Name == auth.LocalAdminName {
		au.Method = auth.MethodLocal // the name is reserved for the built-in administrator
	}
	return au
}

// adminSignOut ends every session of a user ("sign out everywhere").
func (s *Server) adminSignOut(w http.ResponseWriter, r *http.Request) {
	if s.opts.Sessions == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	name := r.PathValue("name")
	n := s.opts.Sessions.RevokeUser(name)
	s.record(r, audit.Event{Action: audit.SessionRevoke, Category: "authentication", Type: "end", Success: true,
		Count: n, Reason: "signed out by an administrator: " + name,
		Message: fmt.Sprintf("%d sessions of %s revoked", n, name)})
	writeJSON(w, http.StatusOK, map[string]int{"revoked": n})
}
