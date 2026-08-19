package game

import (
	"errors"
	"testing"
)

func TestApplyMoveAndTurns(t *testing.T) {
	g := New("g1", "white", 4, true)
	if err := g.ApplyMove("white", "e2e4"); err != nil {
		t.Fatal(err)
	}
	if g.Turn() != "black" {
		t.Fatalf("turn = %s, want black", g.Turn())
	}
	if err := g.ApplyMove("white", "e7e5"); !errors.Is(err, ErrNotYourTurn) {
		t.Fatalf("out-of-turn move: got %v, want ErrNotYourTurn", err)
	}
	if err := g.ApplyMove("black", "e2e5"); !errors.Is(err, ErrIllegalMove) {
		t.Fatalf("illegal move: got %v, want ErrIllegalMove", err)
	}
}

func TestFoolsMateEndsGame(t *testing.T) {
	g := New("g2", "black", 4, true)
	moves := []struct{ color, uci string }{
		{"white", "f2f3"}, {"black", "e7e5"},
		{"white", "g2g4"}, {"black", "d8h4"},
	}
	for _, m := range moves {
		if err := g.ApplyMove(m.color, m.uci); err != nil {
			t.Fatalf("%s %s: %v", m.color, m.uci, err)
		}
	}
	if g.Status != StatusFinished || g.Result != "0-1" || g.Termination != "checkmate" {
		t.Fatalf("got status=%s result=%s termination=%s", g.Status, g.Result, g.Termination)
	}
	if err := g.ApplyMove("white", "e2e4"); !errors.Is(err, ErrFinished) {
		t.Fatalf("move after mate: got %v, want ErrFinished", err)
	}
}

func TestResign(t *testing.T) {
	g := New("g3", "white", 4, true)
	if err := g.Resign("white"); err != nil {
		t.Fatal(err)
	}
	if g.Result != "0-1" || g.Termination != "resignation" {
		t.Fatalf("got result=%s termination=%s", g.Result, g.Termination)
	}
}

func TestFENStartAndAfterMove(t *testing.T) {
	g := New("g4", "white", 4, true)
	want := "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"
	if g.FEN() != want {
		t.Fatalf("start FEN = %q", g.FEN())
	}
	_ = g.ApplyMove("white", "e2e4")
	if g.FEN() == want {
		t.Fatal("FEN unchanged after move")
	}
}

// A game id is not a credential. Friend game ids travel in links by design,
// and every id sits in the address bar and in any screenshot. ColorFor is the
// only thing standing between holding an id and moving or resigning in
// somebody else's game, and a rated loss moves a real rating: the bug this
// covers let an unrelated session take a player from 1000 to 686.
func TestColorForRefusesPeopleWithoutASeat(t *testing.T) {
	const owner, stranger, opponent = "user-owner", "user-stranger", "user-opponent"

	bot := New("g1", "white", 3, true)
	bot.UserID = owner

	friend := New("g2", "white", 3, false)
	friend.Friend = true
	friend.UserID = owner
	friend.OpponentID = opponent

	// A game from before identities were attached at creation. The id is the
	// only credential it has, so it keeps working rather than locking out.
	legacy := New("g3", "white", 3, true)

	cases := []struct {
		name    string
		game    *Game
		user    string
		want    string
		allowed bool
	}{
		{"bot game, owner", bot, owner, "white", true},
		{"bot game, stranger", bot, stranger, "", false},
		{"bot game, signed out", bot, "", "", false},
		{"friend game, first seat", friend, owner, "white", true},
		{"friend game, second seat", friend, opponent, "black", true},
		{"friend game, spectator", friend, stranger, "", false},
		{"ownerless game keeps working", legacy, stranger, "white", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, allowed := c.game.ColorFor(c.user)
			if allowed != c.allowed || got != c.want {
				t.Fatalf("ColorFor(%q) = (%q, %v), want (%q, %v)",
					c.user, got, allowed, c.want, c.allowed)
			}
		})
	}
}
