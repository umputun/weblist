package server

import (
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// clientIPMiddleware replaces r.RemoteAddr with the client address, without the port, so rate limits,
// lockouts and logs key on the client rather than the connection.
func (wb *Web) clientIPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ip, ok := wb.clientIP(r); ok {
			r.RemoteAddr = ip.String()
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the TCP peer, or, when the peer is a trusted proxy, the address it forwarded: the
// rightmost X-Forwarded-For hop that is not itself a trusted proxy, else X-Real-IP. Headers from any
// other peer are ignored, as a client can set them to anything. ok is false for a peer that is not an
// ip:port, which a TCP listener never produces.
func (wb *Web) clientIP(r *http.Request) (ip netip.Addr, ok bool) {
	peerPort, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	peer := peerPort.Addr().Unmap()
	if !wb.isTrustedProxy(peer) {
		return peer, true
	}

	// a proxy appends to the header a client may have sent already, so only the right end is reliable
	if values := r.Header.Values("X-Forwarded-For"); len(values) > 0 {
		hops := strings.Split(strings.Join(values, ","), ",")
		var hop netip.Addr
		for _, h := range slices.Backward(hops) {
			if hop, err = netip.ParseAddr(strings.TrimSpace(h)); err != nil {
				return peer, true
			}
			hop = hop.Unmap()
			if !wb.isTrustedProxy(hop) {
				return hop, true
			}
		}
		return hop, true
	}

	if realIP, err := netip.ParseAddr(strings.TrimSpace(r.Header.Get("X-Real-IP"))); err == nil {
		return realIP.Unmap(), true
	}
	return peer, true
}

func (wb *Web) isTrustedProxy(addr netip.Addr) bool {
	for _, p := range wb.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
