package auth

import (
	"crypto/sha256"
	"encoding/hex"
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
)

const (
	sessionsFile = "sessions.json"
	// touchEvery throttles last-seen updates per session.
	touchEvery = 30 * time.Second
)

// Store keeps sign-in sessions on the server, so they can be listed and revoked. The cookie
// carries only a random token; the store is keyed by its SHA-256, so the file on disk cannot be
// used to sign in.
type Store struct {
	path string
	ttl  time.Duration
	now  func() time.Time

	mu       sync.Mutex
	sessions map[string]*storedSession // by token hash
	dirty    bool

	saveMu sync.Mutex // serializes writes of the file
}

type storedSession struct {
	User     User      `json:"user"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Expires  time.Time `json:"expires"`
}

// SessionSummary describes one user's valid sessions.
type SessionSummary struct {
	Count    int
	Methods  []string
	LastSeen time.Time
}

// OpenStore loads the sessions kept in dataDir. Sessions last ttl from sign-in.
func OpenStore(dataDir string, ttl time.Duration) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, sessionsFile), ttl: ttl, now: time.Now, sessions: map[string]*storedSession{}}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, err
	default:
		if err := json.Unmarshal(data, &s.sessions); err != nil {
			slog.Warn("auth: unreadable sessions file; everyone has to sign in again", "err", err)
			s.sessions = map[string]*storedSession{}
		}
	}
	return s, nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Create starts a session for u and returns its token. It is saved at once.
func (s *Store) Create(u User) (string, error) {
	token := randomString()
	now := s.now()
	s.mu.Lock()
	s.sessions[hashToken(token)] = &storedSession{User: u, Created: now, LastSeen: now, Expires: now.Add(s.ttl)}
	s.mu.Unlock()
	return token, s.save()
}

// Get returns the user of a valid session and records the activity.
func (s *Store) Get(token string) (User, bool) {
	if token == "" {
		return User{}, false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	ss, ok := s.sessions[hashToken(token)]
	if !ok || !now.Before(ss.Expires) {
		return User{}, false
	}
	if now.Sub(ss.LastSeen) >= touchEvery {
		ss.LastSeen = now
		s.dirty = true
	}
	return ss.User, true
}

// Revoke ends the session with this token and returns its user.
func (s *Store) Revoke(token string) (User, bool) {
	key := hashToken(token)
	s.mu.Lock()
	ss, ok := s.sessions[key]
	delete(s.sessions, key)
	s.mu.Unlock()
	if !ok {
		return User{}, false
	}
	if err := s.save(); err != nil {
		slog.Error("auth: save sessions", "err", err)
	}
	return ss.User, true
}

// RevokeWhere ends every session whose user matches and returns how many ended.
func (s *Store) RevokeWhere(match func(User) bool) int {
	s.mu.Lock()
	n := 0
	for k, ss := range s.sessions {
		if match(ss.User) {
			delete(s.sessions, k)
			n++
		}
	}
	s.mu.Unlock()
	if n > 0 {
		if err := s.save(); err != nil {
			slog.Error("auth: save sessions", "err", err)
		}
	}
	return n
}

// RevokeUser ends every session of the user name (case-insensitive).
func (s *Store) RevokeUser(name string) int {
	return s.RevokeWhere(func(u User) bool { return strings.EqualFold(u.Name, name) })
}

// Summary lists the valid sessions per lower-case user name.
func (s *Store) Summary() map[string]SessionSummary {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]SessionSummary{}
	for _, ss := range s.sessions {
		if !now.Before(ss.Expires) {
			continue
		}
		key := strings.ToLower(ss.User.Name)
		sum := out[key]
		sum.Count++
		if !slices.Contains(sum.Methods, ss.User.Method) {
			sum.Methods = append(sum.Methods, ss.User.Method)
		}
		if ss.LastSeen.After(sum.LastSeen) {
			sum.LastSeen = ss.LastSeen
		}
		out[key] = sum
	}
	return out
}

// Flush saves last-seen times if they changed, and drops expired sessions.
func (s *Store) Flush() error {
	s.mu.Lock()
	dirty := s.dirty
	s.mu.Unlock()
	if !dirty {
		return nil
	}
	return s.save()
}

// save writes the sessions atomically (expired ones are dropped).
func (s *Store) save() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	now := s.now()
	s.mu.Lock()
	for k, ss := range s.sessions {
		if !now.Before(ss.Expires) {
			delete(s.sessions, k)
		}
	}
	data, err := json.Marshal(s.sessions)
	s.dirty = false
	s.mu.Unlock()
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
