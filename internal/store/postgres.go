package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Archive struct {
	pool *pgxpool.Pool
}

func NewArchive(ctx context.Context, url string) (*Archive, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Archive{pool: pool}, nil
}

// Pool exposes the connection pool to the other stores in this package that
// share the database.
func (a *Archive) Pool() *pgxpool.Pool { return a.pool }

func (a *Archive) Close() { a.pool.Close() }

type Stats struct {
	Total      int `json:"total"`
	EngineWins int `json:"engine_wins"`
	PlayerWins int `json:"player_wins"`
	Draws      int `json:"draws"`
}

func (a *Archive) Stats(ctx context.Context) (*Stats, error) {
	var s Stats
	err := a.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE (player_color = 'white' AND result = '0-1')
		                            OR (player_color = 'black' AND result = '1-0')),
		       count(*) FILTER (WHERE (player_color = 'white' AND result = '1-0')
		                            OR (player_color = 'black' AND result = '0-1')),
		       count(*) FILTER (WHERE result = '1/2-1/2')
		FROM games`).Scan(&s.Total, &s.EngineWins, &s.PlayerWins, &s.Draws)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// joinMoves stores the move list as a single space-separated string. Games
// are replayed from it, never queried inside it, so a text column beats an
// array or a join table here.
func joinMoves(moves []string) string { return strings.Join(moves, " ") }

// formatTime renders a scanned timestamp for JSON.
func formatTime(v any) string {
	if t, ok := v.(time.Time); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return ""
}

// nullableUUID maps an empty user id to SQL NULL. An anonymous game has no
// user, and "" is not a UUID, so it has to become NULL rather than fail the
// column type.
func nullableUUID(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// SaveReview stores a finished game's review. Written once and read many
// times: the numbers cannot change after the last move.
//
// The value is whatever the review package produced, stored as JSON rather
// than spread across columns. It is read whole, never queried into, and its
// shape belongs to the thing that computes it.
func (a *Archive) SaveReview(ctx context.Context, gameID string, r any) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = a.pool.Exec(ctx,
		"UPDATE games SET review = $2 WHERE id = $1", gameID, raw)
	return err
}

// GetReview returns the stored review, if there is one yet. The second return
// separates "no review yet" from "no such game", because the first is what a
// client polling for one is waiting on and the second is a 404.
// The review is handed back as raw JSON rather than decoded and re-encoded.
// Nothing between the worker and the browser reads a field of it, so parsing
// it here would only be a chance to lose one.
func (a *Archive) GetReview(ctx context.Context, gameID string) (json.RawMessage, bool, error) {
	var raw []byte
	err := a.pool.QueryRow(ctx,
		"SELECT review FROM games WHERE id = $1", gameID).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, ErrNotFound
	}
	if err != nil {
		return nil, false, err
	}
	if len(raw) == 0 {
		return nil, false, nil
	}
	return json.RawMessage(raw), true, nil
}
