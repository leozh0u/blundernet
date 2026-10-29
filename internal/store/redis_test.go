package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/game"
)

func testRedis(t *testing.T) (*redis.Client, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb, mr
}

func TestGameUpdateAcceptsLegacyRevision(t *testing.T) {
	rdb, mr := testRedis(t)
	ctx := context.Background()
	if err := mr.Set(key("legacy"), `{"id":"legacy","moves":[],"ply":0,"player_color":"white","status":"ongoing"}`); err != nil {
		t.Fatal(err)
	}
	games := NewGames(rdb)
	g, err := games.Get(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := g.ApplyMove("white", "e2e4"); err != nil {
		t.Fatal(err)
	}
	if err := games.Update(ctx, g, 0); err != nil {
		t.Fatal(err)
	}
	if g.Revision != 1 {
		t.Fatalf("legacy revision = %d", g.Revision)
	}
}

func TestSlowSubscriberClosesOnCancellation(t *testing.T) {
	for _, kind := range []string{"game", "classroom"} {
		t.Run(kind, func(t *testing.T) {
			rdb, _ := testRedis(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var events <-chan []byte
			name := channel("slow")
			if kind == "game" {
				events = NewGames(rdb).Subscribe(ctx, "slow")
			} else {
				events = NewClassBoard(rdb).Watch(ctx, "slow")
				name = boardChannel("slow")
			}
			deadline := time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				if err := rdb.Publish(ctx, name, "update").Err(); err != nil {
					t.Fatal(err)
				}
				if len(events) == cap(events) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if len(events) != cap(events) {
				t.Fatal("subscriber never filled")
			}
			// One more update stalls a sender while its consumer is away.
			_ = rdb.Publish(ctx, name, "blocked").Err()
			time.Sleep(10 * time.Millisecond)
			cancel()
			deadline = time.Now().Add(time.Second)
			for time.Now().Before(deadline) {
				n, err := rdb.PubSubNumSub(context.Background(), name).Result()
				if err != nil {
					t.Fatal(err)
				}
				if n[name] == 0 {
					return
				}
				time.Sleep(time.Millisecond)
			}
			t.Fatal("cancelled subscription still holds its Redis connection")
		})
	}
}

func TestGameUpdateRejectsStateChangedWithoutAMove(t *testing.T) {
	for _, change := range []string{"resign", "join"} {
		t.Run(change, func(t *testing.T) {
			rdb, _ := testRedis(t)
			games := NewGames(rdb)
			ctx := context.Background()
			g := game.New("race", "white", 1, false)
			g.Friend = true
			if err := games.Create(ctx, g); err != nil {
				t.Fatal(err)
			}
			first, err := games.Get(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			stale, err := games.Get(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if change == "resign" {
				if err := first.Resign("white"); err != nil {
					t.Fatal(err)
				}
				if err := stale.ApplyMove("white", "e2e4"); err != nil {
					t.Fatal(err)
				}
			} else {
				first.OpponentID = "first-player"
				stale.OpponentID = "second-player"
			}
			if err := games.Update(ctx, first, 0); err != nil {
				t.Fatal(err)
			}
			if err := games.Update(ctx, stale, 0); !errors.Is(err, ErrConflict) {
				t.Fatalf("stale %s overwrote state: %v", change, err)
			}
			got, err := games.Get(ctx, g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != first.Status || got.OpponentID != first.OpponentID || got.Ply != first.Ply {
				t.Fatalf("saved state changed: %+v", got)
			}
		})
	}
}
