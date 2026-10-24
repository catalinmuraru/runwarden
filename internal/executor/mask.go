package executor

import (
	"bytes"
	"slices"
	"strings"

	"github.com/catalinmuraru/runwarden/internal/params"
)

// masker replaces secure values in streamed output. It holds back the last len(longest)-1
// bytes of each chunk, so a secret split across two chunks is still replaced.
type masker struct {
	secrets [][]byte // longest first
	hold    int
	pending []byte
}

func newMasker(secrets []string) *masker {
	m := &masker{}
	seen := map[string]bool{}
	for _, s := range secrets {
		if strings.TrimSpace(s) == "" || seen[s] {
			continue
		}
		seen[s] = true
		m.secrets = append(m.secrets, []byte(s))
	}
	slices.SortFunc(m.secrets, func(a, b []byte) int { return len(b) - len(a) })
	if len(m.secrets) > 0 {
		m.hold = len(m.secrets[0]) - 1
	}
	return m
}

// write masks p and returns the part that is safe to emit now.
func (m *masker) write(p []byte) []byte {
	if len(m.secrets) == 0 {
		return p
	}
	buf := m.replace(append(m.pending, p...))
	if len(buf) <= m.hold {
		m.pending = buf
		return nil
	}
	cut := len(buf) - m.hold
	m.pending = append([]byte(nil), buf[cut:]...)
	return buf[:cut]
}

// flush returns whatever is still held back.
func (m *masker) flush() []byte {
	out := m.replace(m.pending)
	m.pending = nil
	return out
}

func (m *masker) replace(buf []byte) []byte {
	for _, s := range m.secrets {
		buf = bytes.ReplaceAll(buf, s, []byte(params.Mask))
	}
	return buf
}
