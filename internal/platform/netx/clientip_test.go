package netx

import (
	"net/http"
	"net/netip"
	"testing"
)

func TestClientIP(t *testing.T) {
	proxy := []netip.Prefix{netip.MustParsePrefix("10.0.0.1/32")}
	tests := []struct {
		name    string
		remote  string
		xff     string
		trusted []netip.Prefix
		want    string
	}{
		{"direct client ignores xff", "203.0.113.9:5000", "1.2.3.4", proxy, "203.0.113.9"},
		{"trusted proxy honours xff", "10.0.0.1:5000", "192.168.1.20", proxy, "192.168.1.20"},
		{"spoofed prefix is ignored", "10.0.0.1:5000", "1.2.3.4, 192.168.1.20", proxy, "192.168.1.20"},
		{"chain through two proxies", "10.0.0.1:5000", "192.168.1.20, 10.0.0.1", proxy, "192.168.1.20"},
		{"no xff uses proxy", "10.0.0.1:5000", "", proxy, "10.0.0.1"},
		{"malformed xff falls back", "10.0.0.1:5000", "garbage", proxy, "10.0.0.1"},
		{"no trusted proxies", "10.0.0.1:5000", "1.2.3.4", nil, "10.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &http.Request{RemoteAddr: tt.remote, Header: http.Header{}}
			if tt.xff != "" {
				r.Header.Set("X-Forwarded-For", tt.xff)
			}
			got, ok := ClientIP(r, tt.trusted)
			if !ok || got.String() != tt.want {
				t.Fatalf("got %v ok=%v, want %s", got, ok, tt.want)
			}
		})
	}
}

func TestInNetworks(t *testing.T) {
	nets := []netip.Prefix{netip.MustParsePrefix("192.168.0.0/16")}
	if !InNetworks(netip.MustParseAddr("192.168.4.4"), nets) {
		t.Fatal("expected inside")
	}
	if InNetworks(netip.MustParseAddr("8.8.8.8"), nets) || InNetworks(netip.MustParseAddr("8.8.8.8"), nil) {
		t.Fatal("expected outside")
	}
}
