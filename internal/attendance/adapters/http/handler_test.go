package http

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/edutrak/backend/internal/attendance/adapters/network"
)

// These tests exercise the exact pair the check-in uses: the client address the
// handler derives and the institutional network policy that judges it.

func req(remote string, xff ...string) *http.Request {
	r := httptest.NewRequest("POST", "/api/attendance/sessions/x/check-in", nil)
	r.RemoteAddr = remote
	for _, v := range xff {
		r.Header.Add("X-Forwarded-For", v)
	}
	return r
}

func prefixes(ss ...string) []netip.Prefix {
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestClientAddressAndPolicy(t *testing.T) {
	institution := network.New(prefixes("192.0.2.0/24"), false)
	proxy := prefixes("203.0.113.10/32") // a reverse proxy WE operate

	tests := []struct {
		name    string
		trusted []netip.Prefix
		r       *http.Request
		wantIP  string
		allowed bool
	}{
		{"real connection from the institution", nil, req("192.0.2.50:5555"), "192.0.2.50", true},
		{"real connection from outside", nil, req("198.51.100.4:5555"), "198.51.100.4", false},
		{"NAT: many students, one shared address", nil, req("192.0.2.1:40000"), "192.0.2.1", true},

		// spoofing: nobody is a trusted proxy, so the header is ignored completely
		{"outsider claims an institutional address via X-Forwarded-For", nil, req("198.51.100.4:5555", "192.0.2.50"), "198.51.100.4", false},
		{"outsider prepends several fake entries", nil, req("198.51.100.4:5555", "192.0.2.1, 192.0.2.2, 192.0.2.3"), "198.51.100.4", false},
		{"outsider sends the header twice", nil, req("198.51.100.4:5555", "192.0.2.1", "192.0.2.2"), "198.51.100.4", false},
		{"institutional client cannot be downgraded by a header", nil, req("192.0.2.50:5555", "198.51.100.4"), "192.0.2.50", true},

		// a trusted proxy: the header is honoured, walking right-to-left
		{"trusted proxy forwards an institutional client", proxy, req("203.0.113.10:443", "192.0.2.50"), "192.0.2.50", true},
		{"trusted proxy forwards an outside client", proxy, req("203.0.113.10:443", "198.51.100.4"), "198.51.100.4", false},
		{"client prepends a fake institutional entry behind a trusted proxy", proxy, req("203.0.113.10:443", "192.0.2.50, 198.51.100.4"), "198.51.100.4", false},
		{"proxy chain entries are skipped", proxy, req("203.0.113.10:443", "192.0.2.50, 203.0.113.10"), "192.0.2.50", true},
		{"trusted proxy without header: the proxy itself is judged", proxy, req("203.0.113.10:443"), "203.0.113.10", false},
		{"malformed header falls back to the proxy address", proxy, req("203.0.113.10:443", "not-an-ip"), "203.0.113.10", false},

		// the header from a peer that is NOT the trusted proxy is still ignored
		{"untrusted peer next to a trusted proxy", proxy, req("198.51.100.4:5555", "192.0.2.50"), "198.51.100.4", false},

		{"IPv4-mapped IPv6 peer", nil, req("[::ffff:192.0.2.9]:5555"), "192.0.2.9", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{trustedProxies: tt.trusted}
			ip := h.clientIP(tt.r)
			if ip.String() != tt.wantIP {
				t.Fatalf("client IP = %s, want %s", ip, tt.wantIP)
			}
			if got := institution.Allows(ip); got != tt.allowed {
				t.Fatalf("policy.Allows(%s) = %v, want %v", ip, got, tt.allowed)
			}
		})
	}
}

func TestUnparseablePeerYieldsAnUnknownAddressThatIsDenied(t *testing.T) {
	h := &Handler{}
	ip := h.clientIP(req("garbage"))
	if ip.IsValid() {
		t.Fatalf("ip = %s, want the zero address", ip)
	}
	for _, p := range []network.Policy{network.New(prefixes("192.0.2.0/24"), false), network.New(nil, true), network.New(nil, false)} {
		if p.Allows(ip) {
			t.Fatal("an unknown address must always be denied")
		}
	}
}

func TestNoConfiguredNetworksAllowsNobody(t *testing.T) {
	h := &Handler{}
	none := network.New(nil, false)
	for _, remote := range []string{"192.0.2.50:1", "10.1.2.3:1", "192.168.1.9:1", "127.0.0.1:1"} {
		if none.Allows(h.clientIP(req(remote))) {
			t.Fatalf("%s accepted with no configured networks: private addresses are not automatic proof of presence", remote)
		}
	}
}
