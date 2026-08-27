package store

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A classroom is a coach, a join code, and a roster whose puzzle work the
// coach can see.
//
// Every function here that reads or changes a classroom takes the id of the
// person asking, and decides for itself whether they may. That is deliberate
// and it is the lesson from the authorization bug this site already had: a
// check that lives in the handler is a check the next handler forgets. Making
// the caller a required argument means there is no way to fetch a roster
// without saying who wants it.
var (
	ErrNoSuchCode  = errors.New("no classroom with that code")
	ErrNotAMember  = errors.New("not a member of that classroom")
	ErrNotCoach    = errors.New("not a coach of that classroom")
	ErrGuestUser   = errors.New("a classroom needs an account")
	ErrAlreadyIn   = errors.New("already a member of that classroom")
	ErrLastCoach   = errors.New("a classroom keeps at least one coach")
	ErrCodeClashes = errors.New("could not allocate a join code")
	ErrBadName     = errors.New("a classroom needs a name of 1 to 60 characters")
	ErrTooManyRuns = errors.New("too many classrooms for one account")
	ErrRoomFull    = errors.New("that classroom is full")
)

const (
	RoleCoach   = "coach"
	RoleStudent = "student"
)

// joinCodeLen is six characters of the 32 character recovery alphabet, so a
// billion codes. Short enough to read off a whiteboard and type without
// complaint, which is the actual requirement: a coach says it out loud to a
// room. Guessing one is a brute force against a rate limited endpoint rather
// than a search anybody can run.
const joinCodeLen = 6

// Caps, so one account cannot fill the table and one leaked code cannot be
// used to bury a coach's roster. Both are far above any real class and exist
// to bound the damage rather than to shape the product.
const (
	maxRoomsPerCoach = 20
	maxRoomMembers   = 200
)

type Classroom struct {
	ID        string
	Name      string
	JoinCode  string
	Role      string // the asking user's role, empty if they are not in it
	Members   int
	CreatedAt time.Time
}

// Member is one person on the roster, with the numbers a coach opens the page
// for. Nothing here is private to the student beyond what a coach setting
// homework would expect to see: puzzle work, not games and not passwords.
type Member struct {
	UserID       string
	Username     string
	Role         string
	JoinedAt     time.Time
	PuzzleRating float64
	Attempts     int
	Solved       int
	LastActive   *time.Time
}

type Classrooms struct {
	pool *pgxpool.Pool
}

func NewClassrooms(pool *pgxpool.Pool) *Classrooms { return &Classrooms{pool: pool} }

// newJoinCode returns the stored form. Formatting for display belongs to
// whoever shows it: what is stored has to be what typing produces.
func newJoinCode() (string, error) {
	buf := make([]byte, joinCodeLen)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	out := make([]byte, joinCodeLen)
	for i, b := range buf {
		out[i] = recoveryAlphabet[int(b)%len(recoveryAlphabet)]
	}
	return string(out), nil
}

// NormaliseJoinCode makes a code survive being read off a screen and typed
// back: case is ignored, and anything outside the alphabet, including the
// dash a coach will copy along with it, is cosmetic.
func NormaliseJoinCode(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if strings.ContainsRune(recoveryAlphabet, r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Create opens a classroom with the caller as its first coach.
func (c *Classrooms) Create(ctx context.Context, userID, name string) (*Classroom, error) {
	// Trimmed before measuring, so a name of sixty spaces is refused here
	// rather than by the table's own CHECK, which would surface as a 500.
	name = strings.TrimSpace(name)
	if n := len([]rune(name)); n == 0 || n > 60 {
		return nil, ErrBadName
	}
	guest, err := c.isGuest(ctx, userID)
	if err != nil {
		return nil, err
	}
	if guest {
		return nil, ErrGuestUser
	}
	var mine int
	if err := c.pool.QueryRow(ctx,
		`SELECT count(*) FROM classroom_members WHERE user_id = $1 AND role = 'coach'`,
		userID).Scan(&mine); err != nil {
		return nil, err
	}
	if mine >= maxRoomsPerCoach {
		return nil, ErrTooManyRuns
	}

	// Retried rather than looped forever: a clash at six characters is a one
	// in a billion event, so three attempts failing means something else is
	// wrong and the caller should hear about it.
	for attempt := 0; attempt < 3; attempt++ {
		code, err := newJoinCode()
		if err != nil {
			return nil, err
		}
		id := uuid.NewString()
		room, err := c.insert(ctx, id, name, code, userID)
		if err == nil {
			return room, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		return nil, err
	}
	return nil, ErrCodeClashes
}

// insert writes the room and its first coach together, because a classroom
// with no coach cannot be administered by anyone and would have to be cleaned
// up by hand.
func (c *Classrooms) insert(ctx context.Context, id, name, code, userID string) (*Classroom, error) {
	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	var created time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO classrooms (id, name, join_code, created_by)
		VALUES ($1, $2, $3, $4) RETURNING created_at`,
		id, name, code, userID).Scan(&created)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO classroom_members (classroom_id, user_id, role)
		VALUES ($1, $2, $3)`, id, userID, RoleCoach); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Classroom{
		ID: id, Name: name, JoinCode: code, Role: RoleCoach,
		Members: 1, CreatedAt: created,
	}, nil
}

// Join puts the caller in the classroom with that code, as a student.
//
// Guests are refused. Everything else on this site works signed out on
// purpose, but a roster of guest accounts is a roster that empties itself when
// somebody clears their cookies, and the coach looking at it next week would
// have no way to tell that from a student who stopped turning up.
func (c *Classrooms) Join(ctx context.Context, userID, code string) (*Classroom, error) {
	code = NormaliseJoinCode(code)
	if len(code) != joinCodeLen {
		return nil, ErrNoSuchCode
	}
	guest, err := c.isGuest(ctx, userID)
	if err != nil {
		return nil, err
	}
	if guest {
		return nil, ErrGuestUser
	}

	var room Classroom
	err = c.pool.QueryRow(ctx,
		`SELECT id, name, join_code, created_at FROM classrooms WHERE join_code = $1`,
		code).Scan(&room.ID, &room.Name, &room.JoinCode, &room.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNoSuchCode
	}
	if err != nil {
		return nil, err
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO classroom_members (classroom_id, user_id, role)
		VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, room.ID, userID, RoleStudent)
	if err != nil {
		return nil, err
	}
	// Already in the room. Said plainly rather than silently succeeding,
	// because a coach who joins their own code should not be quietly demoted
	// to student, and ON CONFLICT DO NOTHING is what stops that happening.
	if tag.RowsAffected() == 0 {
		return nil, ErrAlreadyIn
	}
	// Counted after the insert and inside the transaction, so a code passed
	// around a hundred people cannot have every request pass a "there is room"
	// check at the same moment. The rollback undoes this join rather than
	// leaving the room one over.
	var size int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM classroom_members WHERE classroom_id = $1`,
		room.ID).Scan(&size); err != nil {
		return nil, err
	}
	if size > maxRoomMembers {
		return nil, ErrRoomFull
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	room.Role = RoleStudent
	room.Members = size
	return &room, nil
}

// Delete closes a classroom. Coach only, and it is the only way a room ever
// goes away: the last coach cannot walk out of a room, so without this every
// classroom ever opened would be permanent. The membership rows go with it
// through the foreign key rather than by a second statement here.
func (c *Classrooms) Delete(ctx context.Context, classroomID, userID string) error {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return err
	}
	if role != RoleCoach {
		return ErrNotCoach
	}
	_, err = c.pool.Exec(ctx, `DELETE FROM classrooms WHERE id = $1`, classroomID)
	return err
}

// ForUser lists the classrooms the caller belongs to. The join code travels
// only to coaches: a student who has the code can hand out access to a room
// that is not theirs to open.
func (c *Classrooms) ForUser(ctx context.Context, userID string) ([]Classroom, error) {
	rows, err := c.pool.Query(ctx, `
		SELECT r.id, r.name,
		       CASE WHEN m.role = 'coach' THEN r.join_code ELSE '' END,
		       m.role, r.created_at,
		       (SELECT count(*) FROM classroom_members x WHERE x.classroom_id = r.id)
		FROM classroom_members m
		JOIN classrooms r ON r.id = m.classroom_id
		WHERE m.user_id = $1
		ORDER BY r.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Classroom{}
	for rows.Next() {
		var room Classroom
		if err := rows.Scan(&room.ID, &room.Name, &room.JoinCode, &room.Role,
			&room.CreatedAt, &room.Members); err != nil {
			return nil, err
		}
		out = append(out, room)
	}
	return out, rows.Err()
}

// roleOf is the one place that answers "may this person do this". Everything
// below goes through it.
func (c *Classrooms) roleOf(ctx context.Context, classroomID, userID string) (string, error) {
	var role string
	err := c.pool.QueryRow(ctx,
		`SELECT role FROM classroom_members WHERE classroom_id = $1 AND user_id = $2`,
		classroomID, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotAMember
	}
	if err != nil {
		return "", err
	}
	return role, nil
}

// Roster returns the room and its members, as much of it as the caller may
// see. A coach gets everybody's numbers, since that is what the room is for. A
// student gets the room and their own row and nobody else's, because "who else
// is in my class and how are they doing" is not a question the site should
// answer to a classmate.
func (c *Classrooms) Roster(ctx context.Context, classroomID, userID string) (*Classroom, []Member, error) {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return nil, nil, err
	}

	var room Classroom
	room.Role = role
	err = c.pool.QueryRow(ctx, `
		SELECT id, name, CASE WHEN $2 THEN join_code ELSE '' END, created_at,
		       (SELECT count(*) FROM classroom_members x WHERE x.classroom_id = classrooms.id)
		FROM classrooms WHERE id = $1`,
		classroomID, role == RoleCoach).
		Scan(&room.ID, &room.Name, &room.JoinCode, &room.CreatedAt, &room.Members)
	if err != nil {
		return nil, nil, err
	}

	// The predicate does the narrowing rather than a filter in Go, so a
	// student cannot be handed rows the caller then forgets to drop.
	rows, err := c.pool.Query(ctx, `
		SELECT m.user_id, u.username, m.role, m.joined_at, u.puzzle_rating,
		       count(a.puzzle_id),
		       count(a.puzzle_id) FILTER (WHERE a.solved),
		       max(a.attempted_at)
		FROM classroom_members m
		JOIN users u ON u.id = m.user_id
		LEFT JOIN puzzle_attempts a ON a.user_id = m.user_id
		WHERE m.classroom_id = $1 AND ($2 OR m.user_id = $3)
		GROUP BY m.user_id, u.username, m.role, m.joined_at, u.puzzle_rating
		ORDER BY m.role, lower(u.username)`,
		classroomID, role == RoleCoach, userID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	members := []Member{}
	for rows.Next() {
		var (
			m    Member
			name *string
		)
		if err := rows.Scan(&m.UserID, &name, &m.Role, &m.JoinedAt,
			&m.PuzzleRating, &m.Attempts, &m.Solved, &m.LastActive); err != nil {
			return nil, nil, err
		}
		if name != nil {
			m.Username = *name
		}
		members = append(members, m)
	}
	return &room, members, rows.Err()
}

// Remove takes somebody off the roster. A coach may remove anybody, and anyone
// may remove themselves, which is the same operation and not worth two routes.
func (c *Classrooms) Remove(ctx context.Context, classroomID, callerID, targetID string) error {
	role, err := c.roleOf(ctx, classroomID, callerID)
	if err != nil {
		return err
	}
	if role != RoleCoach && callerID != targetID {
		return ErrNotCoach
	}

	tx, err := c.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`DELETE FROM classroom_members WHERE classroom_id = $1 AND user_id = $2`,
		classroomID, targetID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotAMember
	}

	// Checked after the delete, inside the transaction, so two coaches leaving
	// at once cannot both pass a "there are two of us" check and leave nobody.
	var coaches int
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM classroom_members WHERE classroom_id = $1 AND role = 'coach'`,
		classroomID).Scan(&coaches); err != nil {
		return err
	}
	if coaches == 0 {
		return ErrLastCoach
	}
	return tx.Commit(ctx)
}

// RotateCode issues a new join code, which is how a coach shuts out a code
// that got passed around. Coach only.
func (c *Classrooms) RotateCode(ctx context.Context, classroomID, userID string) (string, error) {
	role, err := c.roleOf(ctx, classroomID, userID)
	if err != nil {
		return "", err
	}
	if role != RoleCoach {
		return "", ErrNotCoach
	}
	for attempt := 0; attempt < 3; attempt++ {
		code, err := newJoinCode()
		if err != nil {
			return "", err
		}
		_, err = c.pool.Exec(ctx,
			`UPDATE classrooms SET join_code = $2 WHERE id = $1`, classroomID, code)
		if err == nil {
			return code, nil
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			continue
		}
		return "", err
	}
	return "", ErrCodeClashes
}

func (c *Classrooms) isGuest(ctx context.Context, userID string) (bool, error) {
	var guest bool
	err := c.pool.QueryRow(ctx, `SELECT is_guest FROM users WHERE id = $1`, userID).Scan(&guest)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotAMember
	}
	return guest, err
}
