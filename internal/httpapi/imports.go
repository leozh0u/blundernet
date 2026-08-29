package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/review"
	"github.com/leozh0u/blundernet/internal/store"
)

// Reviewing a game played somewhere else.
//
// Open to people who are not signed in, deliberately. It is the most useful
// thing this site can do for a stranger who arrived from a link, and putting a
// signup in front of it would mean nobody ever finds out.

// maxPGN is generous for a game and small enough that the parser is never
// handed a file.
const maxPGN = 1 << 16

func (s *Server) handleImportCreate(w http.ResponseWriter, r *http.Request) {
	if s.imports == nil || s.jobs == nil {
		httpError(w, http.StatusServiceUnavailable, "reviews are not configured")
		return
	}
	var body struct {
		PGN string `json:"pgn"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPGN)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}

	moves, err := review.ParsePGN(body.PGN)
	switch {
	case errors.Is(err, review.ErrNoMoves):
		httpError(w, http.StatusBadRequest, "no moves found in that game")
		return
	case errors.Is(err, review.ErrTooLong):
		httpError(w, http.StatusBadRequest, "that game is too long to review")
		return
	case err != nil:
		httpError(w, http.StatusBadRequest, "that does not look like a chess game")
		return
	}

	// The review is attached to an account when there is one, so it can be
	// found again, but never creates one.
	var owner string
	if u := UserFrom(r.Context()); u != nil {
		owner = u.ID
	}
	id, err := s.imports.Create(r.Context(), owner, moves)
	if err != nil {
		internalError(w, err)
		return
	}
	if err := s.jobs.Enqueue(r.Context(), queue.Job{GameID: id, Kind: queue.KindImport}); err != nil {
		internalError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"id": id, "moves": len(moves)})
}

// handleImportReview answers with the review, or 202 while the worker is still
// on it, which is the same contract the game review already uses.
func (s *Server) handleImportReview(w http.ResponseWriter, r *http.Request) {
	if s.imports == nil {
		httpError(w, http.StatusServiceUnavailable, "reviews are not configured")
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	out, done, err := s.imports.Review(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		httpError(w, http.StatusNotFound, "no such review")
		return
	}
	if err != nil {
		internalError(w, err)
		return
	}
	if !done {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}
