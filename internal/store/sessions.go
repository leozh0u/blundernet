package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

// Sessions are opaque random tokens in Redis, not JWTs.
//
// A JWT cannot be revoked. Signing out, changing a password, or losing a
// laptop all need the old credential to stop working immediately, and the
// usual fix is a server-side denylist, which is a session table with extra
// steps. Redis is already here and revocation is one DEL.
//
// Redis stores the SHA-256 of the token rather than the token, so a dump of
// the keyspace does not hand over live sessions. The token is random rather
// than derived from anything, so a fast hash is fine; there is nothing to
// brute force the way there is with a password.
type Sessions struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewSessions(rdb *redis.Client, ttl time.Duration) *Sessions {
	return &Sessions{rdb: rdb, ttl: ttl}
}

const sessionTokenBytes = 32

func sessionKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "session:" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// Create returns the token to hand to the client. It is the only time the
// token exists outside the cookie.
func (s *Sessions) Create(ctx context.Context, userID string) (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	if err := s.rdb.Set(ctx, sessionKey(token), userID, s.ttl).Err(); err != nil {
		return "", err
	}
	return token, nil
}

// Lookup resolves a token to a user id, sliding the expiry so an active
// session does not log out mid-game. Returns ErrNotFound for anything
// unknown or expired.
func (s *Sessions) Lookup(ctx context.Context, token string) (string, error) {
	key := sessionKey(token)
	userID, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	// Best effort. A failed refresh just means the session expires on its
	// original schedule.
	_ = s.rdb.Expire(ctx, key, s.ttl).Err()
	return userID, nil
}

func (s *Sessions) Destroy(ctx context.Context, token string) error {
	return s.rdb.Del(ctx, sessionKey(token)).Err()
}
