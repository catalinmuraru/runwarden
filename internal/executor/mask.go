package executor

import (
	"bytes"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// maxLineHold bounds how much of an unfinished line is held back for regex redaction.
const maxLineHold = 4 << 10

// masker replaces secrets in streamed output before it is stored.
//   - Secure values (and hidden input) are replaced wherever they appear. The last
//     len(longest)-1 bytes of each chunk are held back, so a secret split across two chunks
//     is still caught.
//   - Redaction patterns (regexes) are applied to whole lines, so an unfinished line is held
//     back (up to maxLineHold bytes).
//
// Held-back bytes are released by flush when the stream has been idle for a moment, so a
// prompt like "Continue? " still reaches the user.
type masker struct {
	mu       sync.Mutex
	secrets  [][]byte // longest first
	patterns []*regexp.Regexp
	pending  []byte
}

func newMasker(secrets []string, patterns []*regexp.Regexp) *masker {
	m := &masker{patterns: patterns}
	for _, s := range secrets {
		m.addLocked(s)
	}
	return m
}

// addSecret masks s from now on (e.g. hidden input that a terminal will echo).
func (m *masker) addSecret(s string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.addLocked(s)
}

func (m *masker) addLocked(s string) {
	if strings.TrimSpace(s) == "" || slices.ContainsFunc(m.secrets, func(b []byte) bool { return string(b) == s }) {
		return
	}
	m.secrets = append(m.secrets, []byte(s))
	slices.SortFunc(m.secrets, func(a, b []byte) int { return len(b) - len(a) })
}

// write masks p and returns the part that is safe to emit now.
func (m *masker) write(p []byte) []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.secrets) == 0 && len(m.patterns) == 0 {
		return p
	}
	buf := m.replace(append(m.pending, p...))
	cut := len(buf)
	if len(m.secrets) > 0 {
		cut = max(len(buf)-(len(m.secrets[0])-1), 0)
	}
	if len(m.patterns) > 0 && len(buf)-cut < maxLineHold {
		// Hold back the unfinished last line for the line-based patterns.
		if nl := bytes.LastIndexByte(buf[:cut], '\n'); nl >= 0 {
			cut = nl + 1
		} else if len(buf) < maxLineHold {
			cut = 0
		}
	}
	m.pending = append([]byte(nil), buf[cut:]...)
	return m.redact(buf[:cut])
}

// pendingLen reports how many bytes are held back.
func (m *masker) pendingLen() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.pending)
}

// flush returns whatever is held back.
func (m *masker) flush() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.redact(m.replace(m.pending))
	m.pending = nil
	return out
}

func (m *masker) replace(buf []byte) []byte {
	for _, s := range m.secrets {
		buf = bytes.ReplaceAll(buf, s, []byte(params.Mask))
	}
	return buf
}

func (m *masker) redact(buf []byte) []byte {
	for _, re := range m.patterns {
		buf = re.ReplaceAll(buf, []byte(params.Mask))
	}
	return buf
}
