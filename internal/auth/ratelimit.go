package auth

import (
	"sync"
	"time"
)

// limiter blocks a key after max failures within window. Keys are user name + client address,
// so an attacker can't lock a user out from everywhere.
type limiter struct {
	mu       sync.Mutex
	failures map[string][]time.Time
	max      int
	window   time.Duration
	now      func() time.Time
}

func newLimiter(maxFailures int, window time.Duration) *limiter {
	return &limiter{failures: map[string][]time.Time{}, max: maxFailures, window: window, now: time.Now}
}

// allowed reports whether key may try again. A limiter with max <= 0 never blocks.
func (l *limiter) allowed(key string) bool {
	if l.max <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) < l.max
}

// first records an event for key and reports whether it is the first one in the window.
func (l *limiter) first(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.recent(key))
	l.failures[key] = append(l.failures[key], l.now())
	return n == 0
}

func (l *limiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.failures[key] = append(l.recent(key), l.now())
	if len(l.failures) > 10000 {
		for k := range l.failures {
			l.recent(k)
		}
	}
}

func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.failures, key)
}

// recent drops expired failures for key and returns the rest. Callers hold l.mu.
func (l *limiter) recent(key string) []time.Time {
	cutoff := l.now().Add(-l.window)
	kept := l.failures[key][:0]
	for _, t := range l.failures[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, key)
		return nil
	}
	l.failures[key] = kept
	return kept
}
