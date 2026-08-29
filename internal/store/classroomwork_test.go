package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// solve records a solved attempt for a puzzle that already exists in the
// fixtures, at a chosen time, so "only work done after it was set counts" can
// actually be tested rather than assumed.
func solve(t *testing.T, c *Classrooms, ctx context.Context, userID, puzzleID string, at time.Time) {
	t.Helper()
	_, err := c.pool.Exec(ctx, `
		INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, attempted_at)
		VALUES ($1, $2, true, $3)`, userID, puzzleID, at)
	if err != nil {
		t.Fatal(err)
	}
}

// workFixtures puts two puzzles in the corpus with known themes and ratings.
func workFixtures(t *testing.T, c *Classrooms, ctx context.Context) {
	t.Helper()
	_, err := c.pool.Exec(ctx, `
		INSERT INTO puzzles (id, fen, moves, rating, solution_plies, phase, themes)
		VALUES
		  ('workfork1', '8/8/8/8/8/8/8/K6k w - - 0 1', 'a1a2', 1300, 1, 'middlegame', ARRAY['fork']),
		  ('workfork2', '8/8/8/8/8/8/8/K6k w - - 0 1', 'a1a2', 1400, 1, 'middlegame', ARRAY['fork']),
		  ('workpin1',  '8/8/8/8/8/8/8/K6k w - - 0 1', 'a1a2', 1300, 1, 'middlegame', ARRAY['pin'])
		ON CONFLICT (id) DO NOTHING`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestOnlyACoachSetsHomework(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "w_coach1")
	student := account(t, users, ctx, "w_student1")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}

	if _, err := rooms.SetAssignment(ctx, room.ID, student.ID, "fork", 0, 0, 5); !errors.Is(err, ErrNotCoach) {
		t.Errorf("student setting homework: %v, want ErrNotCoach", err)
	}
	if _, err := rooms.SetAssignment(ctx, room.ID, coach.ID, "fork", 0, 0, 0); !errors.Is(err, ErrBadAssignment) {
		t.Errorf("a target of zero: %v, want ErrBadAssignment", err)
	}
	if _, err := rooms.SetAssignment(ctx, room.ID, coach.ID, "fork", 0, 0, 5); err != nil {
		t.Errorf("coach setting homework: %v", err)
	}
}

// The query is the whole feature, so this is the test that matters: the right
// puzzles count, the wrong theme does not, and work from before it was set
// does not.
func TestProgressCountsTheRightWork(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	workFixtures(t, rooms, ctx)
	coach := account(t, users, ctx, "w_coach2")
	student := account(t, users, ctx, "w_student2")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}

	// Solved before the homework was set, so it must not count.
	solve(t, rooms, ctx, student.ID, "workfork1", time.Now().Add(-time.Hour))

	a, err := rooms.SetAssignment(ctx, room.ID, coach.ID, "fork", 0, 0, 2)
	if err != nil {
		t.Fatal(err)
	}

	list, err := rooms.Assignments(ctx, room.ID, student.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("got %d assignments", len(list))
	}
	if list[0].Done != 0 {
		t.Errorf("work from before the assignment counted: done is %d, want 0", list[0].Done)
	}

	// A pin does not count towards a fork assignment.
	solve(t, rooms, ctx, student.ID, "workpin1", time.Now())
	list, _ = rooms.Assignments(ctx, room.ID, student.ID)
	if list[0].Done != 0 {
		t.Errorf("the wrong theme counted: done is %d, want 0", list[0].Done)
	}

	// Two forks after it was set, which finishes it.
	solve(t, rooms, ctx, student.ID, "workfork1", time.Now())
	solve(t, rooms, ctx, student.ID, "workfork2", time.Now())
	list, _ = rooms.Assignments(ctx, room.ID, student.ID)
	if list[0].Done != 2 {
		t.Errorf("done is %d, want 2", list[0].Done)
	}

	// The coach sees how many people finished, not their own count.
	fromCoach, err := rooms.Assignments(ctx, room.ID, coach.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fromCoach[0].Class != 1 {
		t.Errorf("class finished count is %d, want 1", fromCoach[0].Class)
	}
	if fromCoach[0].Done != 0 {
		t.Errorf("the coach's own progress is %d, want 0", fromCoach[0].Done)
	}
}

// Solving the same puzzle twice is one puzzle, not two.
func TestTheSamePuzzleCountsOnce(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	workFixtures(t, rooms, ctx)
	coach := account(t, users, ctx, "w_coach3")
	student := account(t, users, ctx, "w_student3")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.SetAssignment(ctx, room.ID, coach.ID, "fork", 0, 0, 3); err != nil {
		t.Fatal(err)
	}

	solve(t, rooms, ctx, student.ID, "workfork1", time.Now())
	solve(t, rooms, ctx, student.ID, "workfork1", time.Now().Add(time.Second))
	list, err := rooms.Assignments(ctx, room.ID, student.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Done != 1 {
		t.Errorf("the same puzzle twice counted as %d, want 1", list[0].Done)
	}
}

func TestRatingWindowNarrowsTheWork(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	workFixtures(t, rooms, ctx)
	coach := account(t, users, ctx, "w_coach4")
	student := account(t, users, ctx, "w_student4")
	room, err := rooms.Create(ctx, coach.ID, "Team practice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.Join(ctx, student.ID, room.JoinCode); err != nil {
		t.Fatal(err)
	}
	// Deliberately the wrong way round: a coach who types it backwards meant
	// the other order.
	if _, err := rooms.SetAssignment(ctx, room.ID, coach.ID, "fork", 1350, 1250, 2); err != nil {
		t.Fatal(err)
	}

	solve(t, rooms, ctx, student.ID, "workfork1", time.Now()) // 1300, inside
	solve(t, rooms, ctx, student.ID, "workfork2", time.Now()) // 1400, outside
	list, err := rooms.Assignments(ctx, room.ID, student.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list[0].Done != 1 {
		t.Errorf("done is %d, want 1: only the 1300 puzzle is in the window", list[0].Done)
	}
}

func TestHomeworkBelongsToItsClassroom(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coachA := account(t, users, ctx, "w_coach5a")
	coachB := account(t, users, ctx, "w_coach5b")
	roomA, err := rooms.Create(ctx, coachA.ID, "Room A")
	if err != nil {
		t.Fatal(err)
	}
	roomB, err := rooms.Create(ctx, coachB.ID, "Room B")
	if err != nil {
		t.Fatal(err)
	}
	a, err := rooms.SetAssignment(ctx, roomA.ID, coachA.ID, "fork", 0, 0, 5)
	if err != nil {
		t.Fatal(err)
	}

	if err := rooms.DropAssignment(ctx, roomB.ID, a.ID, coachB.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("dropping another room's homework: %v, want a refusal", err)
	}
	stranger := account(t, users, ctx, "w_stranger5")
	if _, err := rooms.Assignments(ctx, roomA.ID, stranger.ID); !errors.Is(err, ErrNotAMember) {
		t.Errorf("stranger reading homework: %v, want ErrNotAMember", err)
	}
	if err := rooms.DropAssignment(ctx, roomA.ID, a.ID, coachA.ID); err != nil {
		t.Errorf("coach dropping their own: %v", err)
	}
}
