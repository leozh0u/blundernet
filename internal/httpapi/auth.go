package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"github.com/leozh0u/blundernet/internal/rating"
	"github.com/leozh0u/blundernet/internal/store"
)

const sessionCookie = "bn_session"

// Passwords are capped as well as floored. Argon2id reads the whole input, so
// an unbounded password is a cheap way to make the server do 64MB of work per
// megabyte submitted.
const (
	minPasswordLen = 8
	maxPasswordLen = 128
)

var usernamePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,20}$`)

type ctxKey int

const userCtxKey ctxKey = iota

// UserFrom returns the signed-in user, or nil. Anonymous play stays
// supported, so every caller has to handle nil rather than assuming a user.
func UserFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(userCtxKey).(*store.User)
	return u
}

// withUser resolves the session cookie onto the request context. It never
// rejects: routes that require a user check for themselves, and the rest of
// the site works signed out.
func (s *Server) withUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := s.lookupSession(r)
		if user != nil {
			r = r.WithContext(context.WithValue(r.Context(), userCtxKey, user))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) lookupSession(r *http.Request) *store.User {
	if s.users == nil {
		return nil
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil
	}
	userID, err := s.sessions.Lookup(r.Context(), c.Value)
	if err != nil {
		return nil
	}
	user, err := s.users.ByID(r.Context(), userID)
	if err != nil {
		return nil
	}
	return user
}

// secureCookies is off for local http development and on everywhere else.
// A Secure cookie is dropped silently over plain http, which makes local
// login fail in a way that looks like the session layer is broken.
func (s *Server) setSessionCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		// Lax rather than Strict. Strict would drop the cookie when someone
		// follows a link into the site and make them look signed out on
		// arrival. Lax still withholds it from cross-site POSTs, which is the
		// CSRF case that matters here.
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(maxAge.Seconds()),
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.secureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func (c credentials) validate() string {
	if !usernamePattern.MatchString(c.Username) {
		return "username must be 3 to 20 characters, letters, numbers, underscore or hyphen"
	}
	if len(c.Password) < minPasswordLen {
		return "password must be at least 8 characters"
	}
	if len(c.Password) > maxPasswordLen {
		return "password must be at most 128 characters"
	}
	return ""
}

// ensureIdentity returns the caller's user, creating a guest account if they
// have none. Anything that stores progress calls this, so a first-time visitor
// gets a rating and a game history without being asked to sign up. The row is
// a real user row, so signing up later fills in credentials rather than
// migrating anything.
func (s *Server) ensureIdentity(w http.ResponseWriter, r *http.Request) (*store.User, error) {
	if u := UserFrom(r.Context()); u != nil {
		return u, nil
	}
	if s.users == nil {
		return nil, nil // accounts disabled; play stays anonymous and unsaved
	}
	guest, err := s.users.CreateGuest(r.Context())
	if err != nil {
		return nil, err
	}
	token, err := s.sessions.Create(r.Context(), guest.ID)
	if err != nil {
		return nil, err
	}
	s.setSessionCookie(w, token, s.sessionTTL)
	return guest, nil
}

func (s *Server) handleSignup(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		httpError(w, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var creds credentials
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		httpError(w, http.StatusBadRequest, "body must be {\"username\": \"\", \"password\": \"\"}")
		return
	}
	if msg := creds.validate(); msg != "" {
		httpError(w, http.StatusBadRequest, msg)
		return
	}

	// Signing up while playing as a guest upgrades that account in place, so
	// the games and rating already earned carry over. Falling back to a fresh
	// account covers the guest row having been reaped in between.
	var user *store.User
	var err error
	if current := UserFrom(r.Context()); current != nil && current.IsGuest {
		user, err = s.users.Upgrade(r.Context(), current.ID, creds.Username, creds.Password)
		if errors.Is(err, store.ErrNotGuest) {
			httpError(w, http.StatusConflict, "that session already has an account")
			return
		}
	} else {
		user, err = s.users.Create(r.Context(), creds.Username, creds.Password)
	}
	if errors.Is(err, store.ErrUsernameTaken) {
		httpError(w, http.StatusConflict, "that username is taken")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	s.startSession(w, r, user, http.StatusCreated)
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		httpError(w, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	var creds credentials
	if err := json.NewDecoder(r.Body).Decode(&creds); err != nil {
		httpError(w, http.StatusBadRequest, "body must be {\"username\": \"\", \"password\": \"\"}")
		return
	}
	// Deliberately not validated the way signup is. Rejecting a short password
	// here would tell an attacker the rules without an account, and a
	// credential that cannot exist simply will not match.
	user, err := s.users.Authenticate(r.Context(), creds.Username, creds.Password)
	if errors.Is(err, store.ErrBadCredentials) {
		httpError(w, http.StatusUnauthorized, "wrong username or password")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	s.startSession(w, r, user, http.StatusOK)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *store.User, code int) {
	// Rotate: destroy whatever token the request arrived with before issuing a
	// new one. A guest token that survives the upgrade still resolves to the
	// row that is now a credentialed account, so anyone who planted that
	// cookie keeps access to it.
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.sessions.Destroy(r.Context(), c.Value); err != nil {
			slog.Warn("destroy previous session", "err", err)
		}
	}
	token, err := s.sessions.Create(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	s.setSessionCookie(w, token, s.sessionTTL)
	writeJSON(w, code, map[string]string{"id": user.ID, "username": user.Username})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		// Destroy the record, not just the cookie. Clearing the cookie alone
		// leaves a token that still works if it was captured.
		if err := s.sessions.Destroy(r.Context(), c.Value); err != nil {
			internalError(w, err)
			return
		}
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	if user == nil {
		writeJSON(w, http.StatusOK, map[string]any{"user": nil})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": map[string]any{
		"id": user.ID, "username": user.Username, "guest": user.IsGuest,
	}})
}

// readIdentity resolves the caller without creating anything. Read routes use
// this rather than ensureIdentity: minting an account is a side effect, and a
// GET that has one lets anyone fill the users table by looping over it.
func (s *Server) readIdentity(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	if s.archive == nil || s.users == nil {
		httpError(w, http.StatusServiceUnavailable, "accounts are not configured")
		return nil, false
	}
	return UserFrom(r.Context()), true
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	user, ok := s.readIdentity(w, r)
	if !ok {
		return
	}
	// Nobody here yet. Report the starting rating rather than creating an
	// account to look it up, so the frontend has something to render on a
	// first visit and a crawler costs nothing.
	if user == nil {
		writeJSON(w, http.StatusOK, &store.Profile{
			IsGuest: true, Rating: rating.DefaultRating,
			Deviation: rating.DefaultDeviation, Provisional: true,
		})
		return
	}
	profile, err := s.archive.Profile(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	user, ok := s.readIdentity(w, r)
	if !ok {
		return
	}
	if user == nil {
		writeJSON(w, http.StatusOK, map[string]any{"games": []store.HistoryEntry{}, "next_before": ""})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	// Keyset pagination: the caller passes back the finished_at of the last
	// row it saw rather than an offset.
	games, err := s.archive.History(r.Context(), user.ID, r.URL.Query().Get("before"), limit)
	if err != nil {
		internalError(w, err)
		return
	}
	var next string
	if len(games) > 0 {
		next = games[len(games)-1].FinishedAt
	}
	writeJSON(w, http.StatusOK, map[string]any{"games": games, "next_before": next})
}
