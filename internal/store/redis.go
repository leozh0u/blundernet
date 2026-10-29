// Package store persists game state: Redis for live games (fast, TTL'd,
// shared by every api/worker instance) and Postgres for finished games.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/game"
)

var (
	ErrNotFound = errors.New("game not found")
	// ErrConflict means another writer updated the game between our read
	// and write; the caller re-reads and decides whether to retry.
	ErrConflict = errors.New("concurrent update conflict")
)

const gameTTL = 24 * time.Hour

// casScript writes only if the ply and revision still match what the caller
// read (-1 = key must not exist yet). Joins and resignations do not change ply.
// This is the same
// check-and-set-in-one-atomic-step idea as a conditional UPDATE in SQL,
// done in Lua because Redis has no WHERE clause.
var casScript = redis.NewScript(`
local cur = redis.call('GET', KEYS[1])
local expected = tonumber(ARGV[1])
if expected == -1 then
  if cur then return 0 end
elseif not cur then
  return 0
else
  local state = cjson.decode(cur)
  if state['ply'] ~= expected or (state['revision'] or 0) ~= tonumber(ARGV[4]) then return 0 end
end
redis.call('SET', KEYS[1], ARGV[2], 'EX', ARGV[3])
return 1
`)

type Games struct {
	rdb *redis.Client
}

func NewGames(rdb *redis.Client) *Games { return &Games{rdb: rdb} }

func key(id string) string { return "game:" + id }

func channel(id string) string { return "game-events:" + id }

func (s *Games) Create(ctx context.Context, g *game.Game) error {
	return s.write(ctx, g, -1)
}

// Update saves g if neither its ply nor its revision changed since the read.
func (s *Games) Update(ctx context.Context, g *game.Game, expectedPly int) error {
	return s.write(ctx, g, expectedPly)
}

func (s *Games) write(ctx context.Context, g *game.Game, expectedPly int) error {
	next := *g
	next.Revision++
	raw, err := json.Marshal(&next)
	if err != nil {
		return err
	}
	ok, err := casScript.Run(ctx, s.rdb, []string{key(g.ID)},
		expectedPly, raw, int(gameTTL.Seconds()), g.Revision).Int()
	if err != nil {
		return fmt.Errorf("redis cas: %w", err)
	}
	if ok != 1 {
		return ErrConflict
	}
	g.Revision = next.Revision
	return nil
}

func (s *Games) Get(ctx context.Context, id string) (*game.Game, error) {
	raw, err := s.rdb.Get(ctx, key(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var g game.Game
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, err
	}
	return &g, nil
}

// Publish fans a state event out to every subscriber of this game, across
// all api instances.
func (s *Games) Publish(ctx context.Context, gameID string, event []byte) error {
	return s.rdb.Publish(ctx, channel(gameID), event).Err()
}

// Subscribe returns a channel of raw events for one game. Cancel the
// context to unsubscribe.
func (s *Games) Subscribe(ctx context.Context, gameID string) <-chan []byte {
	sub := s.rdb.Subscribe(ctx, channel(gameID))
	out := make(chan []byte, 16)
	go func() {
		defer close(out)
		defer sub.Close()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-sub.Channel():
				if !ok {
					return
				}
				select {
				case out <- []byte(msg.Payload):
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
