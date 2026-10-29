package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestConcurrentJoinsRespectRoomCapacity(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "capacity_coach")
	room, err := rooms.Create(ctx, coach.ID, "Last seat")
	if err != nil {
		t.Fatal(err)
	}
	_, err = rooms.pool.Exec(ctx, `
		WITH filler AS (
		    INSERT INTO users (id, is_guest)
		    SELECT gen_random_uuid(), true FROM generate_series(1, $2::int)
		    RETURNING id
		)
		INSERT INTO classroom_members (classroom_id, user_id, role)
		SELECT $1, id, 'student' FROM filler`, room.ID, maxRoomMembers-2)
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	ids := make([]string, n)
	for i := range ids {
		ids[i] = account(t, users, ctx, fmt.Sprintf("capacity_student%d", i)).ID
	}
	start, results := make(chan struct{}), make(chan error, n)
	for _, id := range ids {
		go func() {
			<-start
			_, err := rooms.Join(ctx, id, room.JoinCode)
			results <- err
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < n; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrRoomFull) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("%d people took the final seat", successes)
	}
}

func TestConcurrentCoachDeparturesKeepOneCoach(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	first := account(t, users, ctx, "leaving_coach1")
	second := account(t, users, ctx, "leaving_coach2")
	room, err := rooms.Create(ctx, first.ID, "Two coaches")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rooms.pool.Exec(ctx, `INSERT INTO classroom_members (classroom_id, user_id, role) VALUES ($1, $2, 'coach')`, room.ID, second.ID); err != nil {
		t.Fatal(err)
	}
	start, results := make(chan struct{}), make(chan error, 2)
	for _, id := range []string{first.ID, second.ID} {
		go func() {
			<-start
			results <- rooms.Remove(ctx, room.ID, id, id)
		}()
	}
	close(start)
	successes := 0
	for i := 0; i < 2; i++ {
		if err := <-results; err == nil {
			successes++
		} else if !errors.Is(err, ErrLastCoach) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("%d departures succeeded, want one", successes)
	}
}

func TestConcurrentQuestionsLeaveOnlyOneOpen(t *testing.T) {
	rooms, users, ctx := testClassrooms(t)
	coach := account(t, users, ctx, "question_coach")
	room, err := rooms.Create(ctx, coach.ID, "One question")
	if err != nil {
		t.Fatal(err)
	}
	const n = 8
	start, results := make(chan struct{}), make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			<-start
			_, err := rooms.Ask(ctx, room.ID, coach.ID, "8/8/8/4k3/8/4K3/8/8 w - - 0 1", "Next move?")
			results <- err
		}()
	}
	close(start)
	for i := 0; i < n; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	var open int
	if err := rooms.pool.QueryRow(ctx, `SELECT count(*) FROM classroom_questions WHERE classroom_id = $1 AND closed_at IS NULL`, room.ID).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 1 {
		t.Fatalf("%d questions remain open", open)
	}
}
