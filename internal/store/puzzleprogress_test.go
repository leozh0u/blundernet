package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPuzzleProgressRejectsStaleTransitions(t *testing.T) {
	rdb, _ := testRedis(t)
	ctx := context.Background()
	r := NewRanked(rdb)
	initial := InProgress{PuzzleID: "one", Started: time.Now()}
	if err := r.Replace(ctx, "user", nil, &initial); err != nil {
		t.Fatal(err)
	}
	next := initial
	next.Step = 2
	if err := r.Replace(ctx, "user", &initial, &next); err != nil {
		t.Fatal(err)
	}
	for _, err := range []error{
		r.Replace(ctx, "user", nil, &initial),
		r.Replace(ctx, "user", &initial, nil),
		r.Replace(ctx, "user", &initial, &initial),
	} {
		if !errors.Is(err, ErrConflict) {
			t.Fatalf("stale transition accepted: %v", err)
		}
	}
	got, _, err := r.Get(ctx, "user")
	if err != nil || got.Step != 2 {
		t.Fatalf("newer step was lost: %+v, %v", got, err)
	}
}

func TestConcurrentPuzzleResultCountsOnce(t *testing.T) {
	for _, mode := range []string{"ranked", "streak"} {
		t.Run(mode, func(t *testing.T) {
			p, ctx := testPuzzles(t)
			load(t, p, ctx, fixture()[:1])
			uid, pid := uuid.NewString(), fixture()[0].ID
			if _, err := p.pool.Exec(ctx, `INSERT INTO users (id, is_guest) VALUES ($1, true)`, uid); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			start, results := make(chan struct{}), make(chan error, 2)
			for i := 0; i < 2; i++ {
				go func() {
					<-start
					var err error
					if mode == "ranked" {
						_, err = p.RecordRanked(ctx, uid, pid, true, 1000, started)
					} else {
						err = p.RecordStreakAttempt(ctx, uid, pid, true, 1000, started)
					}
					results <- err
				}()
			}
			close(start)
			var successes, conflicts int
			for i := 0; i < 2; i++ {
				switch err := <-results; {
				case err == nil:
					successes++
				case errors.Is(err, ErrConflict):
					conflicts++
				default:
					t.Fatal(err)
				}
			}
			if successes != 1 || conflicts != 1 {
				t.Fatalf("successes=%d, conflicts=%d", successes, conflicts)
			}
			var count, plays int
			if err := p.pool.QueryRow(ctx, `SELECT count(*) FROM puzzle_attempts WHERE user_id = $1`, uid).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if err := p.pool.QueryRow(ctx, `SELECT plays FROM puzzles WHERE id = $1`, pid).Scan(&plays); err != nil {
				t.Fatal(err)
			}
			if count != 1 || plays != 1 {
				t.Fatalf("attempts=%d, plays=%d, want one each", count, plays)
			}
		})
	}
}
