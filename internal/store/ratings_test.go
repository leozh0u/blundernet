package store

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/leozh0u/blundernet/internal/game"
	"github.com/leozh0u/blundernet/internal/testdb"
)

func testArchive(t *testing.T) (*Archive, *Users, context.Context) {
	t.Helper()
	ctx := context.Background()
	archive, err := NewArchive(ctx, testdb.URL(t, "store_test"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(archive.Close)
	if _, err := archive.Pool().Exec(ctx, "TRUNCATE users CASCADE"); err != nil {
		t.Fatal(err)
	}
	return archive, NewUsers(archive.Pool()), ctx
}

// finished builds a game that ended the way the caller asked for.
func finished(userID, result string, level int, rated bool) *game.Game {
	g := game.New(uuid.NewString(), "white", level, rated)
	g.UserID = userID
	g.Status = game.StatusFinished
	g.Result = result
	g.Termination = "resignation"
	return g
}

func TestRatedGameMovesTheRatingAndTheLadder(t *testing.T) {
	archive, users, ctx := testArchive(t)

	player, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, err := archive.Profile(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.BotLevel != 3 {
		t.Fatalf("a new player starts at level %d, want 3", before.BotLevel)
	}

	// Beat the bot: the rating rises and the next opponent is one rung up.
	if err := archive.SaveFinished(ctx, finished(player.ID, "1-0", 3, true)); err != nil {
		t.Fatal(err)
	}
	after, err := archive.Profile(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rating <= before.Rating {
		t.Errorf("rating went %v to %v after a win", before.Rating, after.Rating)
	}
	if after.BotLevel != 4 {
		t.Errorf("bot level = %d after a win, want 4", after.BotLevel)
	}

	// Lose to it: back down again.
	if err := archive.SaveFinished(ctx, finished(player.ID, "0-1", 4, true)); err != nil {
		t.Fatal(err)
	}
	back, err := archive.Profile(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.BotLevel != 3 {
		t.Errorf("bot level = %d after a loss, want 3", back.BotLevel)
	}
}

func TestLearningGameMovesNothing(t *testing.T) {
	archive, users, ctx := testArchive(t)

	player, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := archive.Profile(ctx, player.ID)

	if err := archive.SaveFinished(ctx, finished(player.ID, "0-1", 6, false)); err != nil {
		t.Fatal(err)
	}
	after, err := archive.Profile(ctx, player.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rating != before.Rating || after.BotLevel != before.BotLevel ||
		after.RatedGames != before.RatedGames {
		t.Errorf("a learning game changed something: %+v then %+v", before, after)
	}
	// It is still archived, because history is history whether or not it counted.
	games, err := archive.History(ctx, player.ID, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 {
		t.Errorf("history has %d games, want 1", len(games))
	}
}

// A stronger bot is worth more, so the same win against level 6 has to move
// the rating further than one against level 1.
func TestBeatingAHigherLevelIsWorthMore(t *testing.T) {
	archive, users, ctx := testArchive(t)

	weak, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	strong, err := users.CreateGuest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.SaveFinished(ctx, finished(weak.ID, "1-0", 1, true)); err != nil {
		t.Fatal(err)
	}
	if err := archive.SaveFinished(ctx, finished(strong.ID, "1-0", 6, true)); err != nil {
		t.Fatal(err)
	}
	a, _ := archive.Profile(ctx, weak.ID)
	b, _ := archive.Profile(ctx, strong.ID)
	if b.Rating <= a.Rating {
		t.Errorf("level 1 win gave %v, level 6 win gave %v", a.Rating, b.Rating)
	}
}
