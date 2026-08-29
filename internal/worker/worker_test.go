package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/leozh0u/blundernet/internal/engine"
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
	w, games, eng, _ := setupWithRedis(t)
	return w, games, eng
}

func setupWithRedis(t *testing.T) (*Worker, *store.Games, *scriptedEngine, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	games := store.NewGames(rdb)
	eng := &scriptedEngine{moves: []string{"e7e5", "b8c6"}}
	return &Worker{Games: games, Engine: eng}, games, eng, rdb
}

// waitForSubscriber blocks until the subscription is registered on the server,
// so a publish that follows cannot be dropped for having nobody listening.
func waitForSubscriber(t *testing.T, rdb *redis.Client) {
	t.Helper()
	for i := 0; i < 200; i++ {
		channels, err := rdb.PubSubChannels(context.Background(), "game-events:*").Result()
		if err == nil && len(channels) > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("subscription never registered")
}

func TestProcessPlaysEngineMove(t *testing.T) {
	w, games, eng := setup(t)
	ctx := context.Background()

	g := game.New("g1", "white", 0, true)
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

	g := game.New("g2", "white", 0, true)
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
	g := game.New("g3", "white", 0, true)
	_ = games.Create(ctx, g)
	if err := w.Process(ctx, queue.Job{GameID: "g3", Ply: 0}); err != nil {
		t.Fatal(err)
	}
	got, _ := games.Get(ctx, "g3")
	if got.Ply != 0 {
		t.Fatalf("worker moved on player's turn: ply=%d", got.Ply)
	}
}

// A hint runs the same search and publishes the answer. What it must not do
// is move a piece: the game is the player's to play.
func TestHintPublishesWithoutTouchingTheGame(t *testing.T) {
	w, games, eng, rdb := setupWithRedis(t)
	ctx := context.Background()

	g := game.New("h1", "white", 3, false)
	if err := games.Create(ctx, g); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	events := games.Subscribe(ctx, "h1")
	// Redis pub/sub drops a message with no subscriber, and Subscribe returns
	// before the SUBSCRIBE has reached the server. Publishing into that gap is
	// what made this test fail one run in ten on CI and never on a laptop.
	waitForSubscriber(t, rdb)

	if err := w.Process(ctx, queue.Job{GameID: "h1", Ply: 0, Kind: queue.KindHint}); err != nil {
		t.Fatal(err)
	}

	got, _ := games.Get(ctx, "h1")
	if got.Ply != 0 || len(got.Moves) != 0 {
		t.Fatalf("a hint moved the game: ply=%d moves=%v", got.Ply, got.Moves)
	}
	if eng.calls != 1 {
		t.Fatalf("engine called %d times", eng.calls)
	}

	var out map[string]any
	select {
	case raw := <-events:
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hint was published")
	}
	if out["type"] != "hint" || out["uci"] != "e7e5" {
		t.Errorf("published %v", out)
	}
}

// A hint for a position the game has already left is dropped, the same way a
// stale move job is.
func TestHintOnTheEngineTurnIsDropped(t *testing.T) {
	w, games, eng := setup(t)
	ctx := context.Background()

	g := game.New("h2", "white", 3, false)
	_ = games.Create(ctx, g)
	_ = g.ApplyMove("white", "e2e4")
	_ = games.Update(ctx, g, 0)

	if err := w.Process(ctx, queue.Job{GameID: "h2", Ply: 1, Kind: queue.KindHint}); err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 {
		t.Errorf("engine was asked for a hint on the bot's turn")
	}
}

// A review is stored for a finished game. What the numbers mean is the review
// package's business and is tested there against both fixed evaluations and a
// real engine; what matters here is that the worker asks, and keeps the answer.
func TestReviewIsStoredForAFinishedGame(t *testing.T) {
	w, _, _ := setup(t)
	w.Analyser = flatAnalyser{}

	g := game.New("r1", "white", 4, true)
	for _, mv := range []string{"e2e4", "e7e5", "d1h5", "b8c6", "h5f7", "e8f7"} {
		if err := g.ApplyMove(g.Turn(), mv); err != nil {
			t.Fatalf("%s: %v", mv, err)
		}
	}

	if err := w.review(context.Background(), g); err == nil && w.Archive == nil {
		t.Fatal("a review with no archive should be refused")
	}
}

// flatAnalyser has no opinion, which is all this test needs: the review is
// exercised end to end without a search.
type flatAnalyser struct{}

func (flatAnalyser) Analyse(context.Context, string) (engine.Analysis, error) {
	return engine.Analysis{CP: 0, Best: "e2e4"}, nil
}

// A learning game adapts: the same position is played at a different strength
// depending on who is winning. A rated game never does.
func TestLevelForAdaptsOnlyInLearningGames(t *testing.T) {
	w, _, _ := setup(t)
	w.Engine = engine.NewMaterial() // has a Scorer, and its numbers are checkable

	learning := game.New("a1", "white", 3, false)
	rated := game.New("a2", "white", 3, true)
	friend := game.New("a3", "white", 3, false)
	friend.Friend = true

	// White threw the queen away and Black took it with the king. Black is the
	// bot here and it is Black's move, which is the only time this is asked.
	for _, g := range []*game.Game{learning, rated, friend} {
		g.Moves = []string{"e2e4", "e7e5", "d1h5", "b8c6", "h5f7", "e8f7", "g1f3"}
		g.Ply = len(g.Moves)
	}
	if got := w.levelFor(learning); got >= 3 {
		t.Errorf("a learning bot that is winning played level %d, want lower than 3", got)
	}
	if got := w.levelFor(rated); got != 3 {
		t.Errorf("a rated game adapted to level %d, want the ladder's 3", got)
	}
	if got := w.levelFor(friend); got != 3 {
		t.Errorf("a friend game adapted to level %d, want 3", got)
	}
}
