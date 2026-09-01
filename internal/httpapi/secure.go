package httpapi

import "net/http"

// The headers and limits that have nothing to do with any one route.
//
// Everything here is the kind of protection that is invisible when it works
// and is only ever noticed by its absence, which is exactly why it belongs in
// one middleware rather than being remembered at each handler.

// maxBody is the ceiling on any request body.
//
// The largest thing anyone legitimately sends is a pasted game, and that is
// already capped at 64KB by the import handler. Everything else is a move or a
// username. Without a ceiling, an unauthenticated POST to the login endpoint
// can stream gigabytes into json.Decode and the box runs out of memory long
// before the rate limiter cares, because the limiter counts requests and this
// is one request. Nesting is fine: a handler that wants a tighter limit wraps
// the body again and the smaller number wins.
const maxBody = 1 << 17 // 128KB

func capBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
		}
		next.ServeHTTP(w, r)
	})
}

// The content security policy.
//
// script-src is the line that matters. Everything else on this site is data
// rendered by React, which escapes it, and there is no innerHTML anywhere, so
// an injected string has no way to become a script today. This says it cannot
// become one tomorrow either.
//
// style-src has to allow inline styles: react-chessboard positions every piece
// with a style attribute, and so does every board this app draws. That is a
// real weakening and it is worth being clear about, but a style attribute
// cannot execute anything with script-src locked down.
//
// The two font entries are Google Fonts, which is the one third party the page
// talks to. frame-ancestors is the clickjacking one and matters more here than
// it looks: session cookies are SameSite=Lax, and a frame on this same site is
// not cross-site, so without this a page could frame the site and sit an
// invisible button over "delete this classroom".
const csp = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
	"font-src 'self' https://fonts.gstatic.com; " +
	"img-src 'self' data:; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"form-action 'self'; " +
	"frame-ancestors 'none'"

func (s *Server) secureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		// Without this a browser is free to guess that a stored string is
		// HTML and render it, which turns a text field into a script tag.
		h.Set("X-Content-Type-Options", "nosniff")
		// X-Frame-Options says the same thing as frame-ancestors for the
		// browsers that never learned frame-ancestors.
		h.Set("X-Frame-Options", "DENY")
		// A puzzle URL carries the filter that found it and a classroom URL
		// carries the room. Neither should travel to a site somebody clicks
		// through to.
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		// Only over HTTPS. Sending HSTS from a plaintext dev server would pin
		// localhost to https for six months on the developer's own browser,
		// which is a genuinely annoying thing to do to yourself.
		if s.secureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}
