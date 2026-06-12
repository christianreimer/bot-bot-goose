package httpx

import (
	"net/http"
	"testing"
)

func TestClientIPHandlesIPv4AndIPv6(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// IPv4 from Go's HTTP server (host:port).
		{"127.0.0.1:5678", "127.0.0.1"},
		{"203.0.113.7:443", "203.0.113.7"},
		// IPv6 — Go formats `[ip]:port`, naive last-colon strip would
		// leave the brackets.
		{"[::1]:54321", "::1"},
		{"[2001:db8::1]:8080", "2001:db8::1"},
		// Bare address with no port — possible behind some proxies.
		{"::1", "::1"},
		{"[::1]", "::1"},
		{"10.0.0.1", "10.0.0.1"},
	}
	for _, c := range cases {
		r := &http.Request{RemoteAddr: c.in}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
