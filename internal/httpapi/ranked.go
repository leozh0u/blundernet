package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

// Ranked is the test: one puzzle at your level, no filters, no hints, no
// second try, and the rating moves both ways. It is the first thing on the
// site that genuinely needs an account, which makes it the honest place to
// ask for one. Everything else stays free and signed out.
//
// The solution never leaves the server here. Moves are posted one at a time
// and graded against the stored line, so the browser learns the answer only
// once the attempt is over.

// The window the ranked puzzle is drawn from. Wide enough that there is
// always something to serve at any rating, narrow enough that the result says
// something. Lichess uses a comparable band.
const (
	rankedBandBelow = 100
	rankedBandAbove = 250
)

type rankedView struct {
	ID        string `json:"id"`
	FEN       string `json:"fen"`
	SetupMove string `json:"setup_move"`
	Color     string `json:"color"`
	Moves     int    `json:"moves"`
	Step      int    `json:"step"`
	// The puzzle's rating is deliberately absent until the attempt is over.
	// Seeing "2100" before starting changes how hard somebody tries, and the
	// number is supposed to be measuring them rather than the other way round.
	Rating *int `json:"rating,omitempty"`
}

// rankedUser resolves the caller and refuses guests. A guest account is free
// to mint, so letting one hold a rating would make the leaderboard a list of
// throwaway sessions.
func (s *Server) rankedUser(w http.ResponseWriter, r *http.Request) (*store.User, bool) {
	if s.puzzles == nil || s.ranked == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return nil, false
	}
	u := UserFrom(r.Context())
	if u == nil || u.IsGuest {
		httpError(w, http.StatusUnauthorized, "ranked puzzles need an account")
		return nil, false
	}
	return u, true
}

// handleRankedNext hands back the puzzle in progress, or draws a new one.
//
// Returning the one in progress is the point: if a fresh puzzle arrived on
// every request, an unwanted one could be reloaded away until an easy one
// turned up, and the rating would measure persistence rather than tactics.
func (s *Server) handleRankedNext(w http.ResponseWriter, r *http.Request) {
	user, ok := s.rankedUser(w, r)
	if !ok {
		return
	}

	state, found, err := s.ranked.Get(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if found {
		p, ok, err := s.puzzles.ByID(r.Context(), state.PuzzleID)
		if err != nil {
			internalError(w, err)
			return
		}
		if ok {
			writeJSON(w, http.StatusOK, toRankedView(p, state.Step))
			return
		}
		// The puzzle went away under the attempt, which only happens if the
		// corpus was reloaded mid-solve. Drop it and draw another.
		if err := s.ranked.Clear(r.Context(), user.ID); err != nil {
			internalError(w, err)
			return
		}
	}

	rating, _, _, err := s.puzzles.PuzzleRating(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	seen, err := s.seen.Load(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	p, found, err := s.drawRanked(r, int(rating), seen)
	if err != nil {
		internalError(w, err)
		return
	}
	if !found {
		httpError(w, http.StatusNotFound, "no puzzle left at your rating")
		return
	}
	if err := s.ranked.Set(r.Context(), user.ID, store.InProgress{
		PuzzleID: p.ID, Step: 0, Started: time.Now(),
	}); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toRankedView(p, 0))
}

// drawRanked widens the band rather than giving up. The first window is the
// one that makes a rating mean something; the rest are there so somebody at
// the top or bottom of the scale, or somebody who has seen everything nearby,
// still gets a puzzle instead of an error.
func (s *Server) drawRanked(r *http.Request, rating int, seen map[string]bool) (puzzle.Puzzle, bool, error) {
	windows := []struct{ below, above int }{
		{rankedBandBelow, rankedBandAbove},
		{rankedBandBelow * 4, rankedBandAbove * 4},
	}
	for _, w := range windows {
		f := store.Filter{MinRating: rating - w.below, MaxRating: rating + w.above}
		p, found, err := s.puzzles.One(r.Context(), f, func(id string) bool { return seen[id] })
		if err != nil || found {
			return p, found, err
		}
	}
	// Anything at all, which at six million puzzles only happens to somebody
	// who has genuinely run out of new ones near their level.
	return s.puzzles.One(r.Context(), store.Filter{}, func(id string) bool { return seen[id] })
}

func toRankedView(p puzzle.Puzzle, step int) rankedView {
	return rankedView{
		ID: p.ID, FEN: p.FEN, SetupMove: p.SetupMove(), Color: p.SolverColor(),
		Moves: p.UserMoves(), Step: step,
	}
}

type rankedMoveRequest struct {
	UCI string `json:"uci"`
	Ms  int    `json:"ms"`
}

type rankedMoveResponse struct {
	Correct     bool                 `json:"correct"`
	Done        bool                 `json:"done"`
	Reply       string               `json:"reply,omitempty"`
	Solution    []string             `json:"solution,omitempty"`
	Explanation *puzzle.Explanation  `json:"explanation,omitempty"`
	Rating      *store.RankedOutcome `json:"result,omitempty"`
	PuzzleRatng int                  `json:"puzzle_rating,omitempty"`
}

// handleRankedMove grades one move.
func (s *Server) handleRankedMove(w http.ResponseWriter, r *http.Request) {
	user, ok := s.rankedUser(w, r)
	if !ok {
		return
	}
	var req rankedMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UCI == "" {
		httpError(w, http.StatusBadRequest, "a move is required")
		return
	}

	state, found, err := s.ranked.Get(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if !found {
		httpError(w, http.StatusConflict, "no ranked puzzle in progress")
		return
	}
	p, exists, err := s.puzzles.ByID(r.Context(), state.PuzzleID)
	if err != nil {
		internalError(w, err)
		return
	}
	if !exists {
		_ = s.ranked.Clear(r.Context(), user.ID)
		httpError(w, http.StatusConflict, "that puzzle is gone")
		return
	}

	correct, done := puzzle.Check(p, state.Step, req.UCI)
	if correct && !done {
		// The opponent's forced reply is the next ply, and the solver is up
		// again after it.
		reply := p.Solution()[state.Step+1]
		if err := s.ranked.Set(r.Context(), user.ID, store.InProgress{
			PuzzleID: state.PuzzleID, Step: state.Step + 2, Started: state.Started,
		}); err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, rankedMoveResponse{Correct: true, Reply: reply})
		return
	}

	// Either it is over or it was wrong, and both end the attempt. There is no
	// second try in ranked; that is what learning mode is for.
	out, err := s.puzzles.RecordRanked(r.Context(), user.ID, p.ID, correct, req.Ms)
	if err != nil {
		internalError(w, err)
		return
	}
	if err := s.ranked.Clear(r.Context(), user.ID); err != nil {
		slog.Warn("clear ranked attempt", "user", user.ID, "err", err)
	}
	if err := s.seen.Add(r.Context(), user.ID, p.ID); err != nil {
		slog.Warn("mark puzzle seen", "puzzle", p.ID, "err", err)
	}

	resp := rankedMoveResponse{
		Correct: correct, Done: true, Solution: p.Solution(),
		Rating: &out, PuzzleRatng: int(out.PuzzleRating),
	}
	if e, ok := puzzle.Explain(p); ok {
		resp.Explanation = &e
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleRankedProfile is the ranked scoreboard for one person: their puzzle
// rating and how many they have solved.
func (s *Server) handleRankedProfile(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	u := UserFrom(r.Context())
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]any{"signed_in": false})
		return
	}
	rating, dev, solved, err := s.puzzles.PuzzleRating(r.Context(), u.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"signed_in":        !u.IsGuest,
		"guest":            u.IsGuest,
		"rating":           rating,
		"rating_deviation": dev,
		"solved":           solved,
	})
}
