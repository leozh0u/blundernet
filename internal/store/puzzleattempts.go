package store

import (
	"context"
	"fmt"
)

// Attempt is one try at one puzzle.
type Attempt struct {
	UserID   string
	PuzzleID string
	Solved   bool
	Ms       int
	Mode     string // learning or ranked
	Hints    int
}

const (
	ModeLearning = "learning"
	ModeRanked   = "ranked"
)

// Record writes an attempt and moves the puzzle's own counters. Both happen in
// one transaction because a play that is not counted, or a counter moved
// without the row behind it, both make the wrong-list and the puzzle stats
// disagree with each other.
//
// Ranked plays are counted separately from all plays, since only ranked ones
// will move the puzzle's rating.
func (p *Puzzles) Record(ctx context.Context, a Attempt) error {
	if a.Mode != ModeLearning && a.Mode != ModeRanked {
		return fmt.Errorf("unknown puzzle mode %q", a.Mode)
	}
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	if _, err := tx.Exec(ctx, `
		INSERT INTO puzzle_attempts (user_id, puzzle_id, solved, ms, mode, hints_used)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		a.UserID, a.PuzzleID, a.Solved, nullableInt(a.Ms), a.Mode, a.Hints); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE puzzles
		SET plays        = plays + 1,
		    solved       = solved + CASE WHEN $2 THEN 1 ELSE 0 END,
		    ranked_plays = ranked_plays + CASE WHEN $3 THEN 1 ELSE 0 END
		WHERE id = $1`, a.PuzzleID, a.Solved, a.Mode == ModeRanked); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Failed lists the puzzles a user got wrong and has not since solved, newest
// first. This is the drill list that makes the site worth a second visit.
//
// The NOT EXISTS is against later attempts rather than a solved flag on the
// puzzle, because a user can fail, come back, and get it right, and only the
// second outcome should count.
func (p *Puzzles) Failed(ctx context.Context, userID string, limit int) ([]string, error) {
	rows, err := p.pool.Query(ctx, `
		SELECT DISTINCT ON (a.puzzle_id) a.puzzle_id, a.attempted_at
		FROM puzzle_attempts a
		WHERE a.user_id = $1
		  AND NOT a.solved
		  AND NOT EXISTS (
		      SELECT 1 FROM puzzle_attempts b
		      WHERE b.user_id = a.user_id AND b.puzzle_id = a.puzzle_id
		        AND b.solved AND b.attempted_at > a.attempted_at
		  )
		ORDER BY a.puzzle_id, a.attempted_at DESC
		LIMIT $2`, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var (
			id string
			at any
		)
		if err := rows.Scan(&id, &at); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// Stats is what a user has done with puzzles, for the profile.
type PuzzleStats struct {
	Attempted int `json:"attempted"`
	Solved    int `json:"solved"`
}

func (p *Puzzles) StatsFor(ctx context.Context, userID string) (PuzzleStats, error) {
	var s PuzzleStats
	err := p.pool.QueryRow(ctx, `
		SELECT count(DISTINCT puzzle_id),
		       count(DISTINCT puzzle_id) FILTER (WHERE solved)
		FROM puzzle_attempts WHERE user_id = $1`, userID).Scan(&s.Attempted, &s.Solved)
	return s, err
}

// nullableInt maps an unset duration to SQL NULL rather than a zero that would
// read as "solved instantly".
func nullableInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}
