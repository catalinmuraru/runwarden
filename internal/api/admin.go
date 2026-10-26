package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/catalinmuraru/runwarden/internal/activity"
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
	Runs    int  `json:"runs"`
	Running int  `json:"running"`
	Admin   bool `json:"admin"`
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
	users := s.opts.Activity.Users(time.Now())
	out := make([]adminUser, 0, len(users))
	for _, u := range users {
		key := strings.ToLower(u.Name)
		au := adminUser{User: u, Runs: counts[key], Running: running[key]}
		au.Admin = s.opts.Policy.IsAdmin(authUser(u))
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

func authUser(u activity.User) auth.User { return auth.User{Name: u.Name, Groups: u.Groups} }
