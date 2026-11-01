// Package netx derives the real client address of an HTTP request and checks it
// against configured networks.
package netx

import (
	"net/http"
	"net/netip"
	"strings"
)

// ClientIP returns the address of the peer that opened the connection. The
// X-Forwarded-For header is only consulted when that peer is a trusted proxy,
// and the chain is walked right-to-left skipping trusted proxies so a client
// cannot spoof an address by prepending entries.
func ClientIP(r *http.Request, trusted []netip.Prefix) (netip.Addr, bool) {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	addr := peer.Addr().Unmap()
	if !contains(trusted, addr) {
		return addr, true
	}
	hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return addr, true // malformed chain: fall back to the proxy address
		}
		hop = hop.Unmap()
		if !contains(trusted, hop) {
			return hop, true
		}
	}
	return addr, true
}

// InNetworks reports whether addr belongs to any of nets. An empty list allows nothing.
func InNetworks(addr netip.Addr, nets []netip.Prefix) bool {
	return contains(nets, addr.Unmap())
}

func contains(nets []netip.Prefix, addr netip.Addr) bool {
	for _, n := range nets {
		if n.Contains(addr) {
			return true
		}
	}
	return false
}
