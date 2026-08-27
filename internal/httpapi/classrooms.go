package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/leozh0u/blundernet/internal/store"
)

// Classrooms. A coach opens a room, reads out a six character code, and then
// sees what the class is actually getting wrong.
//
// The handlers here are thin on purpose. Not one of them decides who may see
// what: every store call takes the caller's id and refuses by itself, so a
// route added later cannot forget the rule. What is left here is turning
// errors into status codes, and that mapping is the one piece of security
// thinking the HTTP layer still owns.

type classroomView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	JoinCode string `json:"join_code,omitempty"` // coaches only
	Role     string `json:"role"`
	Members  int    `json:"members"`
}

type memberView struct {
	UserID     string  `json:"user_id"`
	Username   string  `json:"username"`
	Role       string  `json:"role"`
	Rating     int     `json:"rating"`
	Attempts   int     `json:"attempts"`
	Solved     int     `json:"solved"`
	LastActive *string `json:"last_active"`
}

func viewOf(c *store.Classroom) classroomView {
	return classroomView{
		ID: c.ID, Name: c.Name, JoinCode: c.JoinCode,
		Role: c.Role, Members: c.Members,
	}
}

func viewOfMember(m store.Member) memberView {
	v := memberView{
		UserID: m.UserID, Username: m.Username, Role: m.Role,
		Rating: int(m.PuzzleRating), Attempts: m.Attempts, Solved: m.Solved,
	}
	if m.LastActive != nil {
		s := m.LastActive.UTC().Format(time.RFC3339)
		v.LastActive = &s
	}
	return v
}

// classroomError maps a store refusal onto a status code.
//
// The one worth explaining is ErrNotAMember, which is a 404 rather than a 403.
// A 403 would confirm that a classroom with that id exists, so anybody holding
// a guessed id could tell real rooms from imaginary ones. To someone who is
// not in it, a classroom does not exist. ErrNotCoach can be a 403, because by
// then the caller is already in the room and there is nothing left to leak.
func classroomError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotAMember), errors.Is(err, store.ErrNoSuchCode):
		httpError(w, http.StatusNotFound, "no classroom with that code")
	case errors.Is(err, store.ErrNotCoach):
		httpError(w, http.StatusForbidden, "only a coach can do that")
	case errors.Is(err, store.ErrGuestUser):
		httpError(w, http.StatusUnauthorized, "a classroom needs an account")
	case errors.Is(err, store.ErrAlreadyIn):
		httpError(w, http.StatusConflict, "you are already in that classroom")
	case errors.Is(err, store.ErrLastCoach):
		httpError(w, http.StatusConflict, "a classroom keeps at least one coach")
	case errors.Is(err, store.ErrBadName):
		httpError(w, http.StatusBadRequest, "a classroom name is 1 to 60 characters")
	default:
		internalError(w, err)
	}
}

// requireAccount is the gate for everything in this file. Unlike the rest of
// the site, a classroom will not quietly mint a guest to hold your place:
// membership has to still mean something next week.
func (s *Server) requireAccount(w http.ResponseWriter) func(*http.Request) *store.User {
	return func(r *http.Request) *store.User {
		if s.classrooms == nil {
			httpError(w, http.StatusServiceUnavailable, "classrooms are not configured")
			return nil
		}
		u := UserFrom(r.Context())
		if u == nil || u.IsGuest {
			httpError(w, http.StatusUnauthorized, "sign in to use classrooms")
			return nil
		}
		return u
	}
}

func (s *Server) handleClassroomCreate(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	// Length is not checked here. The store trims and checks, and a second
	// copy of the rule in front of it is a second copy to disagree with.
	room, err := s.classrooms.Create(r.Context(), user.ID, body.Name)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, viewOf(room))
}

func (s *Server) handleClassroomList(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	rooms, err := s.classrooms.ForUser(r.Context(), user.ID)
	if err != nil {
		internalError(w, err)
		return
	}
	out := make([]classroomView, 0, len(rooms))
	for i := range rooms {
		out = append(out, viewOf(&rooms[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"classrooms": out})
}

// handleClassroomJoin is rate limited on the auth bucket. A six character code
// is a bearer credential, and guessing one is the obvious attack on this
// route, exactly as it is for a password or a recovery code.
func (s *Server) handleClassroomJoin(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	var body struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	room, err := s.classrooms.Join(r.Context(), user.ID, body.Code)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, viewOf(room))
}

func (s *Server) handleClassroomGet(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	room, members, err := s.classrooms.Roster(r.Context(), r.PathValue("id"), user.ID)
	if err != nil {
		classroomError(w, err)
		return
	}
	out := make([]memberView, 0, len(members))
	for _, m := range members {
		out = append(out, viewOfMember(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"classroom": viewOf(room),
		"members":   out,
	})
}

func (s *Server) handleClassroomRotate(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	code, err := s.classrooms.RotateCode(r.Context(), r.PathValue("id"), user.ID)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"join_code": code})
}

// handleClassroomRemove is a coach removing somebody and a student leaving,
// which are the same row going away and do not need two routes.
func (s *Server) handleClassroomRemove(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w)(r)
	if user == nil {
		return
	}
	err := s.classrooms.Remove(r.Context(), r.PathValue("id"), user.ID, r.PathValue("user"))
	if err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
