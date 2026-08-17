package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/rating"
)

// The bot does not carry a Glicko state of its own, so each level needs a
// number to be rated against.
//
// Only one of these is measured. Level 5 is the 300-simulation configuration
// the engine repo evaluates against Stockfish daily, and 1000 is that
// estimate. The rest are an assumption: 120 points a rung, which is the usual
// spacing on a bot ladder and nothing more principled than that.
//
// The deviation is where that gets said out loud. The measured level gets 50,
// the assumed ones get 150, so a result against a level nobody has calibrated
// moves the player's rating less than a result against the one that is.
// Glicko-2 already knows how to handle an opponent whose strength is
// uncertain; this is just telling it the truth.
const (
	measuredLevel     = 5
	measuredRating    = 1000
	pointsPerLevel    = 120
	measuredDeviation = 50
	assumedDeviation  = 150
)

// EngineRating is the rating assigned to a bot level.
func EngineRating(level int) float64 {
	return measuredRating + float64(level-measuredLevel)*pointsPerLevel
}

// EngineDeviation is how sure that number is.
func EngineDeviation(level int) float64 {
	if level == measuredLevel {
		return measuredDeviation
	}
	return assumedDeviation
}

// scoreFor converts a game result into the player's score. Returns false when
// the game was not decided, which is nothing to rate.
func scoreFor(g *game.Game) (float64, bool) {
	switch g.Result {
	case "1/2-1/2":
		return 0.5, true
	case "1-0":
		return boolScore(g.PlayerColor == "white"), true
	case "0-1":
		return boolScore(g.PlayerColor == "black"), true
	default:
		return 0, false
	}
}

func boolScore(playerWon bool) float64 {
	if playerWon {
		return 1
	}
	return 0
}

// SaveFinished archives a completed game and, if it belonged to a signed-in
// player, updates their rating in the same transaction.
//
// Both the api and the worker try to archive a finished game, so this runs
// twice for most games. The insert is ON CONFLICT DO NOTHING, and the rating
// update is gated on that insert actually inserting: without the gate, the
// second call would rate the same game again and every result would count
// double.
func (a *Archive) SaveFinished(ctx context.Context, g *game.Game) error {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO games (id, player_color, result, termination, moves, ply,
		                   created_at, user_id, bot_level, rated)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (id) DO NOTHING`,
		g.ID, g.PlayerColor, g.Result, g.Termination,
		joinMoves(g.Moves), g.Ply, g.CreatedAt, nullableUUID(g.UserID),
		g.Level, g.Rated)
	if err != nil {
		return err
	}
	// Already archived by the other service. Nothing further to do, and
	// crucially nothing to rate.
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}

	if g.UserID != "" && g.Rated {
		score, decided := scoreFor(g)
		if decided {
			if err := applyRating(ctx, tx, g.UserID, score, g.Level); err != nil {
				return err
			}
			if err := moveLadder(ctx, tx, g.UserID, score); err != nil {
				return err
			}
		}
	}
	return tx.Commit(ctx)
}

// applyRating reads the player's current Glicko state, applies one game, and
// writes it back. The row is locked for the duration: a player finishing two
// games at once would otherwise have both updates read the same starting
// rating and one of them would be lost.
func applyRating(ctx context.Context, tx pgx.Tx, userID string, score float64, level int) error {
	var p rating.Player
	err := tx.QueryRow(ctx, `
		SELECT rating, rating_deviation, rating_volatility
		FROM users WHERE id = $1 FOR UPDATE`, userID).
		Scan(&p.Rating, &p.Deviation, &p.Volatility)
	if errors.Is(err, pgx.ErrNoRows) {
		// The account was deleted between the game finishing and this write.
		// The game is still archived, detached, which is the intended
		// behaviour of the ON DELETE SET NULL on the column.
		return nil
	}
	if err != nil {
		return err
	}

	updated := rating.Update(p, []rating.Result{{
		OpponentRating:    EngineRating(level),
		OpponentDeviation: EngineDeviation(level),
		Score:             score,
	}})

	_, err = tx.Exec(ctx, `
		UPDATE users
		SET rating = $2, rating_deviation = $3, rating_volatility = $4,
		    rated_games = rated_games + 1
		WHERE id = $1`,
		userID, updated.Rating, updated.Deviation, updated.Volatility)
	return err
}

// moveLadder steps the bot level after a rated game: up on a win, down on a
// loss, unchanged on a draw.
//
// One rung at a time rather than jumping to whatever the rating implies,
// because the ladder is meant to track somebody as they improve, and a bot
// that leaps two levels after one lucky game is a bot they now cannot beat.
// The CHECK constraint on the column bounds it; GREATEST and LEAST keep the
// write from ever hitting it.
func moveLadder(ctx context.Context, tx pgx.Tx, userID string, score float64) error {
	step := 0
	switch score {
	case 1:
		step = 1
	case 0:
		step = -1
	}
	if step == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		UPDATE users
		SET bot_level = LEAST(6, GREATEST(1, bot_level + $2))
		WHERE id = $1`, userID, step)
	return err
}

// Profile is a user's public record.
type Profile struct {
	Username   string  `json:"username"` // empty for a guest
	IsGuest    bool    `json:"guest"`
	Rating     float64 `json:"rating"`
	Deviation  float64 `json:"rating_deviation"`
	RatedGames int     `json:"rated_games"`
	BotLevel   int     `json:"bot_level"`

	// The puzzle side of the same account. Tactical strength and playing
	// strength are different skills, so they are different numbers, and the
	// profile is where that gets said rather than left for somebody to guess
	// from a bare number in the corner.
	PuzzleRating    float64 `json:"puzzle_rating"`
	PuzzleDeviation float64 `json:"puzzle_rating_deviation"`
	PuzzlesSolved   int     `json:"puzzles_solved"`
	PuzzlesTried    int     `json:"puzzles_tried"`
	Favourites      int     `json:"favourites"`
	ToReview        int     `json:"to_review"`
	// Provisional until there are enough games for the rating to mean much.
	Provisional bool `json:"provisional"`
}

const provisionalUntil = 5

func (a *Archive) Profile(ctx context.Context, userID string) (*Profile, error) {
	var p Profile
	// NULL for a guest, who has no username until they sign up.
	var username *string
	// One round trip. The counts are subqueries rather than joins because each
	// one is an independent aggregate over a different table, and a join would
	// multiply the rows before counting them.
	err := a.pool.QueryRow(ctx, `
		SELECT u.username, u.rating, u.rating_deviation, u.rated_games, u.is_guest,
		       u.bot_level, u.puzzle_rating, u.puzzle_rating_deviation, u.puzzles_solved,
		       (SELECT count(DISTINCT puzzle_id) FROM puzzle_attempts a
		         WHERE a.user_id = u.id),
		       (SELECT count(*) FROM puzzle_favourites f WHERE f.user_id = u.id),
		       (SELECT count(DISTINCT a.puzzle_id) FROM puzzle_attempts a
		         WHERE a.user_id = u.id AND NOT a.solved
		           AND NOT EXISTS (
		               SELECT 1 FROM puzzle_attempts b
		               WHERE b.user_id = a.user_id AND b.puzzle_id = a.puzzle_id
		                 AND b.solved AND b.attempted_at > a.attempted_at))
		FROM users u WHERE u.id = $1`, userID).
		Scan(&username, &p.Rating, &p.Deviation, &p.RatedGames, &p.IsGuest,
			&p.BotLevel, &p.PuzzleRating, &p.PuzzleDeviation, &p.PuzzlesSolved,
			&p.PuzzlesTried, &p.Favourites, &p.ToReview)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if username != nil {
		p.Username = *username
	}
	p.Provisional = p.RatedGames < provisionalUntil
	return &p, nil
}

// HistoryEntry is one finished game in a player's history.
type HistoryEntry struct {
	ID          string `json:"id"`
	PlayerColor string `json:"player_color"`
	Result      string `json:"result"`
	Termination string `json:"termination"`
	Ply         int    `json:"ply"`
	FinishedAt  string `json:"finished_at"`
}

// History returns a user's finished games, newest first. Keyset paginated on
// finished_at rather than OFFSET, which walks and discards every skipped row
// and gets slower the deeper anybody pages.
func (a *Archive) History(ctx context.Context, userID string, before string, limit int) ([]HistoryEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := a.pool.Query(ctx, `
		SELECT id, player_color, result, termination, ply, finished_at
		FROM games
		WHERE user_id = $1 AND ($2 = '' OR finished_at < $2::timestamptz)
		ORDER BY finished_at DESC
		LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		var finished any
		if err := rows.Scan(&e.ID, &e.PlayerColor, &e.Result, &e.Termination, &e.Ply, &finished); err != nil {
			return nil, err
		}
		e.FinishedAt = formatTime(finished)
		out = append(out, e)
	}
	return out, rows.Err()
}
