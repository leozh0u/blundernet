package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSecurityHeadersOnEveryResponse(t *testing.T) {
	s, _ := newTestServer(t)
	// One API route and one page route, because they are served by different
	// handlers and the point of the middleware is that neither can miss it.
	for _, path := range []string{"/api/stats", "/healthz", "/"} {
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		for _, h := range []string{
			"Content-Security-Policy",
			"X-Content-Type-Options",
			"X-Frame-Options",
			"Referrer-Policy",
		} {
			if rec.Header().Get(h) == "" {
				t.Errorf("%s: missing %s", path, h)
			}
		}
		if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "frame-ancestors 'none'") {
			t.Errorf("%s: csp does not forbid framing: %s", path, got)
		}
	}
}

func TestHSTSOnlyWhenServingHTTPS(t *testing.T) {
	s, _ := newTestServer(t)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("plaintext server sent HSTS: %q", got)
	}
	s.secureCookies = true
	rec = httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Fatal("https server did not send HSTS")
	}
}

func TestOversizedBodyIsRefused(t *testing.T) {
	s, _ := newTestServer(t)
	// A megabyte of nothing at the login endpoint, which needs no account and
	// is the obvious place to point a memory exhaustion attempt.
	body := `{"username":"a","password":"` + strings.Repeat("x", 1<<20) + `"}`
	rec := do(t, s, http.MethodPost, "/api/auth/login", body)
	if rec.Code == http.StatusOK {
		t.Fatalf("oversized body accepted: %d", rec.Code)
	}
}
