package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

// Streak is one run of puzzles that gets harder until you miss.
//
// It shares the shape of ranked mode, held server side with the solution never
// leaving the machine, for the same reason: a number people compare is worth
// protecting. What it does not share is the rating. A streak is a game, and
// the only thing kept from it is the best run.
type Streak struct {
	rdb *redis.Client
}

func NewStreak(rdb *redis.Client) *Streak { return &Streak{rdb: rdb} }

// A run left open in a tab is not abandoned the way a single ranked puzzle is,
// so it lives longer.
const streakTTL = 24 * time.Hour

// StreakState is the run in progress.
type StreakState struct {
	PuzzleID string    `json:"puzzle_id"`
	Step     int       `json:"step"`
	Count    int       `json:"count"` // puzzles solved so far in this run
	Started  time.Time `json:"started"`
}

// The ladder a run climbs. It starts below where most people play so the first
// few are a warm-up rather than a wall, and it rises by a fixed step, which is
// what makes a long streak mean something rather than being luck of the draw.
const (
	streakStartRating = 700
	streakStepRating  = 40
	streakMaxRating   = 2800
)

// RatingFor is the rating band the next puzzle in a run is drawn from.
func (s StreakState) RatingFor() int {
	r := streakStartRating + s.Count*streakStepRating
	if r > streakMaxRating {
		r = streakMaxRating
	}
	return r
}

func streakKey(userID string) string { return "puzzles:streak:" + userID }

func (s *Streak) Get(ctx context.Context, userID string) (StreakState, bool, error) {
	raw, err := s.rdb.Get(ctx, streakKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return StreakState{}, false, nil
	}
	if err != nil {
		return StreakState{}, false, err
	}
	var out StreakState
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return StreakState{}, false, err
	}
	return out, true, nil
}

func (s *Streak) Set(ctx context.Context, userID string, st StreakState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, streakKey(userID), raw, streakTTL).Err()
}

func (s *Streak) Clear(ctx context.Context, userID string) error {
	return s.rdb.Del(ctx, streakKey(userID)).Err()
}

// RecordStreakAttempt writes the attempt without touching any rating.
func (p *Puzzles) RecordStreakAttempt(ctx context.Context, userID, puzzleID string, solved bool, ms int) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, `
		INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, ms, mode, hints_used)
		VALUES ($1, $2, $3, $4, 'streak', 0)`,
		userID, puzzleID, solved, nullableInt(ms)); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE puzzles
		SET plays = plays + 1, solved = solved + CASE WHEN $2 THEN 1 ELSE 0 END
		WHERE id = $1`, puzzleID, solved); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// SaveBestStreak keeps the run if it beat the last one, and reports the best.
// GREATEST rather than a read then a write, so two tabs finishing runs at once
// cannot lose the better of them.
func (p *Puzzles) SaveBestStreak(ctx context.Context, userID string, run int) (int, error) {
	var best int
	err := p.pool.QueryRow(ctx, `
		UPDATE users SET best_streak = GREATEST(best_streak, $2)
		WHERE id = $1
		RETURNING best_streak`, userID, run).Scan(&best)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return best, err
}

func (p *Puzzles) BestStreak(ctx context.Context, userID string) (int, error) {
	var best int
	err := p.pool.QueryRow(ctx,
		"SELECT best_streak FROM users WHERE id = $1", userID).Scan(&best)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return best, err
}
