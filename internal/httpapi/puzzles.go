package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/leozh0u/blundernet/internal/puzzle"
	"github.com/leozh0u/blundernet/internal/store"
)

// Learning mode is a drill: filters, hints, an explanation every time, and no
// rating. It works signed out, so nothing here requires an account, and the
// solution travels with the puzzle so solving is instant and offline once the
// batch is fetched.
//
// Ranked will not do that. There the solution stays on the server and moves
// are checked one at a time, because a rating that moves is worth protecting
// and a drill is not.
type puzzleView struct {
	ID          string              `json:"id"`
	FEN         string              `json:"fen"`
	SetupMove   string              `json:"setup_move"`
	Solution    []string            `json:"solution,omitempty"`
	Color       string              `json:"color"`
	Rating      int                 `json:"rating"`
	Moves       int                 `json:"moves"`
	Phase       string              `json:"phase"`
	Themes      []string            `json:"themes"`
	GameURL     string              `json:"game_url,omitempty"`
	Explanation *puzzle.Explanation `json:"explanation,omitempty"`
}

func toPuzzleView(p puzzle.Puzzle, withSolution bool) puzzleView {
	v := puzzleView{
		ID: p.ID, FEN: p.FEN, SetupMove: p.SetupMove(), Color: p.SolverColor(),
		Rating: int(p.Rating), Moves: p.UserMoves(), Phase: p.Phase,
		Themes: p.Themes, GameURL: p.GameURL,
	}
	if v.Themes == nil {
		v.Themes = []string{}
	}
	if withSolution {
		v.Solution = p.Solution()
		// Derived here rather than in the browser so ranked mode, which never
		// ships a solution, can use the same sentence later. A row whose moves
		// do not replay gets no explanation instead of a wrong one.
		if e, ok := puzzle.Explain(p); ok {
			v.Explanation = &e
		}
	}
	return v
}

const (
	defaultBatch = 10
	maxBatch     = 50
)

// handlePuzzleSearch is the drill. Everything is a query parameter so a filter
// set is a URL, which is what makes "another like this" a link rather than a
// feature.
func (s *Server) handlePuzzleSearch(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	q := r.URL.Query()
	f := store.Filter{
		MinRating:     atoiDefault(q.Get("rating_min"), 0),
		MaxRating:     atoiDefault(q.Get("rating_max"), 0),
		Phases:        splitList(q.Get("phase")),
		Themes:        splitList(q.Get("theme")),
		MinPopularity: atoiDefault(q.Get("popularity_min"), 0),
	}
	for _, p := range f.Phases {
		if p != puzzle.PhaseOpening && p != puzzle.PhaseMiddlegame && p != puzzle.PhaseEndgame {
			httpError(w, http.StatusBadRequest, "unknown phase "+p)
			return
		}
	}
	// Lengths arrive in solver moves because that is what a player counts.
	// A missing upper bound means "or more", which is how the last option in
	// the filter reads.
	if n := atoiDefault(q.Get("moves_min"), 0); n > 0 {
		f.MinPlies = puzzle.PliesFor(n)
	}
	if n := atoiDefault(q.Get("moves_max"), 0); n > 0 {
		f.MaxPlies = puzzle.PliesFor(n)
	}
	if f.MinPlies > 0 && f.MaxPlies > 0 && f.MinPlies > f.MaxPlies {
		httpError(w, http.StatusBadRequest, "moves_min is above moves_max")
		return
	}

	limit := atoiDefault(q.Get("limit"), defaultBatch)
	if limit < 1 {
		limit = 1
	}
	if limit > maxBatch {
		limit = maxBatch
	}

	// Signed out visitors have no seen set, which is the honest consequence of
	// not asking them for an account: the sampler can repeat itself for them.
	var seen map[string]bool
	if u := UserFrom(r.Context()); u != nil && s.seen != nil {
		var err error
		seen, err = s.seen.Load(r.Context(), u.ID)
		if err != nil {
			internalError(w, err)
			return
		}
	}

	found, err := s.puzzles.Select(r.Context(), f, limit, func(id string) bool {
		return seen[id]
	})
	if err != nil {
		internalError(w, err)
		return
	}

	out := make([]puzzleView, 0, len(found))
	for _, p := range found {
		out = append(out, toPuzzleView(p, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"puzzles": out})
}

func (s *Server) handlePuzzleByID(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	p, ok, err := s.puzzles.ByID(r.Context(), r.PathValue("id"))
	if err != nil {
		internalError(w, err)
		return
	}
	if !ok {
		httpError(w, http.StatusNotFound, "no such puzzle")
		return
	}
	writeJSON(w, http.StatusOK, toPuzzleView(p, true))
}

type attemptRequest struct {
	Solved bool `json:"solved"`
	Ms     int  `json:"ms"`
	Hints  int  `json:"hints"`
}

// handlePuzzleAttempt records the outcome. It mints a guest account the same
// way finishing a game does, because the wrong-answer drill list is the reason
// to come back and it should not be gated behind signing up.
func (s *Server) handlePuzzleAttempt(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	var req attemptRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(w, http.StatusBadRequest, "malformed request")
		return
	}
	id := r.PathValue("id")
	if _, ok, err := s.puzzles.ByID(r.Context(), id); err != nil {
		internalError(w, err)
		return
	} else if !ok {
		httpError(w, http.StatusNotFound, "no such puzzle")
		return
	}

	user, err := s.ensureIdentity(w, r)
	if err != nil {
		internalError(w, err)
		return
	}
	if user == nil {
		// Accounts are switched off, so there is nowhere to record this. The
		// drill still works; it just forgets.
		writeJSON(w, http.StatusOK, map[string]any{"recorded": false})
		return
	}

	if err := s.puzzles.Record(r.Context(), store.Attempt{
		UserID: user.ID, PuzzleID: id, Solved: req.Solved,
		Ms: req.Ms, Mode: store.ModeLearning, Hints: req.Hints,
	}); err != nil {
		internalError(w, err)
		return
	}
	if err := s.seen.Add(r.Context(), user.ID, id); err != nil {
		// The attempt is already durable; a missing seen entry costs a repeat.
		slog.Warn("mark puzzle seen", "puzzle", id, "err", err)
	}
	writeJSON(w, http.StatusOK, map[string]any{"recorded": true})
}

// handlePuzzleFailed is the drill list: the puzzles you got wrong and have not
// since got right. This is the part that makes the site worth a second visit,
// and it is why attempts are rows rather than a counter.
func (s *Server) handlePuzzleFailed(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	user := UserFrom(r.Context())
	if user == nil {
		// Nobody has failed anything yet, which is not an error. A signed out
		// visitor has no history because there is nothing to attach it to.
		writeJSON(w, http.StatusOK, map[string]any{"puzzles": []puzzleView{}})
		return
	}
	limit := atoiDefault(r.URL.Query().Get("limit"), defaultBatch)
	if limit < 1 {
		limit = 1
	}
	if limit > maxBatch {
		limit = maxBatch
	}
	ids, err := s.puzzles.Failed(r.Context(), user.ID, limit)
	if err != nil {
		internalError(w, err)
		return
	}
	found, err := s.puzzles.ByIDs(r.Context(), ids)
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]puzzleView, 0, len(found))
	for _, p := range found {
		out = append(out, toPuzzleView(p, true))
	}
	writeJSON(w, http.StatusOK, map[string]any{"puzzles": out})
}

// handlePuzzleThemes lists the themes worth offering as filters, with how many
// puzzles each one has. The counts come from the same summary the sampler
// uses, so the filter menu cannot offer something the sampler cannot serve.
func (s *Server) handlePuzzleThemes(w http.ResponseWriter, r *http.Request) {
	if s.puzzles == nil {
		httpError(w, http.StatusServiceUnavailable, "puzzles are not configured")
		return
	}
	themes, err := s.puzzles.Themes(r.Context())
	if err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"themes": themes})
}

func splitList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}
