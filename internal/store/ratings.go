package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/rating"
)

// The engine is a fixed-strength opponent, so it does not carry a Glicko
// state of its own. Its rating comes from the Stockfish-anchored estimate in
// the engine repo, and the deviation is low because that estimate is measured
// daily rather than guessed. Both need revisiting whenever a new model ships,
// since a stronger engine rated at the old number inflates everyone who beats
// it.
const (
	EngineRating    = 1000
	EngineDeviation = 50
)

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
		INSERT INTO games (id, player_color, result, termination, moves, ply, created_at, user_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (id) DO NOTHING`,
		g.ID, g.PlayerColor, g.Result, g.Termination,
		joinMoves(g.Moves), g.Ply, g.CreatedAt, nullableUUID(g.UserID))
	if err != nil {
		return err
	}
	// Already archived by the other service. Nothing further to do, and
	// crucially nothing to rate.
	if tag.RowsAffected() == 0 {
		return tx.Commit(ctx)
	}

	if g.UserID != "" {
		score, rated := scoreFor(g)
		if rated {
			if err := applyRating(ctx, tx, g.UserID, score); err != nil {
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
func applyRating(ctx context.Context, tx pgx.Tx, userID string, score float64) error {
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
		OpponentRating:    EngineRating,
		OpponentDeviation: EngineDeviation,
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

// Profile is a user's public record.
type Profile struct {
	Username   string  `json:"username"` // empty for a guest
	IsGuest    bool    `json:"guest"`
	Rating     float64 `json:"rating"`
	Deviation  float64 `json:"rating_deviation"`
	RatedGames int     `json:"rated_games"`
	// Provisional until there are enough games for the rating to mean much.
	Provisional bool `json:"provisional"`
}

const provisionalUntil = 5

func (a *Archive) Profile(ctx context.Context, userID string) (*Profile, error) {
	var p Profile
	// NULL for a guest, who has no username until they sign up.
	var username *string
	err := a.pool.QueryRow(ctx, `
		SELECT username, rating, rating_deviation, rated_games, is_guest
		FROM users WHERE id = $1`, userID).
		Scan(&username, &p.Rating, &p.Deviation, &p.RatedGames, &p.IsGuest)
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
