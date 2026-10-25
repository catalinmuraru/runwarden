package clientip

import (
	"net/http/httptest"
	"testing"
)

func TestIP(t *testing.T) {
	r, err := New([]string{"10.0.0.0/8", "192.168.1.5", " "})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, remote, xff, want string
	}{
		{"direct client", "203.0.113.7:5000", "", "203.0.113.7"},
		{"untrusted peer cannot spoof", "203.0.113.7:5000", "1.2.3.4", "203.0.113.7"},
		{"via trusted ingress", "10.1.2.3:5000", "198.51.100.9", "198.51.100.9"},
		{"client-supplied entries left of the real client are ignored", "10.1.2.3:5000", "1.2.3.4, 198.51.100.9", "198.51.100.9"},
		{"several trusted hops", "10.1.2.3:5000", "198.51.100.9, 192.168.1.5, 10.9.9.9", "198.51.100.9"},
		{"trusted peer without header", "10.1.2.3:5000", "", "10.1.2.3"},
		{"garbage header", "10.1.2.3:5000", "not-an-ip", "10.1.2.3"},
		{"ipv4-mapped ipv6 peer", "[::ffff:10.1.2.3]:5000", "198.51.100.9", "198.51.100.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequestWithContext(t.Context(), "GET", "/", nil)
			req.RemoteAddr = tt.remote
			if tt.xff != "" {
				req.Header.Set("X-Forwarded-For", tt.xff)
			}
			if got := r.IP(req); got != tt.want {
				t.Errorf("IP = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := New([]string{"not a cidr"}); err == nil {
		t.Error("invalid proxy accepted")
	}
}
