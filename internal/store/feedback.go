package store

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Feedback is the "something is broken" box.
//
// Reports are written by anybody, signed in or not, because the person most
// likely to hit a bug is the one who just arrived and has no account. That
// makes it an unauthenticated write, so the limits below are the whole defence
// against somebody filling the table.
type Feedback struct {
	pool *pgxpool.Pool
}

func NewFeedback(pool *pgxpool.Pool) *Feedback { return &Feedback{pool: pool} }

const (
	// Long enough for somebody to describe a bug properly, short enough that
	// the column cannot be used as free storage. Anything past this is cut
	// rather than rejected: losing the tail of a long report is friendlier
	// than losing all of it.
	maxFeedbackLen = 2000
	maxPageLen     = 200
)

// Add stores one report. userID may be empty.
func (f *Feedback) Add(ctx context.Context, userID, message, page string) error {
	message = trimTo(message, maxFeedbackLen)
	page = trimTo(page, maxPageLen)
	_, err := f.pool.Exec(ctx,
		"INSERT INTO feedback (user_id, message, page) VALUES ($1, $2, $3)",
		nullableUUID(userID), message, page)
	return err
}

// Report is one message, with the sender's name when there was one.
type Report struct {
	ID       int64
	Username string // empty when the sender had no account
	Message  string
	Page     string
	When     string
}

// Unhandled lists reports nobody has dealt with yet, newest first.
func (f *Feedback) Unhandled(ctx context.Context, limit int) ([]Report, error) {
	rows, err := f.pool.Query(ctx, `
		SELECT f.id, coalesce(u.username, ''), f.message, f.page,
		       to_char(f.created_at, 'YYYY-MM-DD HH24:MI')
		FROM feedback f
		LEFT JOIN users u ON u.id = f.user_id
		WHERE f.handled_at IS NULL
		ORDER BY f.created_at DESC
		LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Report{}
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.ID, &r.Username, &r.Message, &r.Page, &r.When); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// trimTo cuts a string to n runes and strips surrounding whitespace. Runes
// rather than bytes so a cut never lands inside a multi-byte character and
// leaves invalid UTF-8 in the column.
func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}
