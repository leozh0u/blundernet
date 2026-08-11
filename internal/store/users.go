package store

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

var (
	ErrUsernameTaken  = errors.New("username taken")
	ErrBadCredentials = errors.New("bad credentials")
)

type User struct {
	ID       string
	Username string
}

type Users struct {
	pool *pgxpool.Pool
}

func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

// Argon2id rather than bcrypt: it is the Password Hashing Competition winner
// and, unlike bcrypt, it is memory hard, so a GPU cracking rig gains much less
// over a CPU. These parameters cost roughly 64MB and are tuned to land near
// 100ms on the deploy target, which is the usual advice: slow enough to hurt
// an attacker, fast enough that a login does not feel broken.
const (
	argonTime    = 1
	argonMemory  = 64 * 1024 // KiB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
)

// hashPassword returns a self describing hash. The parameters travel with it
// so they can be raised later without invalidating existing passwords: an old
// hash still verifies against the parameters it was made with.
func hashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyPassword(password, encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var memory uint32
	var time uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &threads); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(password), salt, time, memory, threads, uint32(len(want)))
	// Constant time, so the comparison does not leak how much of the hash
	// matched through how long it took to fail.
	return subtle.ConstantTimeCompare(got, want) == 1
}

func (u *Users) Create(ctx context.Context, username, password string) (*User, error) {
	hash, err := hashPassword(password)
	if err != nil {
		return nil, err
	}
	id := uuid.NewString()
	_, err = u.pool.Exec(ctx,
		"INSERT INTO users (id, username, password_hash) VALUES ($1, $2, $3)",
		id, username, hash)
	if err != nil {
		// 23505 is unique_violation, which here can only be the username
		// index. Catching the constraint beats checking first: a check then
		// insert races two signups for the same name.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrUsernameTaken
		}
		return nil, err
	}
	return &User{ID: id, Username: username}, nil
}

// Authenticate returns ErrBadCredentials for both an unknown user and a wrong
// password, so the response cannot be used to enumerate which usernames exist.
func (u *Users) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var id, name, hash string
	err := u.pool.QueryRow(ctx,
		"SELECT id, username, password_hash FROM users WHERE lower(username) = lower($1)",
		username).Scan(&id, &name, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		// Hash anyway. Returning early on an unknown username would make the
		// failure measurably faster than a wrong password, which is exactly
		// the signal the shared error message is hiding.
		hashPassword(password)
		return nil, ErrBadCredentials
	}
	if err != nil {
		return nil, err
	}
	if !verifyPassword(password, hash) {
		return nil, ErrBadCredentials
	}
	return &User{ID: id, Username: name}, nil
}

func (u *Users) ByID(ctx context.Context, id string) (*User, error) {
	var user User
	err := u.pool.QueryRow(ctx,
		"SELECT id, username FROM users WHERE id = $1", id).Scan(&user.ID, &user.Username)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &user, nil
}
