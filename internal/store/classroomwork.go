package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Homework: what a coach set, and how far each student has got.
//
// Progress is counted from the attempts table rather than kept as a counter.
// A counter would have to be advanced by every attempt that might belong to an
// assignment, which means every attempt knowing about every assignment, and it
// would go wrong the first time that was missed. Counting reads rows the site
// already writes, so it cannot drift.

var ErrBadAssignment = errors.New("an assignment needs a target between 1 and 100")

const maxAssignments = 20

type Assignment struct {
	ID        string
	Theme     string
	MinRating int
	MaxRating int
	Target    int
	CreatedAt time.Time
	// Done is the caller's own progress, and Class is how many in the room
	// have finished it. A student sees their own number; a coach reads the
	// room's.
	Done  int
	Class int
}

// SetAssignment adds one. Coach only.
func (c *Classrooms) SetAssignment(ctx context.Context, classroomID, userID, theme string, minRating, maxRating, target int) (*Assignment, error) {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return nil, err
	}
	if role != RoleCoach {
		return nil, ErrNotCoach
	}
	if target < 1 || target > 100 {
		return nil, ErrBadAssignment
	}
	// A rating window the wrong way round matches nothing, and a coach who
	// typed it that way meant the other order.
	if minRating > 0 && maxRating > 0 && minRating > maxRating {
		minRating, maxRating = maxRating, minRating
	}

	var open int
	if err := c.pool.QueryRow(ctx,
		`SELECT count(*) FROM classroom_assignments WHERE classroom_id = $1`,
		classroomID).Scan(&open); err != nil {
		return nil, err
	}
	if open >= maxAssignments {
		return nil, ErrTooManyRuns
	}

	a := Assignment{
		ID: uuid.NewString(), Theme: strings.TrimSpace(theme),
		MinRating: minRating, MaxRating: maxRating, Target: target,
	}
	err = c.pool.QueryRow(ctx, `
		INSERT INTO classroom_assignments
			(id, classroom_id, created_by, theme, min_rating, max_rating, target)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING created_at`,
		a.ID, classroomID, userID, a.Theme, a.MinRating, a.MaxRating, a.Target).
		Scan(&a.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &a, nil
}

// Assignments lists the homework for a room with the caller's progress on
// each, and how many people have finished it.
//
// Only puzzles solved after the assignment was set count. Homework is
// something you are asked to go and do, so work from last week is not credit
// against it, and a coach setting the same theme twice gets two honest counts
// rather than the second one starting full.
func (c *Classrooms) Assignments(ctx context.Context, classroomID, userID string) ([]Assignment, error) {
	if _, err := c.roleOf(ctx, classroomID, userID); err != nil {
		return nil, err
	}
	rows, err := c.pool.Query(ctx, `
		WITH work AS (
		    SELECT a.id, a.theme, a.min_rating, a.max_rating, a.target, a.created_at
		    FROM classroom_assignments a
		    WHERE a.classroom_id = $1
		),
		-- One row per member per assignment: how many distinct puzzles that
		-- member has solved since it was set that match what it asks for.
		progress AS (
		    SELECT w.id, m.user_id, count(DISTINCT t.puzzle_id) AS solved
		    FROM work w
		    JOIN classroom_members m ON m.classroom_id = $1
		    LEFT JOIN puzzle_attempts t
		           ON t.user_id = m.user_id
		          AND t.solved
		          AND t.attempted_at >= w.created_at
		    LEFT JOIN puzzles p
		           ON p.id = t.puzzle_id
		          AND (w.theme = '' OR p.themes @> ARRAY[w.theme])
		          AND (w.min_rating = 0 OR p.rating >= w.min_rating)
		          AND (w.max_rating = 0 OR p.rating <= w.max_rating)
		    WHERE p.id IS NOT NULL OR t.puzzle_id IS NULL
		    GROUP BY w.id, m.user_id
		)
		SELECT w.id, w.theme, w.min_rating, w.max_rating, w.target, w.created_at,
		       coalesce(max(CASE WHEN pr.user_id = $2 THEN pr.solved END), 0),
		       count(*) FILTER (WHERE pr.solved >= w.target)
		FROM work w
		LEFT JOIN progress pr ON pr.id = w.id
		GROUP BY w.id, w.theme, w.min_rating, w.max_rating, w.target, w.created_at
		ORDER BY w.created_at DESC`, classroomID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Assignment{}
	for rows.Next() {
		var a Assignment
		if err := rows.Scan(&a.ID, &a.Theme, &a.MinRating, &a.MaxRating, &a.Target,
			&a.CreatedAt, &a.Done, &a.Class); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DropAssignment removes one. Coach only, and scoped to the classroom so an
// id from another room is not a way in.
func (c *Classrooms) DropAssignment(ctx context.Context, classroomID, assignmentID, userID string) error {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return err
	}
	if role != RoleCoach {
		return ErrNotCoach
	}
	tag, err := c.pool.Exec(ctx,
		`DELETE FROM classroom_assignments WHERE id = $1 AND classroom_id = $2`,
		assignmentID, classroomID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotAMember
	}
	return nil
}
