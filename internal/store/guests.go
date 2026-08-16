package store

import (
	"context"
	"log/slog"
	"time"
)

// Guests that never finished a game are disposable: there is no rating and no
// history behind them, only a row and a session that has almost certainly
// expired. Ones that did play are kept, because somebody may still come back
// to a bookmarked profile.
const (
	guestRetention = 30 * 24 * time.Hour
	reapInterval   = time.Hour
	reapBatchSize  = 5000
)

// ReapGuests deletes one batch of stale, empty guest accounts and reports how
// many went. Batched so a large backlog cannot hold a lock long enough to
// stall the request path.
func (u *Users) ReapGuests(ctx context.Context, olderThan time.Duration) (int64, error) {
	tag, err := u.pool.Exec(ctx, `
		DELETE FROM users
		WHERE id IN (
		    SELECT u.id FROM users u
		    WHERE u.is_guest
		      AND u.created_at < now() - $1::interval
		      AND NOT EXISTS (SELECT 1 FROM games g WHERE g.user_id = u.id)
		    LIMIT $2
		)`, olderThan, reapBatchSize)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// RunGuestReaper reaps on a ticker until the context ends. Every instance runs
// this; the DELETE is idempotent and rows are claimed by whichever gets there
// first, so overlapping runs cost a little duplicated work and nothing else.
func RunGuestReaper(ctx context.Context, users *Users) {
	t := time.NewTicker(reapInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		reapCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := users.ReapGuests(reapCtx, guestRetention)
		cancel()
		if err != nil {
			slog.Warn("reap guests", "err", err)
			continue
		}
		if n > 0 {
			slog.Info("reaped stale guest accounts", "count", n)
		}
	}
}
