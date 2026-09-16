// Package clientip derives the address a request should be attributed to.
//
// Every address-keyed decision in this gateway — the token-guessing throttle,
// the push rate limiter's failure budget, the public registration limiter, the
// /metrics allowlist — reads the address from here. Getting it wrong does not
// break loudly: behind a reverse proxy the peer address is the proxy's, so
// every client collapses into one bucket and the limits silently become global.
//
// It is a package of its own because three servers need the same rule and the
// rule is security-sensitive enough that a second copy of it would eventually
// disagree with the first.
package clientip

import (
	"net"
	"net/http"
	"strings"
)

// ForwardedFor is the header a proxy appends the address it received from.
const ForwardedFor = "X-Forwarded-For"

// From returns the address to attribute r to.
//
// trusted lists the networks whose X-Forwarded-For is believed. When it is
// empty — the default, and what a deployment with no proxy in front should
// leave it as — the header is ignored entirely and the peer address is used.
//
// When the peer is trusted, the header is read from the right: each proxy
// appends the address it saw, so the rightmost entry is the one the nearest
// trusted hop observed, while anything to its left was supplied by the client
// and can say whatever it likes. Entries that are themselves trusted are
// skipped, which is what makes a chain of two proxies work; the first
// untrusted entry from the right is the client.
//
// A malformed entry never stops the walk but is never returned either.
func From(r *http.Request, trusted []*net.IPNet) string {
	peer := hostOnly(r.RemoteAddr)
	if len(trusted) == 0 || !contains(trusted, peer) {
		// Either nothing is trusted, or this particular peer is not. Both mean
		// the header is caller-supplied and unusable.
		return peer
	}

	entries := strings.Split(r.Header.Get(ForwardedFor), ",")
	// Walk right to left, skipping hops we would ourselves have trusted.
	for i := len(entries) - 1; i >= 0; i-- {
		addr := hostOnly(strings.TrimSpace(entries[i]))
		if addr == "" {
			continue
		}
		if net.ParseIP(addr) == nil {
			continue
		}
		if contains(trusted, addr) {
			continue
		}
		return addr
	}

	// Every hop was a trusted proxy, so the chain tells us nothing beyond what
	// the peer already did. Fall back to the leftmost parseable entry, which is
	// the furthest back the chain goes, and failing that to the peer.
	for _, entry := range entries {
		addr := hostOnly(strings.TrimSpace(entry))
		if addr != "" && net.ParseIP(addr) != nil {
			return addr
		}
	}
	return peer
}

// hostOnly strips a port and surrounding brackets if one is present. Proxies
// are inconsistent about whether they append "1.2.3.4" or "1.2.3.4:5678", and
// a bracketed IPv6 peer arrives as "[2001:db8::1]:443".
func hostOnly(addr string) string {
	if addr == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	// No port to strip. Brackets without a port are still possible.
	return strings.Trim(addr, "[]")
}

// contains reports whether addr falls in any of the networks. An address that
// does not parse is in none of them: an unparseable peer is never trusted.
func contains(nets []*net.IPNet, addr string) bool {
	ip := net.ParseIP(addr)
	if ip == nil {
		return false
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
