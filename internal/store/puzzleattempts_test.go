package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestFailedPuzzlesReturnsNewestBeforeLimit(t *testing.T) {
	p, ctx := testPuzzles(t)
	rows := fixture()[:2]
	rows[0].ID, rows[1].ID = "a-old", "z-new"
	load(t, p, ctx, rows)
	uid := uuid.NewString()
	if _, err := p.pool.Exec(ctx, `INSERT INTO users (id, is_guest) VALUES ($1, true)`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := p.pool.Exec(ctx, `
		INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, attempted_at)
		VALUES ($1, 'a-old', false, now() - interval '2 days'),
		       ($1, 'z-new', false, now() - interval '1 day')`, uid); err != nil {
		t.Fatal(err)
	}
	got, err := p.Failed(ctx, uid, 1)
	if err != nil || len(got) != 1 || got[0] != "z-new" {
		t.Fatalf("latest failure = %v, %v; want z-new", got, err)
	}
	if err := p.Record(ctx, Attempt{UserID: uid, PuzzleID: "z-new", Solved: true, Mode: ModeLearning}); err != nil {
		t.Fatal(err)
	}
	got, err = p.Failed(ctx, uid, 1)
	if err != nil || len(got) != 1 || got[0] != "a-old" {
		t.Fatalf("unresolved failure = %v, %v; want a-old", got, err)
	}
}
