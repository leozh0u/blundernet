package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// classroom decodes the room out of a create or join response.
const testHTTPFEN = "r1bqkb1r/pppp1ppp/2n2n2/4p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4"

func classroom(t *testing.T, body []byte) classroomView {
	t.Helper()
	var v classroomView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
	return v
}

func openRoom(t *testing.T, s *Server, cookies []*http.Cookie, name string) classroomView {
	t.Helper()
	rec := asUser(s, cookies, "POST", "/api/classrooms", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("creating a classroom: %d %s", rec.Code, rec.Body)
	}
	return classroom(t, rec.Body.Bytes())
}

func TestClassroomsNeedAnAccount(t *testing.T) {
	s := newPuzzleServer(t)
	// Signed out, and then as a guest, which is the case worth having a test
	// for: guests are real user rows here, so "there is a user" is not the
	// same question as "there is an account".
	for _, path := range []string{"/api/classrooms", "/api/classrooms/join"} {
		rec := do(t, s, "POST", path, `{"name":"x","code":"ABCDEF"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("signed out POST %s: %d %s", path, rec.Code, rec.Body)
		}
	}
	guest := do(t, s, "POST", "/api/puzzles/streak", "").Result().Cookies()
	rec := asUser(s, guest, "POST", "/api/classrooms", `{"name":"Ghost class"}`)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("guest creating a classroom: %d %s", rec.Code, rec.Body)
	}
}

func TestCoachOpensARoomAndAStudentJoins(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "room_coach")
	student := signUp(t, s, "room_student")

	room := openRoom(t, s, coach, "Team practice")
	if room.JoinCode == "" || room.Role != "coach" {
		t.Fatalf("created room: %+v", room)
	}

	rec := asUser(s, student, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("joining: %d %s", rec.Code, rec.Body)
	}
	joined := classroom(t, rec.Body.Bytes())
	if joined.ID != room.ID || joined.Role != "student" {
		t.Errorf("joined %+v, want the same room as a student", joined)
	}
	if joined.JoinCode != "" {
		t.Errorf("the join response handed a student the code %q", joined.JoinCode)
	}
	// The student's own list shows the room without the code, since handing
	// out access is the coach's to do.
	rec = asUser(s, student, "GET", "/api/classrooms", "")
	var list struct {
		Classrooms []classroomView `json:"classrooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Classrooms) != 1 {
		t.Fatalf("student is in %d rooms, want 1", len(list.Classrooms))
	}
	if list.Classrooms[0].JoinCode != "" {
		t.Errorf("student was given the join code %q", list.Classrooms[0].JoinCode)
	}
}

// A stranger holding a classroom id gets a 404 rather than a 403, because a
// 403 would confirm the room exists and turn a guessed id into a way to tell
// real rooms from imaginary ones.
func TestAStrangerCannotTellTheRoomExists(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "leak_coach")
	stranger := signUp(t, s, "leak_stranger")
	room := openRoom(t, s, coach, "Team practice")

	real := asUser(s, stranger, "GET", "/api/classrooms/"+room.ID, "")
	fake := asUser(s, stranger, "GET", "/api/classrooms/2ec4a3f6-0000-4000-8000-000000000000", "")
	if real.Code != http.StatusNotFound {
		t.Errorf("stranger reading a real room: %d %s", real.Code, real.Body)
	}
	if fake.Code != real.Code || fake.Body.String() != real.Body.String() {
		t.Errorf("a real room answers %d %s and an imaginary one %d %s; they must match",
			real.Code, real.Body, fake.Code, fake.Body)
	}
}

func TestOnlyACoachRotatesTheCode(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "rotate_coach")
	student := signUp(t, s, "rotate_student")
	room := openRoom(t, s, coach, "Team practice")
	if rec := asUser(s, student, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}

	// A member who is not a coach gets 403, not 404: they already know the
	// room exists, so there is nothing left to hide from them.
	rec := asUser(s, student, "POST", "/api/classrooms/"+room.ID+"/code", "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("student rotating the code: %d %s", rec.Code, rec.Body)
	}
	rec = asUser(s, coach, "POST", "/api/classrooms/"+room.ID+"/code", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("coach rotating the code: %d %s", rec.Code, rec.Body)
	}
}

func TestAStudentIsNotShownTheClass(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "roster_coach")
	alice := signUp(t, s, "roster_alice")
	bob := signUp(t, s, "roster_bob")
	room := openRoom(t, s, coach, "Team practice")
	for _, who := range [][]*http.Cookie{alice, bob} {
		if rec := asUser(s, who, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`); rec.Code != http.StatusOK {
			t.Fatal(rec.Body)
		}
	}

	var seen struct {
		Classroom classroomView `json:"classroom"`
		Members   []memberView  `json:"members"`
	}
	rec := asUser(s, alice, "GET", "/api/classrooms/"+room.ID, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &seen); err != nil {
		t.Fatal(err)
	}
	if len(seen.Members) != 1 || seen.Members[0].Username != "roster_alice" {
		t.Errorf("a student sees %d rows, want only their own", len(seen.Members))
	}
	if seen.Classroom.Members != 3 {
		t.Errorf("the room reports %d members, want 3", seen.Classroom.Members)
	}

	rec = asUser(s, coach, "GET", "/api/classrooms/"+room.ID, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &seen); err != nil {
		t.Fatal(err)
	}
	if len(seen.Members) != 3 {
		t.Errorf("the coach sees %d rows, want 3", len(seen.Members))
	}
}

func TestLeavingAndRemoving(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "leave_coach")
	student := signUp(t, s, "leave_student")
	other := signUp(t, s, "leave_other")
	room := openRoom(t, s, coach, "Team practice")
	for _, who := range [][]*http.Cookie{student, other} {
		if rec := asUser(s, who, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`); rec.Code != http.StatusOK {
			t.Fatal(rec.Body)
		}
	}

	var mine struct {
		Members []memberView `json:"members"`
	}
	rec := asUser(s, coach, "GET", "/api/classrooms/"+room.ID, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &mine); err != nil {
		t.Fatal(err)
	}
	var studentID, otherID string
	for _, m := range mine.Members {
		switch m.Username {
		case "leave_student":
			studentID = m.UserID
		case "leave_other":
			otherID = m.UserID
		}
	}

	// A student cannot remove a classmate.
	rec = asUser(s, student, "DELETE", "/api/classrooms/"+room.ID+"/members/"+otherID, "")
	if rec.Code != http.StatusForbidden {
		t.Errorf("student removing a classmate: %d %s", rec.Code, rec.Body)
	}
	// But can leave.
	rec = asUser(s, student, "DELETE", "/api/classrooms/"+room.ID+"/members/"+studentID, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("student leaving: %d %s", rec.Code, rec.Body)
	}
	// And the coach can remove somebody.
	rec = asUser(s, coach, "DELETE", "/api/classrooms/"+room.ID+"/members/"+otherID, "")
	if rec.Code != http.StatusNoContent {
		t.Errorf("coach removing a student: %d %s", rec.Code, rec.Body)
	}
}

// A hand-typed id must not reach Postgres. Before this was checked it came
// back as a 500 and logged an error, which is both a worse answer and a way
// for anybody to fill the log.
func TestARubbishIdIsNotAServerError(t *testing.T) {
	s := newPuzzleServer(t)
	member := signUp(t, s, "rubbish_id")
	for _, path := range []string{
		"/api/classrooms/not-a-uuid",
		"/api/classrooms/..%2f..%2fetc",
		"/api/classrooms/1",
	} {
		if rec := asUser(s, member, "GET", path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d %s, want 404", path, rec.Code, rec.Body)
		}
	}
}

func TestAClassroomNeedsARealName(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "name_coach")
	for _, body := range []string{`{"name":""}`, `{"name":"   "}`, `{"name":"` + strings.Repeat("x", 61) + `"}`} {
		rec := asUser(s, coach, "POST", "/api/classrooms", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("creating with %s: %d %s, want 400", body, rec.Code, rec.Body)
		}
	}
}

func TestOnlyACoachClosesTheRoom(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "close_coach")
	student := signUp(t, s, "close_student")
	room := openRoom(t, s, coach, "Team practice")
	if rec := asUser(s, student, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}

	if rec := asUser(s, student, "DELETE", "/api/classrooms/"+room.ID, ""); rec.Code != http.StatusForbidden {
		t.Errorf("student closing the room: %d %s", rec.Code, rec.Body)
	}
	if rec := asUser(s, coach, "DELETE", "/api/classrooms/"+room.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("coach closing the room: %d %s", rec.Code, rec.Body)
	}
	// Gone for the student too, and the membership row went with it.
	if rec := asUser(s, student, "GET", "/api/classrooms/"+room.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("reading a closed room: %d %s", rec.Code, rec.Body)
	}
	rec := asUser(s, student, "GET", "/api/classrooms", "")
	var list struct {
		Classrooms []classroomView `json:"classrooms"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Classrooms) != 0 {
		t.Errorf("student still lists %d rooms after it was closed", len(list.Classrooms))
	}
}

func TestAskingAndAnsweringOverHTTP(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "http_q_coach")
	alice := signUp(t, s, "http_q_alice")
	bob := signUp(t, s, "http_q_bob")
	room := openRoom(t, s, coach, "Team practice")
	for _, who := range [][]*http.Cookie{alice, bob} {
		if rec := asUser(s, who, "POST", "/api/classrooms/join", `{"code":"`+room.JoinCode+`"}`); rec.Code != http.StatusOK {
			t.Fatal(rec.Body)
		}
	}

	// Nothing asked yet is a normal state, not an error.
	rec := asUser(s, alice, "GET", "/api/classrooms/"+room.ID+"/questions/open", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"question":null`) {
		t.Fatalf("before any question: %d %s", rec.Code, rec.Body)
	}

	// A student cannot ask.
	if rec := asUser(s, alice, "POST", "/api/classrooms/"+room.ID+"/questions",
		`{"fen":"`+testHTTPFEN+`","prompt":"Best move?"}`); rec.Code != http.StatusForbidden {
		t.Errorf("student asking: %d %s", rec.Code, rec.Body)
	}

	rec = asUser(s, coach, "POST", "/api/classrooms/"+room.ID+"/questions",
		`{"fen":"`+testHTTPFEN+`","prompt":"Best move?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("coach asking: %d %s", rec.Code, rec.Body)
	}
	var asked struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &asked); err != nil {
		t.Fatal(err)
	}

	answer := `{"uci":"f3g5","san":"Ng5"}`
	for _, who := range [][]*http.Cookie{alice, bob} {
		if rec := asUser(s, who, "POST", "/api/classrooms/"+room.ID+"/questions/"+asked.ID+"/answer", answer); rec.Code != http.StatusNoContent {
			t.Fatalf("answering: %d %s", rec.Code, rec.Body)
		}
	}

	// The student sees a count and their own move, and no breakdown.
	rec = asUser(s, alice, "GET", "/api/classrooms/"+room.ID+"/questions/open", "")
	var studentSees struct {
		Question questionView `json:"question"`
		Answers  []struct {
			UCI string `json:"uci"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &studentSees); err != nil {
		t.Fatal(err)
	}
	if studentSees.Question.Answered != 2 {
		t.Errorf("student sees %d answers, want the count of 2", studentSees.Question.Answered)
	}
	if studentSees.Question.Mine != "f3g5" {
		t.Errorf("student's own move is %q", studentSees.Question.Mine)
	}
	if len(studentSees.Answers) != 0 {
		t.Errorf("student was handed the class answers: %+v", studentSees.Answers)
	}

	// The coach sees them gathered.
	rec = asUser(s, coach, "GET", "/api/classrooms/"+room.ID+"/questions/open", "")
	var coachSees struct {
		Answers []struct {
			UCI   string   `json:"uci"`
			Count int      `json:"count"`
			Who   []string `json:"who"`
		} `json:"answers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &coachSees); err != nil {
		t.Fatal(err)
	}
	if len(coachSees.Answers) != 1 || coachSees.Answers[0].Count != 2 {
		t.Fatalf("coach sees %+v, want one move played twice", coachSees.Answers)
	}

	// Closing stops it, and then there is nothing open again.
	if rec := asUser(s, coach, "POST", "/api/classrooms/"+room.ID+"/questions/"+asked.ID+"/close", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("closing: %d %s", rec.Code, rec.Body)
	}
	rec = asUser(s, alice, "GET", "/api/classrooms/"+room.ID+"/questions/open", "")
	if !strings.Contains(rec.Body.String(), `"question":null`) {
		t.Errorf("after closing: %s", rec.Body)
	}
}

func TestAnswerHasToBeAMove(t *testing.T) {
	s := newPuzzleServer(t)
	coach := signUp(t, s, "http_q_bad")
	room := openRoom(t, s, coach, "Team practice")
	rec := asUser(s, coach, "POST", "/api/classrooms/"+room.ID+"/questions",
		`{"fen":"`+testHTTPFEN+`","prompt":"Best move?"}`)
	var asked struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &asked); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"uci":""}`, `{"uci":"nonsense"}`, `{"uci":"e2"}`} {
		if rec := asUser(s, coach, "POST", "/api/classrooms/"+room.ID+"/questions/"+asked.ID+"/answer", body); rec.Code != http.StatusBadRequest {
			t.Errorf("answering with %s: %d %s", body, rec.Code, rec.Body)
		}
	}
}
