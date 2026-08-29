package store

import (
	"errors"
	"testing"
)

const testFEN = "r1bqkb1r/pppp1ppp/2n2n2/4p3/2B1P3/5N2/PPPP1PPP/RNBQK2R w KQkq - 4 4"

func TestOnlyACoachAsks(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "q_coach1")
	student := account(t, users, ctx, "q_student1")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}

	if _, err := rooms.Ask(ctx, room.ID, student.ID, testFEN, "Best move?"); !errors.Is(err, ErrNotCoach) {
		t.Errorf("student asking: %v, want ErrNotCoach", err)
	}
	if _, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "Best move?"); err != nil {
		t.Errorf("coach asking: %v", err)
	}
}

// The point of the whole feature: a coach sees what the class played, and a
// student does not. Seeing the answers before you commit turns a question into
// a vote.
func TestAStudentCannotSeeTheClassAnswers(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "q_coach2")
	alice := account(t, users, ctx, "q_alice2")
	bob := account(t, users, ctx, "q_bob2")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []*User{alice, bob} {
		if _, err := rooms.Join(ctx, u.ID, room.JoinCode); err != nil {
			t.Fatal(err)
		}
	}
	q, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "Best move?")
	if err != nil {
		t.Fatal(err)
	}

	if err := rooms.Answer(ctx, room.ID, q.ID, alice.ID, "f3g5", "Ng5"); err != nil {
		t.Fatal(err)
	}
	if err := rooms.Answer(ctx, room.ID, q.ID, bob.ID, "f3g5", "Ng5"); err != nil {
		t.Fatal(err)
	}

	// Alice sees her own move and the count, and no group breakdown.
	seen, groups, mine, err := rooms.OpenQuestion(ctx, room.ID, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if groups != nil {
		t.Errorf("a student was handed %d answer groups", len(groups))
	}
	if mine != "f3g5" {
		t.Errorf("student's own answer is %q, want f3g5", mine)
	}
	if seen.Answered != 2 {
		t.Errorf("answered count is %d, want 2", seen.Answered)
	}

	// The coach sees the moves gathered, which is the thing worth reading.
	_, groups, _, err = rooms.OpenQuestion(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Count != 2 || groups[0].UCI != "f3g5" {
		t.Fatalf("coach sees %+v, want one group of two on f3g5", groups)
	}
	if len(groups[0].Who) != 2 {
		t.Errorf("group names %v, want both students", groups[0].Who)
	}
}

func TestAnsweringAgainReplacesTheAnswer(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "q_coach3")
	student := account(t, users, ctx, "q_student3")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}
	q, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "Best move?")
	if err != nil {
		t.Fatal(err)
	}

	if err := rooms.Answer(ctx, room.ID, q.ID, student.ID, "f3g5", "Ng5"); err != nil {
		t.Fatal(err)
	}
	if err := rooms.Answer(ctx, room.ID, q.ID, student.ID, "c4f7", "Bxf7+"); err != nil {
		t.Fatal(err)
	}

	seen, groups, mine, err := rooms.OpenQuestion(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if seen.Answered != 1 {
		t.Errorf("answered count is %d after a change of mind, want 1", seen.Answered)
	}
	if len(groups) != 1 || groups[0].UCI != "c4f7" {
		t.Errorf("groups %+v, want only the second answer", groups)
	}
	_ = mine
}

func TestAskingAgainClosesTheOneBefore(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "q_coach4")
	student := account(t, users, ctx, "q_student4")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}

	first, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "One")
	if err != nil {
		t.Fatal(err)
	}
	second, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "Two")
	if err != nil {
		t.Fatal(err)
	}

	open, _, _, err := rooms.OpenQuestion(ctx, room.ID, student.ID)
	if err != nil {
		t.Fatal(err)
	}
	if open.ID != second.ID {
		t.Errorf("open question is %s, want the newer one", open.ID)
	}
	// The old question stops taking answers, so nobody is answering a position
	// that left the screen.
	if err := rooms.Answer(ctx, room.ID, first.ID, student.ID, "f3g5", "Ng5"); !errors.Is(err, ErrQuestionClosed) {
		t.Errorf("answering the closed question: %v, want ErrQuestionClosed", err)
	}
}

func TestClosingStopsAnswers(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "q_coach5")
	student := account(t, users, ctx, "q_student5")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}
	q, err := rooms.Ask(ctx, room.ID, coach.ID, testFEN, "Best move?")
	if err != nil {
		t.Fatal(err)
	}

	if err := rooms.CloseQuestion(ctx, room.ID, q.ID, student.ID); !errors.Is(err, ErrNotCoach) {
		t.Errorf("student closing: %v, want ErrNotCoach", err)
	}
	if err := rooms.CloseQuestion(ctx, room.ID, q.ID, coach.ID); err != nil {
		t.Fatal(err)
	}
	if err := rooms.Answer(ctx, room.ID, q.ID, student.ID, "f3g5", "Ng5"); !errors.Is(err, ErrQuestionClosed) {
		t.Errorf("answering after close: %v, want ErrQuestionClosed", err)
	}
	if _, _, _, err := rooms.OpenQuestion(ctx, room.ID, student.ID); !errors.Is(err, ErrNoQuestion) {
		t.Errorf("reading after close: %v, want ErrNoQuestion", err)
	}
}

// Holding a question id from one room must not be a way into another.
func TestAQuestionBelongsToItsClassroom(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coachA := account(t, users, ctx, "q_coach6a")
	coachB := account(t, users, ctx, "q_coach6b")
	roomA, err := rooms.Create(ctx, coachA.ID, "Room A")
	if err != nil {
		t.Fatal(err)
	}
	roomB, err := rooms.Create(ctx, coachB.ID, "Room B")
	if err != nil {
		t.Fatal(err)
	}
	q, err := rooms.Ask(ctx, roomA.ID, coachA.ID, testFEN, "Best move?")
	if err != nil {
		t.Fatal(err)
	}

	// Coach B is a legitimate coach of their own room, and still cannot touch
	// a question that belongs to room A.
	if err := rooms.Answer(ctx, roomB.ID, q.ID, coachB.ID, "f3g5", "Ng5"); !errors.Is(err, ErrQuestionClosed) {
		t.Errorf("answering another room's question: %v, want a refusal", err)
	}
	if err := rooms.CloseQuestion(ctx, roomB.ID, q.ID, coachB.ID); !errors.Is(err, ErrNoQuestion) {
		t.Errorf("closing another room's question: %v, want a refusal", err)
	}
	// And a stranger to both rooms gets nothing at all.
	stranger := account(t, users, ctx, "q_stranger6")
	if _, _, _, err := rooms.OpenQuestion(ctx, roomA.ID, stranger.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("stranger reading a question: %v, want ErrNotAMember", err)
	}
}
