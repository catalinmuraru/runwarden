package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/catalinmuraru/runwarden/internal/auth"
	"github.com/catalinmuraru/runwarden/internal/executor"
)

// errHelpersBusy means too many helper scripts (dropdown values, defaults) are running.
var errHelpersBusy = errors.New("too many lists are loading, try again in a moment")

// helpers bounds how many helper scripts run at once, per user and in total, and caches their
// output briefly: forms ask for the same lists again and again, and every call starts a process.
type helpers struct {
	maxUser, maxTotal int
	ttl               time.Duration

	mu      sync.Mutex
	perUser map[string]int
	total   int
	cache   map[string]cached
}

type cached struct {
	out     []byte
	expires time.Time
}

const maxCached = 500

func newHelpers(maxUser, maxTotal int, ttl time.Duration) *helpers {
	if maxUser <= 0 {
		maxUser = 2
	}
	if maxTotal <= 0 {
		maxTotal = 10
	}
	return &helpers{maxUser: maxUser, maxTotal: maxTotal, ttl: ttl, perUser: map[string]int{}, cache: map[string]cached{}}
}

func (h *helpers) acquire(user string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.total >= h.maxTotal || h.perUser[user] >= h.maxUser {
		return false
	}
	h.total++
	h.perUser[user]++
	return true
}

func (h *helpers) release(user string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.total--
	if h.perUser[user]--; h.perUser[user] <= 0 {
		delete(h.perUser, user)
	}
}

func (h *helpers) get(key string, now time.Time) ([]byte, bool) {
	if h.ttl <= 0 {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.cache[key]
	if !ok || now.After(c.expires) {
		delete(h.cache, key)
		return nil, false
	}
	return c.out, true
}

func (h *helpers) put(key string, out []byte, now time.Time) {
	if h.ttl <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.cache) >= maxCached {
		for k, c := range h.cache {
			if now.After(c.expires) {
				delete(h.cache, k)
			}
		}
		if len(h.cache) >= maxCached {
			h.cache = map[string]cached{}
		}
	}
	h.cache[key] = cached{out: out, expires: now.Add(h.ttl)}
}

// capture runs a helper command through the executor, within the helper limits and cache.
// Failures are not cached. secrets and the kubeconfig (of the environment the helper works on)
// are part of the cache key, so output computed with one environment's credentials or cluster
// is not reused for another.
func (s *Server) capture(ctx context.Context, args []string, dir string, secrets map[string]string, kc *executor.Kube) ([]byte, error) {
	key := dir + "\x00" + strings.Join(args, "\x00") + "\x00" + fingerprint(secrets) + "\x00" + kubeFingerprint(kc)
	now := time.Now()
	if out, ok := s.helpers.get(key, now); ok {
		return out, nil
	}
	u, _ := auth.UserFrom(ctx)
	user := strings.ToLower(u.Name)
	if !s.helpers.acquire(user) {
		return nil, errHelpersBusy
	}
	defer s.helpers.release(user)
	out, err := s.exec.Capture(ctx, args, dir, secrets, kc)
	if err != nil {
		return nil, err
	}
	s.helpers.put(key, out, now)
	return out, nil
}

// fingerprint identifies a set of secrets without keeping their values.
func fingerprint(secrets map[string]string) string {
	if len(secrets) == 0 {
		return ""
	}
	names := make([]string, 0, len(secrets))
	for n := range secrets {
		names = append(names, n)
	}
	slices.Sort(names)
	h := sha256.New()
	for _, n := range names {
		h.Write([]byte(n + "=" + secrets[n] + "\x00"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// kubeFingerprint identifies a kubeconfig and its context variables without keeping them.
func kubeFingerprint(kc *executor.Kube) string {
	if kc == nil {
		return ""
	}
	h := sha256.New()
	h.Write(kc.Config)
	return hex.EncodeToString(h.Sum(nil)) + fingerprint(kc.Env)
}
