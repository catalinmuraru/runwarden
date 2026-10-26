// Package activity keeps the in-app view of the audit trail for admins: every audit event as
// a JSON line in one file per day, and an index of the users who signed in or used the server.
// The application log (ECS, shipped by Filebeat) stays the system of record.
package activity

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/catalinmuraru/runwarden/internal/audit"
)

const (
	dayLayout = "2006-01-02"
	usersFile = "users.json"

	// ActiveWindow is how recent the last request must be for a user to count as active.
	ActiveWindow = 5 * time.Minute
	// seenEvery throttles last-activity updates per user.
	seenEvery = 30 * time.Second
	// maxQuery caps the events one query returns.
	maxQuery = 1000
)

// User statuses.
const (
	StatusActive    = "active"     // a request in the last ActiveWindow
	StatusSignedIn  = "signed-in"  // the session may still be valid, but idle
	StatusSignedOut = "signed-out" // signed out, or the session has expired
)

// User is what the server knows about one user.
type User struct {
	Name      string     `json:"name"`
	Groups    []string   `json:"groups,omitempty"`
	FirstSeen time.Time  `json:"first_seen"`
	LastSeen  time.Time  `json:"last_seen"`
	LastLogin *time.Time `json:"last_login,omitempty"`
	SignedOut *time.Time `json:"signed_out,omitempty"`
	LastIP    string     `json:"last_ip,omitempty"`
	Status    string     `json:"status,omitempty"` // computed when listed
}

// Store is the activity log under <data dir>/activity.
type Store struct {
	dir        string
	sessionTTL time.Duration
	// MaxDayBytes caps a day's file for failed sign-ins (0: no cap); see Add.
	MaxDayBytes int64

	fileMu sync.Mutex      // serializes appends
	capped map[string]bool // days whose cap marker is written

	mu    sync.Mutex
	users map[string]*User // by lower-case name
	dirty bool
}

// Open opens (creating if needed) the activity log in dataDir. sessionTTL is how long a
// session lasts after signing in; it decides when an idle user counts as signed out.
func Open(dataDir string, sessionTTL time.Duration) (*Store, error) {
	s := &Store{dir: filepath.Join(dataDir, "activity"), sessionTTL: sessionTTL, users: map[string]*User{}, capped: map[string]bool{}}
	if err := os.MkdirAll(s.dir, 0o750); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(s.dir, usersFile))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		var list []*User
		if err := json.Unmarshal(data, &list); err != nil {
			slog.Warn("activity: unreadable users index; starting a new one", "err", err)
		}
		for _, u := range list {
			s.users[strings.ToLower(u.Name)] = u
		}
	}
	return s, nil
}

// Add appends an audit record and updates the users index from it. Once a day's file exceeds
// MaxDayBytes, failed sign-ins (which anyone can cause without an account) are no longer
// written to it; one marker record says so. Everything else is always kept.
func (s *Store) Add(r audit.Record) {
	s.fileMu.Lock()
	err := s.append(r)
	s.fileMu.Unlock()
	if err != nil {
		slog.Error("activity: append", "err", err)
	}

	if r.User == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case r.Action == audit.Login && r.Outcome == "success":
		u := s.user(r.User, r.Time)
		t := r.Time
		u.LastLogin, u.LastSeen, u.Groups = &t, t, r.Groups
		if r.SourceIP != "" {
			u.LastIP = r.SourceIP
		}
	case r.Action == audit.Logout:
		u := s.user(r.User, r.Time)
		t := r.Time
		u.SignedOut = &t
	case r.Action != audit.Login: // failed sign-ins do not prove the user exists
		u := s.user(r.User, r.Time)
		if r.Time.After(u.LastSeen) {
			u.LastSeen = r.Time
		}
		if r.SourceIP != "" {
			u.LastIP = r.SourceIP
		}
	}
	s.dirty = true
}

// append writes r to its day's file. Callers hold s.fileMu.
func (s *Store) append(r audit.Record) error {
	path := s.dayFile(r.Time)
	if s.MaxDayBytes > 0 && r.Action == audit.Login && r.Outcome == "failure" {
		if info, err := os.Stat(path); err == nil && info.Size() >= s.MaxDayBytes {
			day := filepath.Base(path)
			if s.capped[day] {
				return nil
			}
			s.capped[day] = true
			r = audit.Record{Time: r.Time, Action: audit.Login, Outcome: "failure",
				Message: "activity log limit reached: further failed sign-ins today are only in the application log"}
		}
	}
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // path built from a date in our own directory
	if err != nil {
		return err
	}
	_, err = f.Write(append(line, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Seen records a request by a signed-in user. Updates are throttled per user.
func (s *Store) Seen(name string, groups []string, ip string, now time.Time) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.user(name, now)
	if now.Sub(u.LastSeen) < seenEvery && u.LastIP == ip && !s.signedOutAfterSeen(u) {
		return
	}
	u.LastSeen = now
	if len(groups) > 0 {
		u.Groups = groups
	}
	if ip != "" {
		u.LastIP = ip
	}
	s.dirty = true
}

// user returns the index entry for name, creating it. Callers hold s.mu.
func (s *Store) user(name string, now time.Time) *User {
	key := strings.ToLower(name)
	u := s.users[key]
	if u == nil {
		u = &User{Name: name, FirstSeen: now, LastSeen: now}
		s.users[key] = u
	}
	return u
}

func (s *Store) signedOutAfterSeen(u *User) bool {
	return u.SignedOut != nil && !u.LastSeen.After(*u.SignedOut)
}

// Users lists everyone known, most recently seen first, with their current status.
func (s *Store) Users(now time.Time) []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]User, 0, len(s.users))
	for _, u := range s.users {
		c := *u
		c.Groups = slices.Clone(u.Groups)
		c.Status = s.status(u, now)
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b User) int {
		if c := b.LastSeen.Compare(a.LastSeen); c != 0 {
			return c
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

func (s *Store) status(u *User, now time.Time) string {
	switch {
	case s.signedOutAfterSeen(u):
		return StatusSignedOut
	case now.Sub(u.LastSeen) <= ActiveWindow:
		return StatusActive
	case u.LastLogin != nil && now.Before(u.LastLogin.Add(s.sessionTTL)):
		return StatusSignedIn
	case u.LastLogin == nil && now.Sub(u.LastSeen) < s.sessionTTL:
		return StatusSignedIn // signed in before the index existed
	default:
		return StatusSignedOut
	}
}

// Flush writes the users index if it changed.
func (s *Store) Flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	list := make([]*User, 0, len(s.users))
	for _, u := range s.users {
		c := *u
		c.Status = ""
		list = append(list, &c)
	}
	s.dirty = false
	s.mu.Unlock()

	slices.SortFunc(list, func(a, b *User) int { return strings.Compare(a.Name, b.Name) })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, usersFile)
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Filter narrows Query. Empty fields match everything.
type Filter struct {
	User    string // case-insensitive
	Action  string
	Outcome string
	Script  string
	Since   time.Time
	Until   time.Time
	Limit   int // default 200, at most 1000
}

func (f Filter) match(r audit.Record) bool {
	return (f.User == "" || strings.EqualFold(f.User, r.User)) &&
		(f.Action == "" || f.Action == r.Action) &&
		(f.Outcome == "" || f.Outcome == r.Outcome) &&
		(f.Script == "" || f.Script == r.Script) &&
		(f.Since.IsZero() || !r.Time.Before(f.Since)) &&
		(f.Until.IsZero() || r.Time.Before(f.Until))
}

// Query returns matching events, newest first.
func (s *Store) Query(f Filter) ([]audit.Record, error) {
	if f.Limit <= 0 {
		f.Limit = 200
	}
	f.Limit = min(f.Limit, maxQuery)
	days, err := s.days()
	if err != nil {
		return nil, err
	}
	out := []audit.Record{}
	for i := len(days) - 1; i >= 0 && len(out) < f.Limit; i-- {
		day := days[i]
		if !f.Since.IsZero() && day.Add(24*time.Hour).Before(f.Since) {
			break
		}
		if !f.Until.IsZero() && !day.Before(f.Until) {
			continue
		}
		records, err := s.readDay(day)
		if err != nil {
			return nil, err
		}
		for j := len(records) - 1; j >= 0 && len(out) < f.Limit; j-- {
			if f.match(records[j]) {
				out = append(out, records[j])
			}
		}
	}
	return out, nil
}

// Cleanup removes days older than maxAge (0: keep everything) and returns how many.
func (s *Store) Cleanup(maxAge time.Duration, now time.Time) (int, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	days, err := s.days()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, day := range days {
		if now.Sub(day.Add(24*time.Hour)) <= maxAge {
			continue
		}
		if err := os.Remove(s.dayFile(day)); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

func (s *Store) dayFile(t time.Time) string {
	return filepath.Join(s.dir, t.UTC().Format(dayLayout)+".jsonl")
}

// days lists the days with a file, oldest first.
func (s *Store) days() ([]time.Time, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []time.Time
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".jsonl")
		if !ok || e.IsDir() {
			continue
		}
		if day, err := time.Parse(dayLayout, name); err == nil {
			out = append(out, day)
		}
	}
	slices.SortFunc(out, time.Time.Compare)
	return out, nil
}

func (s *Store) readDay(day time.Time) ([]audit.Record, error) {
	data, err := os.ReadFile(s.dayFile(day))
	if err != nil {
		return nil, err
	}
	var out []audit.Record
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	for sc.Scan() {
		var r audit.Record
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}
