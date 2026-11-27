package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWeb_clientIP(t *testing.T) {
	wb := &Web{Config: Config{TrustedProxies: []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("127.0.0.1/32"),
	}}}

	tbl := []struct {
		name   string
		peer   string
		xff    []string
		realIP string
		want   string
	}{
		{name: "untrusted peer ignores headers", peer: "203.0.113.9:4000", xff: []string{"198.51.100.1"}, realIP: "198.51.100.2",
			want: "203.0.113.9"},
		{name: "untrusted peer without headers", peer: "203.0.113.9:4000", want: "203.0.113.9"},
		{name: "trusted peer uses forwarded client", peer: "10.1.2.3:4000", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "trusted peer takes rightmost untrusted hop", peer: "10.1.2.3:4000", xff: []string{"6.6.6.6, 198.51.100.1, 10.9.9.9"},
			want: "198.51.100.1"},
		{name: "trusted peer joins repeated headers", peer: "10.1.2.3:4000", xff: []string{"6.6.6.6", "198.51.100.1"}, want: "198.51.100.1"},
		{name: "all hops trusted gives leftmost", peer: "10.1.2.3:4000", xff: []string{"10.5.5.5, 10.9.9.9"}, want: "10.5.5.5"},
		{name: "unparsable hop falls back to peer", peer: "10.1.2.3:4000", xff: []string{"198.51.100.1, garbage"}, want: "10.1.2.3"},
		{name: "real-ip wins over forwarded-for", peer: "10.1.2.3:4000", xff: []string{"6.6.6.6"}, realIP: "198.51.100.7",
			want: "198.51.100.7"},
		{name: "unparsable real-ip uses forwarded-for", peer: "10.1.2.3:4000", xff: []string{"198.51.100.1"}, realIP: "nope",
			want: "198.51.100.1"},
		{name: "empty forwarded-for hops are skipped", peer: "10.1.2.3:4000", xff: []string{"198.51.100.1", ""}, want: "198.51.100.1"},
		{name: "blank forwarded-for falls back to peer", peer: "10.1.2.3:4000", xff: []string{" "}, want: "10.1.2.3"},
		{name: "trusted peer uses real-ip without forwarded-for", peer: "10.1.2.3:4000", realIP: "198.51.100.7", want: "198.51.100.7"},
		{name: "real-ip naming a trusted proxy defers to forwarded-for", peer: "10.1.2.3:4000", xff: []string{"198.51.100.1, 10.9.9.9"},
			realIP: "10.9.9.9", want: "198.51.100.1"},
		{name: "trusted real-ip is kept without an untrusted hop", peer: "10.1.2.3:4000", xff: []string{"10.5.5.5"}, realIP: "10.9.9.9",
			want: "10.9.9.9"},
		{name: "forwarded hop with a port", peer: "10.1.2.3:4000", xff: []string{"198.51.100.7:8080"}, want: "198.51.100.7"},
		{name: "forwarded ipv6 hop with a port", peer: "10.1.2.3:4000", xff: []string{"[2001:db8::1]:8080"}, want: "2001:db8::1"},
		{name: "trusted real-ip is kept past an unparsable hop", peer: "10.1.2.3:4000", xff: []string{"garbage"}, realIP: "10.9.9.9",
			want: "10.9.9.9"},
		{name: "unparsable real-ip falls back to peer", peer: "10.1.2.3:4000", realIP: "nope", want: "10.1.2.3"},
		{name: "ipv4-mapped peer matches ipv4 prefix", peer: "[::ffff:10.1.2.3]:4000", xff: []string{"198.51.100.1"}, want: "198.51.100.1"},
		{name: "ipv6 untrusted peer", peer: "[2001:db8::1]:4000", xff: []string{"198.51.100.1"}, want: "2001:db8::1"},
	}
	for _, tc := range tbl {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
			req.RemoteAddr = tc.peer
			for _, v := range tc.xff {
				req.Header.Add("X-Forwarded-For", v)
			}
			if tc.realIP != "" {
				req.Header.Set("X-Real-IP", tc.realIP)
			}
			ip, ok := wb.clientIP(req)
			assert.True(t, ok)
			assert.Equal(t, tc.want, ip.String())
		})
	}

	t.Run("unparsable peer", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
		req.RemoteAddr = "pipe"
		_, ok := wb.clientIP(req)
		assert.False(t, ok)
	})
}

func TestWeb_clientIPMiddleware(t *testing.T) {
	wb := &Web{Config: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}}
	var seen string
	h := wb.clientIPMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }))

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "10.1.2.3:4000"
	req.Header.Set("X-Forwarded-For", "198.51.100.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "198.51.100.1", seen)

	req = httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	req.RemoteAddr = "203.0.113.9:4000"
	req.Header.Set("X-Real-IP", "198.51.100.1")
	h.ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, "203.0.113.9", seen, "port is dropped so one client keeps one rate limit key across connections")
}
