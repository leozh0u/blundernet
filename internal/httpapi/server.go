// Package httpapi is the HTTP surface of the api service: JSON REST for
// game actions, a WebSocket for live updates, and the embedded frontend.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"mime"
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
	feedback      *store.Feedback
	classrooms    *store.Classrooms
	imports       *store.Imports
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
	// Feedback shares the archive's pool rather than taking its own dependency,
	// because it is one table and a connection pool per table is not a design.
	if d.Archive != nil {
		s.feedback = store.NewFeedback(d.Archive.Pool())
		s.classrooms = store.NewClassrooms(d.Archive.Pool())
		s.imports = store.NewImports(d.Archive.Pool())
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
	// Same limiter as login: a recovery code is a bearer credential and
	// guessing it is the obvious attack on this endpoint.
	mux.HandleFunc("POST /api/auth/recover", s.limit("auth", s.limits.Auth, s.handleRecover))
	mux.HandleFunc("POST /api/auth/recovery-code", s.limit("auth", s.limits.Auth, s.handleNewRecoveryCode))
	mux.HandleFunc("POST /api/feedback", s.limit("auth", s.limits.Auth, s.handleFeedback))
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.HandleFunc("POST /api/games", s.limit("create", s.limits.CreateGame, s.handleCreate))
	mux.HandleFunc("GET /api/games/{id}", s.handleGet)
	mux.HandleFunc("POST /api/games/{id}/moves", s.limit("move", s.limits.Move, s.handleMove))
	mux.HandleFunc("POST /api/games/{id}/hint", s.limit("move", s.limits.Move, s.handleHint))
	mux.HandleFunc("POST /api/games/{id}/join", s.limit("create", s.limits.CreateGame, s.handleJoin))
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
	// A join code is a bearer credential, so joining is limited on the same
	// bucket as login rather than on the cheaper puzzle one.
	mux.HandleFunc("POST /api/classrooms", s.limit("create", s.limits.CreateGame, s.handleClassroomCreate))
	mux.HandleFunc("GET /api/classrooms", s.handleClassroomList)
	mux.HandleFunc("POST /api/classrooms/join", s.limit("auth", s.limits.Auth, s.handleClassroomJoin))
	mux.HandleFunc("GET /api/classrooms/{id}", s.handleClassroomGet)
	mux.HandleFunc("POST /api/classrooms/{id}/code", s.limit("create", s.limits.CreateGame, s.handleClassroomRotate))
	mux.HandleFunc("DELETE /api/classrooms/{id}/members/{user}", s.handleClassroomRemove)
	mux.HandleFunc("DELETE /api/classrooms/{id}", s.handleClassroomDelete)
	mux.HandleFunc("POST /api/classrooms/{id}/questions", s.limit("attempt", s.limits.Move, s.handleQuestionAsk))
	mux.HandleFunc("GET /api/classrooms/{id}/questions/open", s.handleQuestionOpen)
	mux.HandleFunc("POST /api/classrooms/{id}/questions/{question}/answer", s.limit("attempt", s.limits.Move, s.handleQuestionAnswer))
	mux.HandleFunc("POST /api/classrooms/{id}/questions/{question}/close", s.limit("attempt", s.limits.Move, s.handleQuestionClose))
	mux.HandleFunc("GET /api/classrooms/{id}/assignments", s.handleAssignmentList)
	mux.HandleFunc("POST /api/classrooms/{id}/assignments", s.limit("attempt", s.limits.Move, s.handleAssignmentSet))
	mux.HandleFunc("DELETE /api/classrooms/{id}/assignments/{assignment}", s.handleAssignmentDrop)
	// Reviewing a pasted game is open to anyone, and costs engine time, so it
	// sits on the create bucket rather than the loose puzzle one.
	mux.HandleFunc("POST /api/review/pgn", s.limit("create", s.limits.CreateGame, s.handleImportCreate))
	mux.HandleFunc("GET /api/review/{id}", s.handleImportReview)
	mux.HandleFunc("GET /api/stats", s.handleStats)
	mux.HandleFunc("GET /api/me/profile", s.handleProfile)
	mux.HandleFunc("GET /api/me/games", s.handleHistory)
	mux.HandleFunc("GET /api/me/weaknesses", s.limit("puzzles", s.limits.Puzzles, s.handleWeaknesses))
	mux.HandleFunc("GET /api/status", s.handleStatusJSON)
	mux.HandleFunc("GET /status", s.handleStatusPage)
	mux.HandleFunc("GET /privacy", s.handlePrivacy)
	mux.HandleFunc("GET /terms", s.handleTerms)
	mux.Handle("GET /", spaHandler(d.Static))

	// Session resolution wraps the mux rather than sitting on each route, so
	// a route added later cannot forget it. It never rejects, which is what
	// keeps the anonymous paths working.
	//
	// The body cap sits outermost so it applies before anything reads, and the
	// headers sit inside it so they are set on the 413 too.
	s.handler = capBody(s.secureHeaders(s.withUser(mux)))
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
	Friend      bool     `json:"friend,omitempty"`
	Waiting     bool     `json:"waiting,omitempty"`
	// The side this viewer plays. Only the responses to a request know it,
	// because a friend game has two people watching the same channel and the
	// broadcast cannot be addressed to either of them. The browser keeps what
	// it was told when it created or joined the game.
	You         string `json:"you,omitempty"`
	Status      string `json:"status"`
	Result      string `json:"result,omitempty"`
	Termination string `json:"termination,omitempty"`
}

// ToStateFor is ToState plus the one field that depends on who is asking.
func ToStateFor(g *game.Game, userID string) State {
	st := ToState(g)
	if colour, ok := g.ColorFor(userID); ok {
		st.You = colour
	}
	return st
}

func ToState(g *game.Game) State {
	return State{
		Type: "state", ID: g.ID, FEN: g.FEN(), Moves: g.Moves, Turn: g.Turn(),
		PlayerColor: g.PlayerColor, Level: g.Level, Rated: g.Rated,
		Friend: g.Friend, Waiting: g.Friend && g.OpponentID == "",
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
	if req.Mode != game.ModeRated && req.Mode != game.ModeLearning &&
		req.Mode != game.ModeFriend {
		httpError(w, http.StatusBadRequest, "mode must be rated, learning or friend")
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
	if req.Mode == game.ModeFriend {
		g.Friend = true
		g.Rated = false
	}
	if user != nil {
		g.UserID = user.ID
	}
	if err := s.games.Create(r.Context(), g); err != nil {
		internalError(w, err)
		return
	}
	obs.GameCreated(req.Color)
	// Player chose black: the engine opens. Nobody opens for them in a friend
	// game; the other person does.
	if req.Color == "black" && !g.Friend {
		if err := s.jobs.Enqueue(r.Context(), queue.Job{GameID: g.ID, Ply: 0}); err != nil {
			internalError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, ToStateFor(g, userIDOf(r)))
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
	writeJSON(w, http.StatusOK, ToStateFor(g, userIDOf(r)))
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
	color, allowed := g.ColorFor(userIDOf(r))
	if !allowed {
		httpError(w, http.StatusForbidden, "this is not your game")
		return
	}
	prevPly := g.Ply
	if err := g.ApplyMove(color, req.UCI); err != nil {
		gameError(w, err)
		return
	}
	if err := s.games.Update(r.Context(), g, prevPly); err != nil {
		gameError(w, err)
		return
	}
	s.afterChange(r.Context(), g)
	writeJSON(w, http.StatusOK, ToStateFor(g, userIDOf(r)))
}

// handleJoin takes the second seat in a friend game. First to open the link
// gets it; anyone after that is watching, which is the honest outcome of
// sharing a link rather than an invitation to fight over the board.
func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	if !g.Friend {
		httpError(w, http.StatusConflict, "this game is against the bot")
		return
	}
	user, err := s.ensureIdentity(w, r)
	if err != nil {
		internalError(w, err)
		return
	}
	if user == nil {
		httpError(w, http.StatusServiceUnavailable, "accounts are not configured")
		return
	}
	// Already seated, either side. Nothing to do and no error to report.
	if user.ID == g.UserID || user.ID == g.OpponentID {
		writeJSON(w, http.StatusOK, ToStateFor(g, user.ID))
		return
	}
	if g.OpponentID != "" {
		httpError(w, http.StatusConflict, "both seats are taken")
		return
	}
	prevPly := g.Ply
	g.OpponentID = user.ID
	if err := s.games.Update(r.Context(), g, prevPly); err != nil {
		gameError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ToStateFor(g, user.ID))
}

// userIDOf is the caller's id, or empty when signed out.
func userIDOf(r *http.Request) string {
	if u := UserFrom(r.Context()); u != nil {
		return u.ID
	}
	return ""
}

func (s *Server) handleResign(w http.ResponseWriter, r *http.Request) {
	g, err := s.games.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		gameError(w, err)
		return
	}
	// Same seat check as a move, and for the same reason. Without it anyone
	// holding a game id could end somebody else's game as a loss, and a rated
	// loss moves their rating: a stranger could take three hundred points off
	// a player by posting to this route. Friend game ids travel in links by
	// design, so "hard to guess" was never the protection here.
	//
	// Resigning the caller's own colour rather than g.PlayerColor also fixes
	// the friend game case, where the second player pressing resign used to
	// resign the first player's side and hand themselves the win.
	color, allowed := g.ColorFor(userIDOf(r))
	if !allowed {
		httpError(w, http.StatusForbidden, "this is not your game")
		return
	}
	prevPly := g.Ply
	if err := g.Resign(color); err != nil {
		gameError(w, err)
		return
	}
	if err := s.games.Update(r.Context(), g, prevPly); err != nil {
		gameError(w, err)
		return
	}
	s.afterChange(r.Context(), g)
	writeJSON(w, http.StatusOK, ToStateFor(g, userIDOf(r)))
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
	case !g.Friend && g.Turn() == g.EngineColor():
		if err := s.jobs.Enqueue(ctx, queue.Job{GameID: g.ID, Ply: g.Ply}); err != nil {
			slog.Error("enqueue", "game", g.ID, "err", err)
		}
	}
}

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	// Every other handler checks its dependency and this one did not, so a
	// binary started without Postgres answered /api/stats with a segfault
	// instead of a 503. Nothing reaches it in production, where the archive is
	// always wired, but "nothing reaches it" is not a reason to be the one
	// handler that crashes the process.
	if s.archive == nil {
		httpError(w, http.StatusServiceUnavailable, "stats are not configured")
		return
	}
	stats, err := s.archive.Stats(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	g, err := s.games.Get(r.Context(), id)
	if err != nil {
		gameError(w, err)
		return
	}
	// Built per server rather than as a package var, because the origin rule
	// needs to know whether this is production. See sameOrigin.
	upgrader := websocket.Upgrader{CheckOrigin: s.sameOrigin}
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
	if raw, err := json.Marshal(ToStateFor(g, userIDOf(r))); err == nil {
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

// Go's mime table has no entry for .webmanifest, so the file would go out as
// text/plain and some browsers refuse to parse it. Registered once at load.
func init() {
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
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
