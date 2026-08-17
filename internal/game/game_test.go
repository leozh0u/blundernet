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
