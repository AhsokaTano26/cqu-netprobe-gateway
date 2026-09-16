package clientip

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func cidrs(t *testing.T, list ...string) []*net.IPNet {
	t.Helper()
	out := make([]*net.IPNet, 0, len(list))
	for _, raw := range list {
		_, n, err := net.ParseCIDR(raw)
		if err != nil {
			t.Fatalf("ParseCIDR(%q) error = %v", raw, err)
		}
		out = append(out, n)
	}
	return out
}

func request(peer, forwarded string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = peer
	if forwarded != "" {
		r.Header.Set(ForwardedFor, forwarded)
	}
	return r
}

func TestFrom(t *testing.T) {
	// The deployment's own edge: one nginx on the loopback of the same host.
	behindNginx := cidrs(t, "127.0.0.0/8")
	// And a chain: a platform edge that is itself behind our nginx.
	twoHops := cidrs(t, "127.0.0.0/8", "10.0.0.0/8")

	cases := []struct {
		name      string
		trusted   []*net.IPNet
		peer      string
		forwarded string
		want      string
	}{
		{
			name:      "no proxy configured ignores the header",
			peer:      "192.0.2.7:5000",
			forwarded: "203.0.113.9",
			want:      "192.0.2.7",
		},
		{
			name:      "an untrusted peer cannot claim an address",
			trusted:   behindNginx,
			peer:      "192.0.2.7:5000",
			forwarded: "203.0.113.9",
			want:      "192.0.2.7",
		},
		{
			name:      "a trusted peer's header is believed",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			// The client sent its own X-Forwarded-For; nginx appended the
			// address it actually saw. Taking the leftmost would let anyone
			// pick their own bucket, so the rightmost wins.
			name:      "a forged prefix does not win",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "1.2.3.4, 203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			name:      "a forged prefix that names our own network still does not win",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "127.0.0.1, 203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			name:      "a chain of trusted hops resolves to the client",
			trusted:   twoHops,
			peer:      "127.0.0.1:5000",
			forwarded: "203.0.113.9, 10.0.0.5",
			want:      "203.0.113.9",
		},
		{
			name:      "port suffixes are stripped",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "203.0.113.9:41234",
			want:      "203.0.113.9",
		},
		{
			name:      "ipv6 with brackets",
			trusted:   cidrs(t, "127.0.0.0/8", "::1/128"),
			peer:      "[::1]:5000",
			forwarded: "[2001:db8::1]:41234",
			want:      "2001:db8::1",
		},
		{
			// The trust check is per address family: an IPv6 loopback peer is
			// not inside an IPv4 loopback range, so its header stays ignored.
			name:      "an ipv6 peer is not covered by an ipv4 loopback range",
			trusted:   behindNginx,
			peer:      "[::1]:5000",
			forwarded: "203.0.113.9",
			want:      "::1",
		},
		{
			name:      "missing header falls back to the peer",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "",
			want:      "127.0.0.1",
		},
		{
			name:      "empty header falls back to the peer",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "   ",
			want:      "127.0.0.1",
		},
		{
			name:      "junk entries are skipped, not returned",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "not-an-ip, 203.0.113.9",
			want:      "203.0.113.9",
		},
		{
			name:      "a header of nothing but junk falls back to the peer",
			trusted:   behindNginx,
			peer:      "127.0.0.1:5000",
			forwarded: "not-an-ip, , also-not",
			want:      "127.0.0.1",
		},
		{
			name:      "an all-trusted chain falls back to its leftmost entry",
			trusted:   twoHops,
			peer:      "127.0.0.1:5000",
			forwarded: "10.0.0.5, 10.0.0.6",
			want:      "10.0.0.5",
		},
		{
			name:      "a peer with no port is handled",
			trusted:   behindNginx,
			peer:      "127.0.0.1",
			forwarded: "203.0.113.9",
			want:      "203.0.113.9",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := From(request(tc.peer, tc.forwarded), tc.trusted); got != tc.want {
				t.Errorf("From(peer=%q, xff=%q) = %q, want %q",
					tc.peer, tc.forwarded, got, tc.want)
			}
		})
	}
}

// Two clients behind one proxy must not share a bucket. This is the property
// the whole package exists for: before it, every address-keyed limit collapsed
// into one global limit.
func TestFromSeparatesClientsBehindOneProxy(t *testing.T) {
	trusted := cidrs(t, "127.0.0.0/8")

	first := From(request("127.0.0.1:5000", "203.0.113.9"), trusted)
	second := From(request("127.0.0.1:5000", "198.51.100.4"), trusted)
	if first == second {
		t.Fatalf("two clients behind the same proxy both resolved to %q", first)
	}
}
