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

// Sessions are keyed by token, which answers "who is this?" but not "what
// else is signed in as me?". Recovery needs the second question: someone
// resetting a password they think was stolen has to be able to throw the
// thief out, and that means reaching sessions whose tokens the server never
// keeps. So each user also gets a set of their own session keys.
//
// The set is an index, not a source of truth. Members can outlive the
// sessions they name, because a session key expires on its own and Redis does
// not remove it from the set. That costs nothing: deleting a key that has
// already expired is a no-op, and the set expires on its own schedule too.
func userSessionsKey(userID string) string { return "usersessions:" + userID }

// Create returns the token to hand to the client. It is the only time the
// token exists outside the cookie.
func (s *Sessions) Create(ctx context.Context, userID string) (string, error) {
	raw := make([]byte, sessionTokenBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	key := sessionKey(token)
	// One round trip for both writes. The index is allowed to be slightly
	// wrong (see userSessionsKey) but it is not allowed to miss a live
	// session, so it is written in the same pipeline rather than after.
	pipe := s.rdb.TxPipeline()
	pipe.Set(ctx, key, userID, s.ttl)
	pipe.SAdd(ctx, userSessionsKey(userID), key)
	pipe.Expire(ctx, userSessionsKey(userID), s.ttl)
	if _, err := pipe.Exec(ctx); err != nil {
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
	key := sessionKey(token)
	// Read the owner before deleting, so the index entry goes too. If the
	// session has already expired there is nothing to tidy and nothing to do.
	userID, err := s.rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	pipe.Del(ctx, key)
	pipe.SRem(ctx, userSessionsKey(userID), key)
	_, err = pipe.Exec(ctx)
	return err
}

// DestroyAllFor signs a user out everywhere. Used by account recovery, where
// the whole point is that somebody else may be holding a live session.
func (s *Sessions) DestroyAllFor(ctx context.Context, userID string) error {
	idx := userSessionsKey(userID)
	keys, err := s.rdb.SMembers(ctx, idx).Result()
	if err != nil {
		return err
	}
	pipe := s.rdb.TxPipeline()
	if len(keys) > 0 {
		pipe.Del(ctx, keys...)
	}
	pipe.Del(ctx, idx)
	_, err = pipe.Exec(ctx)
	return err
}
