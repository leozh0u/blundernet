// Package httpapi is the HTTP surface of the api service: JSON REST for
// game actions, a WebSocket for live updates, and the embedded frontend.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/engine"
	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/obs"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
)

const Version = "0.1.0"

// Enqueuer is the slice of the queue client this server needs; the
// narrow interface keeps handlers testable without SQS.
type Enqueuer interface {
	Enqueue(ctx context.Context, j queue.Job) error
}

// Deps is a struct rather than a positional list because the constructor had
// grown past the point where five bare arguments at a call site said anything
// about which was which.
type Deps struct {
	Games    *store.Games
	Archive  *store.Archive // nil disables the archive and stats
	Users    *store.Users   // nil disables accounts; the site still plays
	Puzzles  *store.Puzzles // nil disables puzzles; the rest of the site works
	Sessions *store.Sessions
	Jobs     Enqueuer
	Redis    *redis.Client
	Static   fs.FS

	// SecureCookies should be false only for local http development, because
	// a Secure cookie is dropped silently over plain http and the resulting
	// failure looks like the session layer is broken.
	SecureCookies bool
	SessionTTL    time.Duration

	// TrustProxy enables reading the client address from X-Forwarded-For.
	// Only true when something we control terminates the connection first,
	// since otherwise the header is attacker controlled.
	TrustProxy bool

	// Limits left at their zero value fall back to DefaultLimits.
	Limits Limits
}

type Server struct {
	games         *store.Games
	archive       *store.Archive
	users         *store.Users
	puzzles       *store.Puzzles
	seen          *store.Seen
	ranked        *store.Ranked
	streak        *store.Streak
	sessions      *store.Sessions
	jobs          Enqueuer
	rdb           *redis.Client
	static        fs.FS
	limiter       *store.Limiter
	limits        Limits
	secureCookies bool
	trustProxy    bool
	sessionTTL    time.Duration
	handler       http.Handler
}

func New(d Deps) *Server {
	if d.SessionTTL == 0 {
		d.SessionTTL = 30 * 24 * time.Hour
	}
	if d.Sessions == nil && d.Redis != nil {
		d.Sessions = store.NewSessions(d.Redis, d.SessionTTL)
	}
	s := &Server{
		games: d.Games, archive: d.Archive, users: d.Users, puzzles: d.Puzzles,
		sessions: d.Sessions,
		jobs:     d.Jobs, rdb: d.Redis, static: d.Static,
		secureCookies: d.SecureCookies, trustProxy: d.TrustProxy,
		sessionTTL: d.SessionTTL, limits: d.Limits.withDefaults(),
	}
	if d.Redis != nil {
		s.limiter = store.NewLimiter(d.Redis)
		s.seen = store.NewSeen(d.Redis)
		s.ranked = store.NewRanked(d.Redis)
		s.streak = store.NewStreak(d.Redis)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /version", s.handleVersion)
	mux.HandleFunc("POST /api/auth/signup", s.limit("auth", s.limits.Auth, s.handleSignup))
	mux.HandleFunc("POST /api/auth/login", s.limit("auth", s.limits.Auth, s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/games", s.limit("create", s.limits.CreateGame, s.handleCreate))
	mux.HandleFunc("GET /api/games/{id}", s.handleGet)
	mux.HandleFunc("POST /api/games/{id}/moves", s.limit("move", s.limits.Move, s.handleMove))
	mux.HandleFunc("POST /api/games/{id}/hint", s.limit("move", s.limits.Move, s.handleHint))
	mux.HandleFunc("POST /api/games/{id}/resign", s.handleResign)
	mux.HandleFunc("POST /api/games/{id}/review", s.limit("create", s.limits.CreateGame, s.handleReviewStart))
	mux.HandleFunc("GET /api/games/{id}/review", s.handleReview)
	mux.HandleFunc("GET /api/games/{id}/ws", s.handleWS)
	mux.HandleFunc("GET /api/puzzles", s.limit("puzzles", s.limits.Puzzles, s.handlePuzzleSearch))
	mux.HandleFunc("GET /api/puzzles/themes", s.handlePuzzleThemes)
	mux.HandleFunc("GET /api/puzzles/openings", s.handlePuzzleOpenings)
	mux.HandleFunc("GET /api/puzzles/failed", s.limit("puzzles", s.limits.Puzzles, s.handlePuzzleFailed))
	mux.HandleFunc("GET /api/puzzles/favourites", s.limit("puzzles", s.limits.Puzzles, s.handlePuzzleFavourites))
	mux.HandleFunc("POST /api/puzzles/{id}/favourite", s.limit("attempt", s.limits.Move, s.handlePuzzleFavourite))
	mux.HandleFunc("DELETE /api/puzzles/{id}/favourite", s.limit("attempt", s.limits.Move, s.handlePuzzleFavourite))
	mux.HandleFunc("GET /api/puzzles/ranked", s.limit("puzzles", s.limits.Puzzles, s.handleRankedNext))
	mux.HandleFunc("POST /api/puzzles/ranked/move", s.limit("move", s.limits.Move, s.handleRankedMove))
	mux.HandleFunc("GET /api/puzzles/ranked/me", s.handleRankedProfile)
	mux.HandleFunc("POST /api/puzzles/streak", s.limit("puzzles", s.limits.Puzzles, s.handleStreakStart))
	mux.HandleFunc("POST /api/puzzles/streak/move", s.limit("move", s.limits.Move, s.handleStreakMove))
	mux.HandleFunc("GET /api/puzzles/{id}", s.handlePuzzleByID)
	mux.HandleFunc("POST /api/puzzles/{id}/attempt", s.limit("attempt", s.limits.Move, s.handlePuzzleAttempt))
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/me/profile", s.handleProfile)
	mux.HandleFunc("GET /api/me/games", s.handleHistory)
	mux.HandleFunc("GET /api/status", s.handleStatusJSON)
	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /privacy", s.handlePrivacy)
	mux.HandleFunc("GET /terms", s.handleTerms)
	mux.Handle("GET /", spaHandler(d.Static))

	// Session resolution wraps the mux rather than sitting on each route, so
	// a route added later cannot forget it. It never rejects, which is what
	// keeps the anonymous paths working.
	s.handler = s.withUser(mux)
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.handler.ServeHTTP(w, r) }

// State is the wire representation of a game, shared by REST responses and
// WebSocket events so clients render from one shape.
type State struct {
	Type        string   `json:"type"`
	ID          string   `json:"id"`
	FEN         string   `json:"fen"`
	Moves       []string `json:"moves"`
	Turn        string   `json:"turn"`
	PlayerColor string   `json:"player_color"`
	Level       int      `json:"level"`
	Rated       bool     `json:"rated"`
	Status      string   `json:"status"`
	Result      string   `json:"result,omitempty"`
	Termination string   `json:"termination,omitempty"`
}

func ToState(g *game.Game) State {
	return State{
		Type: "state", ID: g.ID, FEN: g.FEN(), Moves: g.Moves, Turn: g.Turn(),
		PlayerColor: g.PlayerColor, Level: g.Level, Rated: g.Rated,
		Status: string(g.Status),
		Result: g.Result, Termination: g.Termination,
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.rdb.Ping(ctx).Err(); err != nil {
		http.Error(w, "redis unreachable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": Version})
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Color string `json:"color"`
		// "rated" puts the ladder in charge of the level and lets the result
		// move both the ladder and the rating. "learning" is a practice game:
		// you pick the level, hints are available, nothing is recorded
		// against you.
		Mode  string `json:"mode"`
		Level int    `json:"level"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Color == "" {
		req.Color = "white"
	}
	if req.Color != "white" && req.Color != "black" {
		httpError(w, http.StatusBadRequest, "color must be white or black")
		return
	}
	if req.Mode == "" {
		req.Mode = game.ModeRated
	}
	if req.Mode != game.ModeRated && req.Mode != game.ModeLearning {
		httpError(w, http.StatusBadRequest, "mode must be rated or learning")
		return
	}

	// Attached at creation, not at archival, because the worker writes the
	// archive for games ending in checkmate and has no session to ask. A
	// first-time visitor gets a guest account here rather than a prompt, so
	// their first game still counts towards a rating.
	user, err := s.ensureIdentity(w, r)
	if err != nil {
		// Losing this quietly means the player finishes a game that is never
		// rated and never appears in their history, with nothing logged.
		internalError(w, err)
		return
	}

	rated := req.Mode == game.ModeRated
	level := req.Level
	if rated {
		// The ladder decides, not the request. A level you can name in a
		// rated game is a rating you can farm.
		level = engine.DefaultLevel
		if user != nil && s.users != nil {
			if n, err := s.users.BotLevel(r.Context(), user.ID); err == nil {
				level = n
			} else {
				slog.Error("read bot level", "user", user.ID, "err", err)
			}
		}
	}
	g := game.New(uuid.NewString(), req.Color, level, rated)
	if user != nil {
		g.UserID = user.ID
	}
	if err := s.games.Create(r.Context(), g); err != nil {
		internalError(w, err)
		return
	}
	obs.GameCreated(req.Color)
	// Player chose black: the engine opens.
	if req.Color == "black" {
		if err := s.jobs.Enqueue(r.Context(), queue.Job{GameID: g.ID, Ply: 0}); err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, ToState(g))
}

// handleHint asks a worker what the player should play. Learning games only:
// a hint in a rated game is somebody else's move on your rating.
//
// The answer comes back over the same WebSocket the game already uses rather
// than in this response, because the search takes about as long as an engine
// move and holding an HTTP request open for it puts inference back on the
// request path, which is the one thing the queue exists to prevent.
func (s *Server) handleHint(w http.ResponseWriter, r *http.Request) {
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	if g.Rated {
		httpError(w, http.StatusForbidden, "hints are for learning games")
		return
	}
	if g.Status != game.StatusOngoing || g.Turn() != g.PlayerColor {
		httpError(w, http.StatusConflict, "not your move")
		return
	}
	if err := s.jobs.Enqueue(r.Context(), queue.Job{
		GameID: g.ID, Ply: g.Ply, Kind: queue.KindHint,
	}); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleReviewStart asks a worker to score a finished game. The review is one
// evaluation per position, which is engine work, so it goes on the queue like
// every other piece of engine work rather than blocking a request.
func (s *Server) handleReviewStart(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		httpError(w, http.StatusServiceUnavailable, "the archive is not configured")
		return
	}
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	if g.Status != game.StatusFinished {
		httpError(w, http.StatusConflict, "the game is not over")
		return
	}
	// Already done: nothing to queue, and the client can fetch it now.
	if _, ok, err := s.archive.GetReview(r.Context(), g.ID); err == nil && ok {
		w.WriteHeader(http.StatusOK)
		return
	}
	if err := s.jobs.Enqueue(r.Context(), queue.Job{
		GameID: g.ID, Ply: g.Ply, Kind: queue.KindReview,
	}); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

// handleReview returns the review, or 202 while it is still being worked out.
func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	if s.archive == nil {
		httpError(w, http.StatusServiceUnavailable, "the archive is not configured")
		return
	}
	review, ok, err := s.archive.GetReview(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such game")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if !ok {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	writeJSON(w, http.StatusOK, review)
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ToState(g))
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UCI string `json:"uci"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UCI == "" {
		httpError(w, http.StatusBadRequest, "body must be {\"uci\": \"e2e4\"}")
		return
	}
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	prevPly := g.Ply
	if err := g.ApplyMove(g.PlayerColor, req.UCI); err != nil {
		gameError(w, err)
		return
	}
	if err := s.games.Update(r.Context(), g, prevPly); err != nil {
		gameError(w, err)
		return
	}
	s.afterChange(r.Context(), g)
	writeJSON(w, http.StatusOK, ToState(g))
}

func (s *Server) handleResign(w http.ResponseWriter, r *http.Request) {
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	prevPly := g.Ply
	if err := g.Resign(g.PlayerColor); err != nil {
		gameError(w, err)
		return
	}
	if err := s.games.Update(r.Context(), g, prevPly); err != nil {
		gameError(w, err)
		return
	}
	s.afterChange(r.Context(), g)
	writeJSON(w, http.StatusOK, ToState(g))
}

// afterChange handles everything downstream of a successful state write:
// fan-out to watchers, engine hand-off, archival. Best-effort by design,
// the state in Redis is already authoritative.
func (s *Server) afterChange(ctx context.Context, g *game.Game) {
	if raw, err := json.Marshal(ToState(g)); err == nil {
		if err := s.games.Publish(ctx, g.ID, raw); err != nil {
			slog.Error("publish", "game", g.ID, "err", err)
		}
	}
	switch {
	case g.Status == game.StatusFinished:
		if s.archive == nil {
			return
		}
		if err := s.archive.SaveFinished(ctx, g); err != nil {
			slog.Error("archive", "game", g.ID, "err", err)
		}
	case g.Turn() == g.EngineColor():
		if err := s.jobs.Enqueue(ctx, queue.Job{GameID: g.ID, Ply: g.Ply}); err != nil {
			slog.Error("enqueue", "game", g.ID, "err", err)
		}
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.archive.Stats(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

var upgrader = websocket.Upgrader{
	// Same-origin in production (frontend is served by this binary); allow
	// cross-origin for local vite dev.
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.games.Get(r.Context(), id)
	if err != nil {
		gameError(w, err)
		return
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	obs.WSOpened()
	defer obs.WSClosed()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := s.games.Subscribe(ctx, id)

	// Reader exists only to detect the client going away.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// Initial snapshot, then pub/sub events. One writer goroutine total,
	// as gorilla requires.
	if raw, err := json.Marshal(ToState(g)); err == nil {
		if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
			return
		}
	}
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ping.C:
			if err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case raw, ok := <-events:
			if !ok {
				return
			}
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
		}
	}
}

// spaHandler serves the embedded frontend, falling back to index.html for
// client-side routes.
func spaHandler(static fs.FS) http.Handler {
	fileServer := http.FileServer(http.FS(static))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path != "/" {
			if _, err := fs.Stat(static, path[1:]); err == nil {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		index, err := fs.ReadFile(static, "index.html")
		if err != nil {
			http.Error(w, "frontend not built", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(index)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func internalError(w http.ResponseWriter, err error) {
	slog.Error("internal error", "err", err)
	httpError(w, http.StatusInternalServerError, "internal error")
}

func gameError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpError(w, http.StatusNotFound, "game not found")
	case errors.Is(err, game.ErrIllegalMove):
		httpError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, game.ErrNotYourTurn), errors.Is(err, game.ErrFinished),
		errors.Is(err, store.ErrConflict):
		httpError(w, http.StatusConflict, err.Error())
	default:
		internalError(w, err)
	}
}
