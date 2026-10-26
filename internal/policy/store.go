package policy

import (
	"encoding/json"
	"errors"
	"fmt"
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
	policyFile = "policy.json"
	// keepExpired is how long expired exceptions stay listed before they are removed.
	keepExpired = 30 * 24 * time.Hour
)

// Options configure a Store.
type Options struct {
	// Environments and UnknownEnvironment seed a new policy; an existing policy keeps its own.
	Environments       []Environment
	UnknownEnvironment string
	// Bootstrap users have the devops role, whatever the policy says (set in the deployment).
	Bootstrap []string
	// MaxExceptionDays bounds how long an exception may last (0: no limit).
	MaxExceptionDays int
}

// Store keeps the policy in memory and in DATA_DIR/policy.json. Readers get immutable snapshots;
// changes replace the whole policy.
type Store struct {
	path string
	opts Options
	now  func() time.Time

	mu sync.RWMutex
	p  *Policy
}

// Open loads the policy from dataDir, or creates it from the seed in opts.
func Open(dataDir string, opts Options) (*Store, error) {
	s := &Store{path: filepath.Join(dataDir, policyFile), opts: opts, now: time.Now}
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		p := &Policy{Revision: 1, Environments: opts.Environments, UnknownEnvironment: opts.UnknownEnvironment}
		p.normalize()
		if err := p.Validate(opts.MaxExceptionDays); err != nil {
			return nil, fmt.Errorf("initial environments: %w", err)
		}
		if err := s.write(p); err != nil {
			return nil, err
		}
		s.p = p
		slog.Info("policy: created", "file", s.path, "environments", p.EnvironmentNames())
	case err != nil:
		return nil, err
	default:
		var p Policy
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, fmt.Errorf("%s: %w", s.path, err)
		}
		p.normalize()
		// Validated without the length limit: it may have been lowered since.
		if err := p.Validate(0); err != nil {
			return nil, fmt.Errorf("%s: %w", s.path, err)
		}
		s.p = &p
		if len(opts.Environments) > 0 && !sameEnvironments(p.Environments, opts.Environments) {
			slog.Warn("policy: the configured environments are only used for a new policy; change them in the Admin area", "file", s.path)
		}
	}
	return s, nil
}

func sameEnvironments(a, b []Environment) bool {
	return slices.EqualFunc(a, b, func(x, y Environment) bool { return x.Name == y.Name && slices.Equal(x.Match, y.Match) })
}

// Snapshot returns the current policy. It must not be modified.
func (s *Store) Snapshot() *Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.p
}

// Bootstrap returns the users that are devops through the deployment settings.
func (s *Store) Bootstrap() []string { return s.opts.Bootstrap }

// MaxExceptionDays returns the longest allowed exception.
func (s *Store) MaxExceptionDays() int { return s.opts.MaxExceptionDays }

// Subject returns the user with the roles from the policy and the bootstrap list.
func (s *Store) Subject(user string) Subject {
	roles := slices.Clone(s.Snapshot().RolesOf(user))
	if containsFold(s.opts.Bootstrap, user) && !containsFold(roles, RoleDevops) {
		roles = append(roles, RoleDevops)
	}
	return Subject{User: user, Roles: roles}
}

// Update applies change to a copy of the policy based on revision, validates and saves it.
// It returns the old and the new policy.
func (s *Store) Update(revision int, change func(p *Policy) error) (before, after *Policy, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.p.Revision {
		return nil, nil, ErrConflict
	}
	next := s.p.Clone()
	if err := change(next); err != nil {
		return nil, nil, err
	}
	next.normalize()
	// The length limit applies to new and changed exceptions only: it may have been lowered
	// after older ones were granted.
	if err := next.Validate(0); err != nil {
		return nil, nil, err
	}
	for _, e := range next.Exceptions {
		old, ok := s.p.FindException(e.ID)
		if ok && old.Expires.Equal(e.Expires) && old.Created.Equal(e.Created) {
			continue
		}
		if err := (&Policy{Environments: next.Environments, Exceptions: []Exception{e}}).Validate(s.opts.MaxExceptionDays); err != nil {
			return nil, nil, err
		}
	}
	next.Revision = s.p.Revision + 1
	if err := s.write(next); err != nil {
		return nil, nil, err
	}
	before, s.p = s.p, next
	return before, next, nil
}

// Sweep marks exceptions that expired since the last sweep and removes those expired long ago.
// It returns the newly expired ones, to be audited. It does not change the revision.
func (s *Store) Sweep() ([]Exception, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	var expired []Exception
	changed := false
	next := s.p.Clone()
	kept := next.Exceptions[:0]
	for _, e := range next.Exceptions {
		if !e.Active(now) && !e.ExpiryAudited {
			e.ExpiryAudited, changed = true, true
			expired = append(expired, e)
		}
		if now.Sub(e.Expires) > keepExpired {
			changed = true
			continue
		}
		kept = append(kept, e)
	}
	next.Exceptions = kept
	if !changed {
		return nil, nil
	}
	if err := s.write(next); err != nil {
		return nil, err
	}
	s.p = next
	return expired, nil
}

func (s *Store) write(p *Policy) error {
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// FindException returns the exception with id.
func (p *Policy) FindException(id string) (Exception, bool) {
	for _, e := range p.Exceptions {
		if e.ID == id {
			return e, true
		}
	}
	return Exception{}, false
}

// FindRule returns the rule with id.
func (p *Policy) FindRule(id string) (Rule, bool) {
	for _, r := range p.Rules {
		if r.ID == id {
			return r, true
		}
	}
	return Rule{}, false
}

// RoleIndex returns the index of the role named name, or -1.
func (p *Policy) RoleIndex(name string) int {
	return slices.IndexFunc(p.Roles, func(r Role) bool { return strings.EqualFold(r.Name, name) })
}
