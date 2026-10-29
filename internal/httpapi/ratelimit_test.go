package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPUsesTrustedProxyPeer(t *testing.T) {
	s, _ := newTestServer(t)
	s.trustProxy = true
	for _, tc := range []struct{ forwarded, want string }{
		{"", "192.0.2.10"},
		{"198.51.100.1", "198.51.100.1"},
		{"spoofed, 198.51.100.2", "198.51.100.2"},
		{"198.51.100.99, 203.0.113.99, 198.51.100.3", "198.51.100.3"},
		{"198.51.100.99, 2001:0db8::1", "2001:db8::1"},
		{"198.51.100.1, invalid", "192.0.2.10"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = "192.0.2.10:1234"
		r.Header.Set("X-Forwarded-For", tc.forwarded)
		if got := s.clientIP(r); got != tc.want {
			t.Errorf("forwarded %q: %q, want %q", tc.forwarded, got, tc.want)
		}
	}
	s.trustProxy = false
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("X-Forwarded-For", "198.51.100.1")
	if got := s.clientIP(r); got != "192.0.2.10" {
		t.Fatalf("untrusted forwarding header accepted: %q", got)
	}
}
