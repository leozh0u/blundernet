package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

var replacePuzzleProgress = redis.NewScript(`
local current = redis.call('GET', KEYS[1]) or ''
if current ~= ARGV[1] then return 0 end
if ARGV[2] == '' then redis.call('DEL', KEYS[1])
else redis.call('SET', KEYS[1], ARGV[2], 'EX', ARGV[3]) end
return 1
`)

// replaceProgress prevents a late request from overwriting a newer step or puzzle.
func replaceProgress(ctx context.Context, rdb *redis.Client, key string, before, after any, ttl time.Duration) error {
	encode := func(v any) ([]byte, error) {
		if v == nil {
			return []byte{}, nil
		}
		return json.Marshal(v)
	}
	old, err := encode(before)
	if err != nil {
		return err
	}
	next, err := encode(after)
	if err != nil {
		return err
	}
	ok, err := replacePuzzleProgress.Run(ctx, rdb, []string{key}, old, next, int(ttl.Seconds())).Int()
	if err != nil {
		return err
	}
	if ok == 0 {
		return ErrConflict
	}
	return nil
}

func (r *Ranked) Replace(ctx context.Context, userID string, before, after *InProgress) error {
	var old, next any
	if before != nil {
		old = *before
	}
	if after != nil {
		next = *after
	}
	return replaceProgress(ctx, r.rdb, rankedKey(userID), old, next, rankedTTL)
}

func (s *Streak) Replace(ctx context.Context, userID string, before, after *StreakState) error {
	var old, next any
	if before != nil {
		old = *before
	}
	if after != nil {
		next = *after
	}
	return replaceProgress(ctx, s.rdb, streakKey(userID), old, next, streakTTL)
}
