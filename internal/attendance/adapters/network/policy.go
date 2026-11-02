// Package network implements the institutional-network policy for QR check-ins.
package network

import (
	"net/netip"

	"github.com/edutrak/backend/internal/platform/netx"
)

// Policy allows an address only if it belongs to a configured institutional
// network. An empty list allows nothing: a missing configuration never means
// "any network". AllowAny exists solely for development and tests and must
// never be enabled in production (config.Load refuses it there).
type Policy struct {
	nets     []netip.Prefix
	allowAny bool
}

func New(nets []netip.Prefix, allowAny bool) Policy { return Policy{nets: nets, allowAny: allowAny} }

func (p Policy) Allows(ip netip.Addr) bool {
	if !ip.IsValid() {
		return false
	}
	if p.allowAny {
		return true
	}
	return netx.InNetworks(ip, p.nets)
}
