package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestUnsafeBrowserRequestsRequireSameOrigin(t *testing.T) {
	for _, tc := range []struct {
		name, origin, site string
		want               int
	}{
		{"same origin", "https://blundernet.com", "same-origin", http.StatusCreated},
		{"non-browser", "", "", http.StatusCreated},
		{"foreign origin", "https://evil.example", "", http.StatusForbidden},
		{"same site subdomain", "https://other.blundernet.com", "same-site", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, q := newTestServer(t)
			r := httptest.NewRequest(http.MethodPost, "https://blundernet.com/api/games", strings.NewReader(`{"color":"black"}`))
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Sec-Fetch-Site", tc.site)
			r.Header.Set("Content-Type", "text/plain")
			w := httptest.NewRecorder()
			s.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", w.Code, tc.want, w.Body)
			}
			if tc.want == http.StatusForbidden && len(q.jobs) != 0 {
				t.Fatal("rejected request still queued work")
			}
		})
	}
}

func TestGameSocketRejectsOversizedIncomingMessage(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/api/games", `{}`)
	var state State
	if err := json.Unmarshal(rec.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/games/"+state.ID+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(strings.Repeat("x", 2048))); err != nil {
		t.Fatal(err)
	}
	if _, _, err := conn.ReadMessage(); !websocket.IsCloseError(err, websocket.CloseMessageTooBig) {
		t.Fatalf("oversized frame was not refused: %v", err)
	}
}

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

func TestWebSocketOriginIsChecked(t *testing.T) {
	s, _ := newTestServer(t)
	s.secureCookies = true // production

	cases := []struct {
		origin string
		want   bool
	}{
		{"", true},                             // not a browser, no cookies to steal
		{"https://blundernet.com", true},       // the site itself
		{"https://evil.example", false},        // the attack this exists for
		{"https://blundernet.com.evil", false}, // suffix trick
		{"http://localhost:5173", false},       // dev origin, not in production
	}
	for _, c := range cases {
		r := httptest.NewRequest(http.MethodGet, "/api/games/x/ws", nil)
		r.Host = "blundernet.com"
		if c.origin != "" {
			r.Header.Set("Origin", c.origin)
		}
		if got := s.sameOrigin(r); got != c.want {
			t.Errorf("origin %q: allowed=%v, want %v", c.origin, got, c.want)
		}
	}

	// The vite dev server proxies the socket, so the origin and the host never
	// match in development and it has to be allowed there.
	s.secureCookies = false
	r := httptest.NewRequest(http.MethodGet, "/api/games/x/ws", nil)
	r.Host = "localhost:8080"
	r.Header.Set("Origin", "http://localhost:5173")
	if !s.sameOrigin(r) {
		t.Error("dev origin refused in development")
	}
}
