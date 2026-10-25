// Package clientip finds the address of the client behind a request, trusting X-Forwarded-For
// only when the request comes from a configured proxy (e.g. the ingress controller).
package clientip

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver resolves client addresses.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given proxy networks (CIDRs or single addresses).
func New(trusted []string) (Resolver, error) {
	var r Resolver
	for _, t := range trusted {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if !strings.Contains(t, "/") {
			addr, err := netip.ParseAddr(t)
			if err != nil {
				return Resolver{}, fmt.Errorf("trusted proxy %q: %w", t, err)
			}
			t = netip.PrefixFrom(addr, addr.BitLen()).String()
		}
		p, err := netip.ParsePrefix(t)
		if err != nil {
			return Resolver{}, fmt.Errorf("trusted proxy %q: %w", t, err)
		}
		r.trusted = append(r.trusted, p.Masked())
	}
	return r, nil
}

func (r Resolver) isTrusted(a netip.Addr) bool {
	for _, p := range r.trusted {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// IP returns the client address of req. When the direct peer is a trusted proxy, it walks
// X-Forwarded-For from the right and returns the first address that is not a trusted proxy, so
// a client can't spoof its address by sending the header itself.
func (r Resolver) IP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		host = req.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if !r.isTrusted(peer) {
		return peer.String()
	}
	var hops []string
	for _, h := range req.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !r.isTrusted(a) {
			return a.String()
		}
	}
	return peer.String()
}
