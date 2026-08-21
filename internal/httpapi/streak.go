package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

// Streak mode. Puzzles climb in difficulty until you miss one, and then the
// run is over. No hints, no second try, and no rating: what is kept is the
// length of the best run.
//
// It runs on the same server-held machinery as ranked, because the browser
// holding the solution would make the number meaningless. Guests can play,
// which is the difference from ranked: a streak is not a rating, so there is
// nothing to protect from a throwaway account.

type streakView struct {
	ID        string `json:"id"`
	FEN       string `json:"fen"`
	SetupMove string `json:"setup_move"`
	Color     string `json:"color"`
	Moves     int    `json:"moves"`
	Step      int    `json:"step"`
	Count     int    `json:"count"`
	Best      int    `json:"best"`
}

// handleStreakStart returns the run in progress, or begins one. It is a POST
// because starting a run creates state, and for a signed out visitor it also
// creates the guest account that holds it.
func (s *Server) handleStreakStart(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil || s.streak == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
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

	state, found, err := s.streak.Get(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if found {
		// A run with a puzzle still on it is resumed rather than redrawn: a
		// fresh puzzle on every request would let somebody reload past
		// anything they did not like.
		if state.PuzzleID != "" {
			p, ok, err := s.puzzles.ByID(r.Context(), state.PuzzleID)
			if err != nil {
				internalError(w, err)
				return
			}
			if ok {
				s.writeStreak(w, r, user.ID, p, state)
				return
			}
		}
		// A run between puzzles, which is where a solve leaves it. The next
		// one is drawn against the run's own state, so the count survives and
		// the rung is one higher. Starting a fresh state here was the second
		// half of the loop bug: even once the id was cleared, the run would
		// have restarted at zero.
		s.nextStreakPuzzle(w, r, user.ID, state)
		return
	}
	s.nextStreakPuzzle(w, r, user.ID, store.StreakState{Started: time.Now()})
}

// nextStreakPuzzle draws the puzzle for the current rung and stores it.
func (s *Server) nextStreakPuzzle(w http.ResponseWriter, r *http.Request, userID string, state store.StreakState) {
	target := state.RatingFor()
	seen, err := s.seen.Load(r.Context(), userID)
	if err != nil {
		internalError(w, err)
		return
	}
	// A narrow band around the rung, widened once if it comes back empty at
	// the very top of the ladder.
	for _, span := range []int{60, 250} {
		f := store.Filter{MinRating: target - span, MaxRating: target + span}
		p, ok, err := s.puzzles.One(r.Context(), f, func(id string) bool { return seen[id] })
		if err != nil {
			internalError(w, err)
			return
		}
		if ok {
			state.PuzzleID = p.ID
			state.Step = 0
			if err := s.streak.Set(r.Context(), userID, state); err != nil {
				internalError(w, err)
				return
			}
			s.writeStreak(w, r, userID, p, state)
			return
		}
	}
	httpError(w, http.StatusNotFound, "no puzzle left at that level")
}

func (s *Server) writeStreak(w http.ResponseWriter, r *http.Request, userID string, p puzzle.Puzzle, state store.StreakState) {
	best, err := s.puzzles.BestStreak(r.Context(), userID)
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, streakView{
		ID: p.ID, FEN: p.FEN, SetupMove: p.SetupMove(), Color: p.SolverColor(),
		Moves: p.UserMoves(), Step: state.Step, Count: state.Count, Best: best,
	})
}

type streakMoveResponse struct {
	Correct  bool     `json:"correct"`
	Done     bool     `json:"done"`
	Reply    string   `json:"reply,omitempty"`
	Solution []string `json:"solution,omitempty"`
	Count    int      `json:"count"`
	Best     int      `json:"best,omitempty"`
	Over     bool     `json:"over"`
}

// handleStreakMove grades one move. A miss ends the run there and then.
func (s *Server) handleStreakMove(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil || s.streak == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	user := UserFrom(r.Context())
	if user == nil {
		httpError(w, http.StatusConflict, "no run in progress")
		return
	}
	var req rankedMoveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UCI == "" {
		httpError(w, http.StatusBadRequest, "a move is required")
		return
	}

	state, found, err := s.streak.Get(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	if !found {
		httpError(w, http.StatusConflict, "no run in progress")
		return
	}
	p, exists, err := s.puzzles.ByID(r.Context(), state.PuzzleID)
	if err != nil {
		internalError(w, err)
		return
	}
	if !exists {
		_ = s.streak.Clear(r.Context(), user.ID)
		httpError(w, http.StatusConflict, "that puzzle is gone")
		return
	}

	correct, done := puzzle.Check(p, state.Step, req.UCI)
	if correct && !done {
		state.Step += 2
		if err := s.streak.Set(r.Context(), user.ID, state); err != nil {
			internalError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, streakMoveResponse{
			Correct: true, Reply: p.Solution()[state.Step-1], Count: state.Count,
		})
		return
	}

	if err := s.puzzles.RecordStreakAttempt(r.Context(), user.ID, p.ID, correct, req.Ms); err != nil {
		internalError(w, err)
		return
	}
	if err := s.seen.Add(r.Context(), user.ID, p.ID); err != nil {
		slog.Warn("mark puzzle seen", "puzzle", p.ID, "err", err)
	}

	if !correct {
		best, err := s.puzzles.SaveBestStreak(r.Context(), user.ID, state.Count)
		if err != nil {
			internalError(w, err)
			return
		}
		if err := s.streak.Clear(r.Context(), user.ID); err != nil {
			slog.Warn("clear streak", "user", user.ID, "err", err)
		}
		writeJSON(w, http.StatusOK, streakMoveResponse{
			Correct: false, Done: true, Over: true,
			Solution: p.Solution(), Count: state.Count, Best: best,
		})
		return
	}

	// Solved: the run is one longer and the next puzzle is one rung harder.
	//
	// Clearing PuzzleID is the part that matters. It used to be left pointing
	// at the puzzle just solved, and the next request found a run in progress,
	// looked that id up, and handed back the same puzzle again. A run could
	// therefore never leave its first puzzle. Reported from the live site.
	state.Count++
	state.Step = 0
	state.PuzzleID = ""
	if err := s.streak.Set(r.Context(), user.ID, state); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, streakMoveResponse{
		Correct: true, Done: true, Count: state.Count,
	})
}
