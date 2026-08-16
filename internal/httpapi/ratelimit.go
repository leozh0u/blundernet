package httpapi

import (
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/store"
)

// Limits are per route group rather than global, because the routes cost
// wildly different things. A move is cheap for the api and expensive for the
// worker behind it; a login is expensive on purpose.
//
// They are configurable rather than constants because the right numbers are
// not knowable before there is traffic. Anonymous callers share a bucket per
// address, so anywhere behind one NAT gets squeezed first, and the only
// honest way to set these is to watch blundernet_rate_limited_total and
// adjust. Local compose runs them wide open so repeated test runs are not
// fighting the production values.
type Limits struct {
	Auth       store.Limit
	CreateGame store.Limit
	Move       store.Limit
	Puzzles    store.Limit
}

// DefaultLimits are the production values.
func DefaultLimits() Limits {
	return Limits{
		// Signup and login. Tight, because what this slows down is credential
		// stuffing rather than a person mistyping a password.
		Auth: store.Limit{Burst: 10, Rate: 1.0 / 60}, // 1/min sustained

		// Starting a game costs a queue job and a quarter second of engine
		// CPU, so this is the one worth holding down.
		CreateGame: store.Limit{Burst: 10, Rate: 0.1}, // 6/min sustained

		// Moves in a game already under way. Loose enough that a fast blitz
		// player never notices it.
		Move: store.Limit{Burst: 60, Rate: 2},

		// A puzzle search is a handful of indexed scans, and a drill session
		// asks for a new batch every ten or twenty puzzles. Loose, but not so
		// loose that somebody can page the whole corpus out of the database.
		Puzzles: store.Limit{Burst: 30, Rate: 1},
	}
}

// withDefaults fills any limit left at its zero value.
func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.Auth == (store.Limit{}) {
		l.Auth = d.Auth
	}
	if l.CreateGame == (store.Limit{}) {
		l.CreateGame = d.CreateGame
	}
	if l.Move == (store.Limit{}) {
		l.Move = d.Move
	}
	if l.Puzzles == (store.Limit{}) {
		l.Puzzles = d.Puzzles
	}
	return l
}

// limitKey identifies who is being limited, and reports whether that identity
// was a registered account.
//
// Only registered users get a bucket of their own. Guests fall back to the
// address along with everyone signed out, because a guest account is free to
// create: giving one its own bucket would let a caller mint a fresh identity
// whenever the old one ran dry, which is not a rate limit.
func (s *Server) limitKey(r *http.Request, group string) (string, bool) {
	if u := UserFrom(r.Context()); u != nil && !u.IsGuest {
		return group + ":user:" + u.ID, true
	}
	return group + ":ip:" + s.clientIP(r), false
}

// clientIP trusts X-Forwarded-For only when the request arrives through a
// proxy we put there. Behind the ALB, or behind Caddy on the small deploy,
// that header is written by our own infrastructure. With no proxy in front it
// is attacker controlled, and trusting it would let anyone hand themselves a
// fresh bucket on every request.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			// Leftmost entry is the original client; the rest is the proxy
			// chain appended on the way in.
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limit wraps a handler in a bucket. A nil limiter passes everything through,
// which is what unit tests get.
func (s *Server) limit(group string, l store.Limit, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.limiter != nil {
			key, identified := s.limitKey(r, group)
			if !s.limiter.Allow(r.Context(), key, l) {
				obs.RateLimited(group, identified)
				w.Header().Set("Retry-After", strconv.Itoa(int(l.RetryAfter().Seconds())+1))
				httpError(w, http.StatusTooManyRequests, "too many requests, slow down")
				return
			}
		}
		next(w, r)
	}
}
