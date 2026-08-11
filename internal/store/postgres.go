package store

import (
	"context"
	"strings"
	"time"

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
