package worker

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/queue"
	"github.com/leozh0u/blundernet/internal/store"
)

// scriptedEngine returns canned moves; fails the test if asked twice for
// the same position when it should not be.
type scriptedEngine struct {
	calls int
	moves []string
}

func (e *scriptedEngine) Name() string { return "scripted" }
func (e *scriptedEngine) BestMove(fen string) (string, error) {
	m := e.moves[e.calls%len(e.moves)]
	e.calls++
	return m, nil
}

func setup(t *testing.T) (*Worker, *store.Games, *scriptedEngine) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	games := store.NewGames(rdb)
	eng := &scriptedEngine{moves: []string{"e7e5", "b8c6"}}
	return &Worker{Games: games, Engine: eng}, games, eng
}

func TestProcessPlaysEngineMove(t *testing.T) {
	w, games, eng := setup(t)
	ctx := context.Background()

	g := game.New("g1", "white")
	if err := games.Create(ctx, g); err != nil {
		t.Fatal(err)
	}
	if err := g.ApplyMove("white", "e2e4"); err != nil {
		t.Fatal(err)
	}
	if err := games.Update(ctx, g, 0); err != nil {
		t.Fatal(err)
	}

	if err := w.Process(ctx, queue.Job{GameID: "g1", Ply: 1}); err != nil {
		t.Fatal(err)
	}
	got, _ := games.Get(ctx, "g1")
	if got.Ply != 2 || got.Moves[1] != "e7e5" {
		t.Fatalf("after process: ply=%d moves=%v", got.Ply, got.Moves)
	}
	if eng.calls != 1 {
		t.Fatalf("engine called %d times", eng.calls)
	}
}

func TestDuplicateDeliveryIsNoOp(t *testing.T) {
	w, games, eng := setup(t)
	ctx := context.Background()

	g := game.New("g2", "white")
	_ = games.Create(ctx, g)
	_ = g.ApplyMove("white", "e2e4")
	_ = games.Update(ctx, g, 0)

	job := queue.Job{GameID: "g2", Ply: 1}
	if err := w.Process(ctx, job); err != nil {
		t.Fatal(err)
	}
	// Same job delivered again: must not move twice.
	if err := w.Process(ctx, job); err != nil {
		t.Fatal(err)
	}
	got, _ := games.Get(ctx, "g2")
	if got.Ply != 2 {
		t.Fatalf("duplicate delivery advanced the game: ply=%d", got.Ply)
	}
	if eng.calls != 1 {
		t.Fatalf("engine called %d times, want 1", eng.calls)
	}
}

func TestStaleJobAndMissingGameAreNoOps(t *testing.T) {
	w, games, _ := setup(t)
	ctx := context.Background()

	// Missing game: ack silently.
	if err := w.Process(ctx, queue.Job{GameID: "nope", Ply: 0}); err != nil {
		t.Fatal(err)
	}

	// Player's turn (ply mismatch): ack silently.
	g := game.New("g3", "white")
	_ = games.Create(ctx, g)
	if err := w.Process(ctx, queue.Job{GameID: "g3", Ply: 0}); err != nil {
		t.Fatal(err)
	}
	got, _ := games.Get(ctx, "g3")
	if got.Ply != 0 {
		t.Fatalf("worker moved on player's turn: ply=%d", got.Ply)
	}
}
