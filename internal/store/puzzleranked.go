package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/rating"
)

// Ranked mode holds one in-progress puzzle per user, server side.
//
// It has to be server side because the solution is not sent to the browser:
// a rating that moves is worth protecting, and shipping the answer to the
// client and asking it to grade itself is not protecting anything.
//
// Holding exactly one also closes the obvious hole. If a new puzzle could be
// requested at any time, an unwanted one could be reloaded away until an easy
// one arrived, and the rating would measure patience rather than tactics. The
// puzzle you were handed is the puzzle you get.
type Ranked struct {
	rdb *redis.Client
}

func NewRanked(rdb *redis.Client) *Ranked { return &Ranked{rdb: rdb} }

// Long enough to think, short enough that a puzzle left open in a forgotten
// tab does not follow somebody around for a week.
const rankedTTL = 2 * time.Hour

// InProgress is the state of the puzzle a user is part way through.
type InProgress struct {
	PuzzleID string    `json:"puzzle_id"`
	Step     int       `json:"step"` // plies of the solution already played
	Started  time.Time `json:"started"`
}

func rankedKey(userID string) string { return "puzzles:ranked:" + userID }

func (r *Ranked) Get(ctx context.Context, userID string) (InProgress, bool, error) {
	raw, err := r.rdb.Get(ctx, rankedKey(userID)).Result()
	if errors.Is(err, redis.Nil) {
		return InProgress{}, false, nil
	}
	if err != nil {
		return InProgress{}, false, err
	}
	var s InProgress
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return InProgress{}, false, err
	}
	return s, true, nil
}

func (r *Ranked) Set(ctx context.Context, userID string, s InProgress) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return r.rdb.Set(ctx, rankedKey(userID), raw, rankedTTL).Err()
}

func (r *Ranked) Clear(ctx context.Context, userID string) error {
	return r.rdb.Del(ctx, rankedKey(userID)).Err()
}

// RankedOutcome is what changed, which is the only thing worth reporting after
// a ranked attempt.
type RankedOutcome struct {
	Rating       float64 `json:"rating"`
	Change       float64 `json:"change"`
	Deviation    float64 `json:"rating_deviation"`
	PuzzleRating float64 `json:"puzzle_rating"`
	Solved       int     `json:"puzzles_solved"`
}

// RecordRanked writes the attempt and moves both ratings in one transaction.
//
// Both sides move because both are uncertain: the player's tactical strength
// and the puzzle's difficulty are the same kind of unknown, and Glicko-2 does
// not care which side of the pairing it is applied to. It is the same function
// with the arguments swapped.
//
// The user row is locked before the puzzle row, always in that order, so two
// people solving the same puzzle at the same moment queue instead of
// deadlocking.
func (p *Puzzles) RecordRanked(ctx context.Context, userID, puzzleID string, solved bool, ms int) (RankedOutcome, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return RankedOutcome{}, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var user rating.Player
	var solvedCount int
	err = tx.QueryRow(ctx, `
		SELECT puzzle_rating, puzzle_rating_deviation, puzzle_rating_volatility, puzzles_solved
		FROM users WHERE id = $1 FOR UPDATE`, userID).
		Scan(&user.Rating, &user.Deviation, &user.Volatility, &solvedCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return RankedOutcome{}, ErrNotFound
	}
	if err != nil {
		return RankedOutcome{}, err
	}

	var puz rating.Player
	err = tx.QueryRow(ctx, `
		SELECT rating, rating_deviation, rating_volatility
		FROM puzzles WHERE id = $1 FOR UPDATE`, puzzleID).Scan(
		&puz.Rating, &puz.Deviation, &puz.Volatility)
	if errors.Is(err, pgx.ErrNoRows) {
		return RankedOutcome{}, ErrNotFound
	}
	if err != nil {
		return RankedOutcome{}, err
	}

	score := 0.0
	if solved {
		score = 1
	}
	// Both updates read the ratings as they were before this attempt, so the
	// pair is symmetric. Updating one and feeding the new number into the
	// other would give the second side a different opponent than the first.
	newUser := rating.Update(user, []rating.Result{{
		OpponentRating: puz.Rating, OpponentDeviation: puz.Deviation, Score: score,
	}})
	newPuz := rating.Update(puz, []rating.Result{{
		OpponentRating: user.Rating, OpponentDeviation: user.Deviation, Score: 1 - score,
	}})

	if _, err := tx.Exec(ctx, `
		INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, ms, mode, hints_used)
		VALUES ($1, $2, $3, $4, 'ranked', 0)`,
		userID, puzzleID, solved, nullableInt(ms)); err != nil {
		return RankedOutcome{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE users
		SET puzzle_rating = $2, puzzle_rating_deviation = $3,
		    puzzle_rating_volatility = $4,
		    puzzles_solved = puzzles_solved + CASE WHEN $5 THEN 1 ELSE 0 END
		WHERE id = $1`,
		userID, newUser.Rating, newUser.Deviation, newUser.Volatility, solved); err != nil {
		return RankedOutcome{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE puzzles
		SET rating = $2, rating_deviation = $3, rating_volatility = $4,
		    plays = plays + 1, ranked_plays = ranked_plays + 1,
		    solved = solved + CASE WHEN $5 THEN 1 ELSE 0 END
		WHERE id = $1`,
		puzzleID, newPuz.Rating, newPuz.Deviation, newPuz.Volatility, solved); err != nil {
		return RankedOutcome{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return RankedOutcome{}, err
	}

	out := RankedOutcome{
		Rating:       newUser.Rating,
		Change:       newUser.Rating - user.Rating,
		Deviation:    newUser.Deviation,
		PuzzleRating: newPuz.Rating,
		Solved:       solvedCount,
	}
	if solved {
		out.Solved++
	}
	return out, nil
}

// PuzzleRating is the user's tactical rating, kept apart from the playing
// rating because they are different skills and every site that has both keeps
// them separate.
func (p *Puzzles) PuzzleRating(ctx context.Context, userID string) (float64, float64, int, error) {
	var r, dev float64
	var solved int
	err := p.pool.QueryRow(ctx, `
		SELECT puzzle_rating, puzzle_rating_deviation, puzzles_solved
		FROM users WHERE id = $1`, userID).Scan(&r, &dev, &solved)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, ErrNotFound
	}
	return r, dev, solved, err
}
