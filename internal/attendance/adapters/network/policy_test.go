package network

import (
	"net/netip"
	"testing"
)

func prefixes(t *testing.T, ss ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}

func TestPolicyAllowsOnlyConfiguredNetworks(t *testing.T) {
	p := New(prefixes(t, "192.0.2.0/24", "198.51.100.7/32", "2001:db8::/32"), false)
	tests := []struct {
		ip   string
		want bool
	}{
		{"192.0.2.1", true}, {"192.0.2.255", true}, {"192.0.3.1", false}, {"192.0.1.255", false},
		{"198.51.100.7", true}, {"198.51.100.8", false},
		{"2001:db8::1", true}, {"2001:db9::1", false},
		{"::ffff:192.0.2.10", true}, // IPv4-mapped IPv6 is the same address
		{"10.0.0.1", false}, {"192.168.1.1", false}, {"127.0.0.1", false},
	}
	for _, tt := range tests {
		if got := p.Allows(netip.MustParseAddr(tt.ip)); got != tt.want {
			t.Errorf("Allows(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}

func TestEmptyConfigurationAllowsNothing(t *testing.T) {
	for _, p := range []Policy{New(nil, false), New([]netip.Prefix{}, false)} {
		for _, ip := range []string{"192.0.2.1", "10.0.0.1", "192.168.0.1", "8.8.8.8", "::1"} {
			if p.Allows(netip.MustParseAddr(ip)) {
				t.Fatalf("an empty policy must not allow %s: a missing configuration is never 'any network'", ip)
			}
		}
	}
}

func TestUnknownAddressIsAlwaysDenied(t *testing.T) {
	if New(prefixes(t, "0.0.0.0/0"), false).Allows(netip.Addr{}) {
		t.Fatal("an undetermined address must be denied")
	}
	if New(nil, true).Allows(netip.Addr{}) {
		t.Fatal("even the development switch must not accept an undetermined address")
	}
}

func TestDevelopmentSwitchAllowsAnyKnownAddress(t *testing.T) {
	p := New(nil, true)
	if !p.Allows(netip.MustParseAddr("203.0.113.5")) {
		t.Fatal("allow-any must accept known addresses")
	}
}
