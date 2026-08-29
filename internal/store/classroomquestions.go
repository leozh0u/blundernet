package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Asking a class a question about a position, and collecting the answers.
//
// Same rule as the rest of the classroom: every function takes the id of
// whoever is asking and decides for itself whether they may.
var (
	ErrNoQuestion     = errors.New("no open question")
	ErrQuestionClosed = errors.New("that question is closed")
	ErrBadPrompt      = errors.New("a question needs a position and a short prompt")
)

// Question is what the class was asked.
type Question struct {
	ID        string
	FEN       string
	Prompt    string
	CreatedAt time.Time
	// Answered is how many people have answered, which is the number a coach
	// watches while waiting rather than the answers themselves.
	Answered int
}

// Grouped counts one move and says who played it. A coach wants "four people
// played Qxf7" before they want a list of names.
type Grouped struct {
	UCI   string   `json:"uci"`
	SAN   string   `json:"san"`
	Count int      `json:"count"`
	Who   []string `json:"who"`
}

const maxPromptLen = 140

// Ask opens a question, closing whatever was open before it. A class has one
// question at a time on purpose: two live questions means a student answering
// the wrong one, and a coach reading answers to a position that is no longer
// on the screen.
func (c *Classrooms) Ask(ctx context.Context, classroomID, userID, fen, prompt string) (*Question, error) {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return nil, err
	}
	if role != RoleCoach {
		return nil, ErrNotCoach
	}
	fen = strings.TrimSpace(fen)
	prompt = strings.TrimSpace(prompt)
	if fen == "" || len([]rune(prompt)) > maxPromptLen {
		return nil, ErrBadPrompt
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		UPDATE classroom_questions SET closed_at = now()
		WHERE classroom_id = $1 AND closed_at IS NULL`, classroomID); err != nil {
		return nil, err
	}

	q := Question{ID: uuid.NewString(), FEN: fen, Prompt: prompt}
	err = tx.QueryRow(ctx, `
		INSERT INTO classroom_questions (id, classroom_id, asked_by, fen, prompt)
		VALUES ($1, $2, $3, $4, $5) RETURNING created_at`,
		q.ID, classroomID, userID, fen, prompt).Scan(&q.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &q, nil
}

// OpenQuestion returns the question a class is currently on, with as much of
// the answers as the caller may see.
//
// A coach gets every answer grouped by move. A student gets their own answer
// and the number of people who have answered, and nothing else: seeing what
// the rest of the class played before you have committed turns a question into
// a vote.
func (c *Classrooms) OpenQuestion(ctx context.Context, classroomID, userID string) (*Question, []Grouped, string, error) {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return nil, nil, "", err
	}

	var q Question
	err = c.pool.QueryRow(ctx, `
		SELECT id, fen, prompt, created_at
		FROM classroom_questions
		WHERE classroom_id = $1 AND closed_at IS NULL
		ORDER BY created_at DESC LIMIT 1`, classroomID).
		Scan(&q.ID, &q.FEN, &q.Prompt, &q.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, "", ErrNoQuestion
	}
	if err != nil {
		return nil, nil, "", err
	}

	if err := c.pool.QueryRow(ctx,
		`SELECT count(*) FROM classroom_answers WHERE question_id = $1`, q.ID).
		Scan(&q.Answered); err != nil {
		return nil, nil, "", err
	}

	// The caller's own answer, so a student sees what they committed to and a
	// coach sees their own if they answered alongside the class.
	var mine string
	err = c.pool.QueryRow(ctx,
		`SELECT uci FROM classroom_answers WHERE question_id = $1 AND user_id = $2`,
		q.ID, userID).Scan(&mine)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, "", err
	}

	if role != RoleCoach {
		return &q, nil, mine, nil
	}

	rows, err := c.pool.Query(ctx, `
		SELECT a.uci, min(a.san), count(*), array_agg(u.username ORDER BY u.username)
		FROM classroom_answers a
		JOIN users u ON u.id = a.user_id
		WHERE a.question_id = $1
		GROUP BY a.uci
		ORDER BY count(*) DESC, a.uci`, q.ID)
	if err != nil {
		return nil, nil, "", err
	}
	defer rows.Close()

	groups := []Grouped{}
	for rows.Next() {
		var g Grouped
		if err := rows.Scan(&g.UCI, &g.SAN, &g.Count, &g.Who); err != nil {
			return nil, nil, "", err
		}
		groups = append(groups, g)
	}
	return &q, groups, mine, rows.Err()
}

// Answer records one move, replacing whatever the caller answered before. A
// student who spots their mistake before the coach reveals should be able to
// change their mind, because that is what thinking looks like.
func (c *Classrooms) Answer(ctx context.Context, classroomID, questionID, userID, uci, san string) error {
	if _, err := c.roleOf(ctx, classroomID, userID); err != nil {
		return err
	}
	// The question has to belong to the classroom the caller was checked
	// against, or holding a question id from one room would be a way into
	// another. Checked in the statement rather than in a separate read, so
	// there is no gap between the check and the write.
	tag, err := c.pool.Exec(ctx, `
		INSERT INTO classroom_answers (question_id, user_id, uci, san)
		SELECT $1, $2, $3, $4 FROM classroom_questions
		WHERE id = $1 AND classroom_id = $5 AND closed_at IS NULL
		ON CONFLICT (question_id, user_id)
		DO UPDATE SET uci = EXCLUDED.uci, san = EXCLUDED.san, answered_at = now()`,
		questionID, userID, uci, san, classroomID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrQuestionClosed
	}
	return nil
}

// CloseQuestion ends a question, which is how a coach stops the count moving
// while they talk about the answers.
func (c *Classrooms) CloseQuestion(ctx context.Context, classroomID, questionID, userID string) error {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return err
	}
	if role != RoleCoach {
		return ErrNotCoach
	}
	tag, err := c.pool.Exec(ctx, `
		UPDATE classroom_questions SET closed_at = now()
		WHERE id = $1 AND classroom_id = $2 AND closed_at IS NULL`,
		questionID, classroomID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNoQuestion
	}
	return nil
}
