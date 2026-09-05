package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

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
	case errors.Is(err, store.ErrRoomFull):
		httpError(w, http.StatusConflict, "that classroom is full")
	case errors.Is(err, store.ErrBadName):
		httpError(w, http.StatusBadRequest, "a classroom name is 1 to 60 characters")
	case errors.Is(err, store.ErrTooManyRuns):
		httpError(w, http.StatusConflict, "you already run the maximum number of classrooms")
	case errors.Is(err, store.ErrNoQuestion):
		httpError(w, http.StatusNotFound, "no open question")
	case errors.Is(err, store.ErrQuestionClosed):
		httpError(w, http.StatusConflict, "that question is closed")
	case errors.Is(err, store.ErrBadAssignment):
		httpError(w, http.StatusBadRequest, "an assignment needs a target between 1 and 100")
	case errors.Is(err, store.ErrBadPrompt):
		httpError(w, http.StatusBadRequest, "a question needs a position and a prompt under 140 characters")
	default:
		internalError(w, err)
	}
}

// requireAccount is the gate for everything in this file. Unlike the rest of
// the site, a classroom will not quietly mint a guest to hold your place:
// membership has to still mean something next week.
func (s *Server) requireAccount(w http.ResponseWriter, r *http.Request) *store.User {
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

// pathUUID reads an id out of the path and refuses anything that is not one.
// Without this a hand-typed id reaches Postgres, fails to parse there, and
// comes back as a 500 that also logs an error, so an unauthenticated caller
// can fill the log by asking for nonsense. It answers the same 404 an id the
// caller may not see would get, which keeps the two indistinguishable.
func pathUUID(w http.ResponseWriter, r *http.Request, name string) (string, bool) {
	v := r.PathValue(name)
	if _, err := uuid.Parse(v); err != nil {
		httpError(w, http.StatusNotFound, "no classroom with that code")
		return "", false
	}
	return v, true
}

func (s *Server) handleClassroomCreate(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
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
	user := s.requireAccount(w, r)
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
	user := s.requireAccount(w, r)
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
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	room, members, err := s.classrooms.Roster(r.Context(), id, user.ID)
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
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	code, err := s.classrooms.RotateCode(r.Context(), id, user.ID)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"join_code": code})
}

// handleClassroomRemove is a coach removing somebody and a student leaving,
// which are the same row going away and do not need two routes.
func (s *Server) handleClassroomRemove(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	target, ok := pathUUID(w, r, "user")
	if !ok {
		return
	}
	if err := s.classrooms.Remove(r.Context(), id, user.ID, target); err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleClassroomDelete closes a room. Coach only, and the only way a room is
// ever removed, since the last coach is not allowed to simply leave.
func (s *Server) handleClassroomDelete(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := s.classrooms.Delete(r.Context(), id, user.ID); err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Questions. A coach puts a position in front of the class and asks about it;
// students answer with a move; the coach reads the answers gathered by move.
//
// Both sides poll this rather than holding a socket. A classroom question
// turns over on the scale of a minute, twenty students is not a fan-out
// problem, and a socket per student is a connection to lose, reconnect and
// reason about for no gain at this size.

type questionView struct {
	ID       string `json:"id"`
	FEN      string `json:"fen"`
	Prompt   string `json:"prompt"`
	Answered int    `json:"answered"`
	// Only the caller's own move. What the rest of the class played is a
	// coach's to see.
	Mine string `json:"mine,omitempty"`
}

func (s *Server) handleQuestionAsk(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		FEN    string `json:"fen"`
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	q, err := s.classrooms.Ask(r.Context(), id, user.ID, body.FEN, body.Prompt)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, questionView{ID: q.ID, FEN: q.FEN, Prompt: q.Prompt})
}

func (s *Server) handleQuestionOpen(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	q, groups, mine, err := s.classrooms.OpenQuestion(r.Context(), id, user.ID)
	if errors.Is(err, store.ErrNoQuestion) {
		// Not an error worth a status code: "nothing is being asked" is the
		// normal state of a classroom and both sides poll for it.
		writeJSON(w, http.StatusOK, map[string]any{"question": nil})
		return
	}
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"question": questionView{ID: q.ID, FEN: q.FEN, Prompt: q.Prompt, Answered: q.Answered, Mine: mine},
		"answers":  groups,
	})
}

func (s *Server) handleQuestionAnswer(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	qid, ok := pathUUID(w, r, "question")
	if !ok {
		return
	}
	var body struct {
		UCI string `json:"uci"`
		SAN string `json:"san"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	// A move is four or five characters. Anything else is not one, and the
	// board is the only thing that should be producing these.
	if n := len(body.UCI); n < 4 || n > 5 {
		httpError(w, http.StatusBadRequest, "that is not a move")
		return
	}
	if err := s.classrooms.Answer(r.Context(), id, qid, user.ID, body.UCI, body.SAN); err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleQuestionClose(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	qid, ok := pathUUID(w, r, "question")
	if !ok {
		return
	}
	if err := s.classrooms.CloseQuestion(r.Context(), id, qid, user.ID); err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Homework.

type assignmentView struct {
	ID        string `json:"id"`
	Theme     string `json:"theme"`
	MinRating int    `json:"min_rating"`
	MaxRating int    `json:"max_rating"`
	Target    int    `json:"target"`
	Done      int    `json:"done"`
	Class     int    `json:"class"`
}

func (s *Server) handleAssignmentList(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	list, err := s.classrooms.Assignments(r.Context(), id, user.ID)
	if err != nil {
		classroomError(w, err)
		return
	}
	out := make([]assignmentView, 0, len(list))
	for _, a := range list {
		out = append(out, assignmentView{
			ID: a.ID, Theme: a.Theme, MinRating: a.MinRating, MaxRating: a.MaxRating,
			Target: a.Target, Done: a.Done, Class: a.Class,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"assignments": out})
}

func (s *Server) handleAssignmentSet(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	var body struct {
		Theme     string `json:"theme"`
		MinRating int    `json:"min_rating"`
		MaxRating int    `json:"max_rating"`
		Target    int    `json:"target"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "bad request")
		return
	}
	a, err := s.classrooms.SetAssignment(r.Context(), id, user.ID,
		body.Theme, body.MinRating, body.MaxRating, body.Target)
	if err != nil {
		classroomError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, assignmentView{
		ID: a.ID, Theme: a.Theme, MinRating: a.MinRating, MaxRating: a.MaxRating,
		Target: a.Target,
	})
}

func (s *Server) handleAssignmentDrop(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	work, ok := pathUUID(w, r, "assignment")
	if !ok {
		return
	}
	if err := s.classrooms.DropAssignment(r.Context(), id, work, user.ID); err != nil {
		classroomError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The live demonstration board.
//
// A coach shows a position and the class watches it change. Everything below
// is about that being live rather than a request away: the coach's changes are
// published, and every student holds a socket open to receive them.

type boardBody struct {
	FEN         string `json:"fen"`
	Orientation string `json:"orientation"`
	Caption     string `json:"caption"`
	Live        bool   `json:"live"`
}

// handleBoardShow takes what the coach is showing and broadcasts it.
//
// Coach only, checked on the server. The browser already hides the editor from
// students, and that is a convenience rather than a control: anyone can post
// to this URL, so the room's own record of who is the coach decides.
func (s *Server) handleBoardShow(w http.ResponseWriter, r *http.Request) {
	user := s.requireAccount(w, r)
	if user == nil {
		return
	}
	id, ok := pathUUID(w, r, "id")
	if !ok {
		return
	}
	if s.board == nil {
		httpError(w, http.StatusServiceUnavailable, "the live board is not configured")
		return
	}
	role, err := s.classrooms.Role(r.Context(), id, user.ID)
	if err != nil {
		classroomError(w, err)
		return
	}
	if role != "coach" {
		// 404 rather than 403, the same as everywhere else here: a student
		// probing this should not learn that the room exists and they are
		// merely the wrong role.
		httpError(w, http.StatusNotFound, "classroom not found")
		return
	}
	var body boardBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		httpError(w, http.StatusBadRequest, "body must be a board")
		return
	}
	if len(body.FEN) > 120 {
		httpError(w, http.StatusBadRequest, "that is not a position")
		return
	}
	if body.Orientation != "black" {
		body.Orientation = "white"
	}
	if len(body.Caption) > 200 {
		body.Caption = body.Caption[:200]
	}
	if err := s.board.Show(r.Context(), id, store.Board{
		FEN: body.FEN, Orientation: body.Orientation, Caption: body.Caption, Live: body.Live,
	}); err != nil {
		internalError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBoardWatch is the student's end: the board now, then every change to
// it, over one socket.
func (s *Server) handleBoardWatch(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r.Context())
	if user == nil || user.IsGuest {
		httpError(w, http.StatusUnauthorized, "sign in to watch")
		return
	}
	id := r.PathValue("id")
	if s.board == nil || s.classrooms == nil {
		httpError(w, http.StatusServiceUnavailable, "the live board is not configured")
		return
	}
	// Membership is checked before the upgrade, so a stranger is refused with
	// an ordinary status code rather than being handed a socket that then goes
	// quiet on them.
	if _, err := s.classrooms.Role(r.Context(), id, user.ID); err != nil {
		classroomError(w, err)
		return
	}

	upgrader := websocket.Upgrader{CheckOrigin: s.sameOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	events := s.board.Watch(ctx, id)

	// Reader exists only to notice the student closing the tab.
	go func() {
		defer cancel()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	// The board as it stands, so somebody arriving in the middle of a lesson
	// sees what everyone else is looking at rather than waiting for the coach
	// to touch a piece.
	if current, err := s.board.Current(ctx, id); err == nil {
		if raw, err := json.Marshal(current); err == nil {
			if err := conn.WriteMessage(websocket.TextMessage, raw); err != nil {
				return
			}
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
