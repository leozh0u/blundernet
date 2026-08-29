package store

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Anonymous pastes accumulate forever otherwise. Anyone can review a game
// without an account, which is the point of the feature and also means nothing
// ties those rows to somebody who might come back for them. A review is read
// in the minutes after it is asked for and then almost never again, so the
// ones nobody owns are kept a week and the ones attached to an account are
// kept, because that person can still open their history.
const (
	importRetention   = 7 * 24 * time.Hour
	importReapBatch   = 2000
	importReapEvery   = time.Hour
	importReapTimeout = 30 * time.Second
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

// Reap deletes one batch of old anonymous reviews. Batched for the same reason
// the guest reaper is: a backlog must not hold a lock long enough to be felt
// on the request path.
func (i *Imports) Reap(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := i.pool.Exec(ctx, `
		DELETE FROM imports
		WHERE id IN (
		    SELECT id FROM imports
		    WHERE user_id IS NULL AND created_at < now() - $1::interval
		    LIMIT $2
		)`, olderThan, importReapBatch)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunImportReaper reaps on a ticker until the context ends, the same shape and
// for the same reasons as the guest reaper.
func RunImportReaper(ctx context.Context, imports *Imports) {
	t := time.NewTicker(importReapEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		reapCtx, cancel := context.WithTimeout(ctx, importReapTimeout)
		n, err := imports.Reap(reapCtx, importRetention)
		cancel()
		if err != nil {
			slog.Warn("reap imports", "err", err)
			continue
		}
		if n > 0 {
			slog.Info("reaped old anonymous reviews", "count", n)
		}
	}
}
