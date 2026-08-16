package store

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// Seen is the set of puzzles a user has already been shown, kept in Redis.
//
// The obvious version of "not one I have seen" is a NOT IN against the
// attempts table, which gets slower the more puzzles somebody solves. That is
// exactly backwards: the people who use the site most would get the worst
// latency. So the set lives in Redis, is read once per search, and the
// candidates the sampler returns are filtered against it in memory.
//
// Postgres still holds the durable record in puzzle_attempts. This is a cache
// of one question, and losing it costs a repeated puzzle rather than history.
type Seen struct {
	rdb *redis.Client
	ttl time.Duration
}

// Long enough that a regular user never repeats, short enough that an account
// which stopped visiting stops costing memory.
const seenTTL = 90 * 24 * time.Hour

func NewSeen(rdb *redis.Client) *Seen { return &Seen{rdb: rdb, ttl: seenTTL} }

func seenKey(userID string) string { return "puzzles:seen:" + userID }

// Load reads the whole set. One round trip and a few tens of kilobytes for a
// heavy user, against one round trip per candidate if this were a membership
// check per puzzle.
func (s *Seen) Load(ctx context.Context, userID string) (map[string]bool, error) {
	if s == nil || userID == "" {
		return nil, nil
	}
	ids, err := s.rdb.SMembers(ctx, seenKey(userID)).Result()
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// Add marks puzzles as seen and pushes the expiry out. Adding and expiring go
// in one pipeline, because two round trips for a bookkeeping write on the
// solve path is one too many.
func (s *Seen) Add(ctx context.Context, userID string, ids ...string) error {
	if s == nil || userID == "" || len(ids) == 0 {
		return nil
	}
	members := make([]any, len(ids))
	for i, id := range ids {
		members[i] = id
	}
	pipe := s.rdb.Pipeline()
	pipe.SAdd(ctx, seenKey(userID), members...)
	pipe.Expire(ctx, seenKey(userID), s.ttl)
	_, err := pipe.Exec(ctx)
	return err
}
