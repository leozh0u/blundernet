package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Games pasted in to be reviewed.
type Imports struct {
	pool *pgxpool.Pool
}

func NewImports(pool *pgxpool.Pool) *Imports { return &Imports{pool: pool} }

// Create records a game to review and returns its id. The review itself is
// null until the worker fills it in.
func (i *Imports) Create(ctx context.Context, userID string, moves []string) (string, error) {
	id := uuid.NewString()
	var owner any
	if userID != "" {
		owner = userID
	}
	_, err := i.pool.Exec(ctx,
		"INSERT INTO imports (id, user_id, moves) VALUES ($1, $2, $3)",
		id, owner, strings.Join(moves, " "))
	return id, err
}

// Moves reads back what was pasted, for the worker.
func (i *Imports) Moves(ctx context.Context, id string) ([]string, error) {
	var moves string
	err := i.pool.QueryRow(ctx, "SELECT moves FROM imports WHERE id = $1", id).Scan(&moves)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return strings.Fields(moves), nil
}

func (i *Imports) SaveReview(ctx context.Context, id string, r any) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = i.pool.Exec(ctx, "UPDATE imports SET review = $2 WHERE id = $1", id, raw)
	return err
}

// Review returns the finished review, or false while it is still queued. Same
// three way answer as a game review: found and done, found and waiting, or no
// such thing.
func (i *Imports) Review(ctx context.Context, id string) (json.RawMessage, bool, error) {
	var raw []byte
	err := i.pool.QueryRow(ctx, "SELECT review FROM imports WHERE id = $1", id).Scan(&raw)
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
