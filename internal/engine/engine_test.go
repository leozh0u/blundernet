package engine

import (
	"os"
	"testing"

	"github.com/notnil/chess"
)

const startFEN = "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1"

func TestEncodeStartPosition(t *testing.T) {
	pos, err := ParseFEN(startFEN)
	if err != nil {
		t.Fatal(err)
	}
	x := Encode(pos)
	if len(x) != Planes*64 {
		t.Fatalf("len = %d", len(x))
	}
	at := func(plane, sq int) float32 { return x[plane*64+sq] }

	// a2 (sq 8) holds a white pawn -> plane 0; a7 (sq 48) black pawn -> plane 6.
	if at(0, 8) != 1 || at(6, 48) != 1 {
		t.Fatal("pawn planes wrong")
	}
	// e1 (sq 4) white king -> plane 5; e8 (sq 60) black king -> plane 11.
	if at(5, 4) != 1 || at(11, 60) != 1 {
		t.Fatal("king planes wrong")
	}
	// White to move: plane 12 all ones. All castling rights: planes 13-16.
	for p := 12; p <= 16; p++ {
		if at(p, 0) != 1 || at(p, 63) != 1 {
			t.Fatalf("plane %d not filled", p)
		}
	}
	// No en passant: plane 17 empty.
	for sq := 0; sq < 64; sq++ {
		if at(17, sq) != 0 {
			t.Fatal("ep plane should be empty")
		}
	}
}

func TestEncodeEnPassantFile(t *testing.T) {
	// After 1. e4, black to move, ep square e3 (file e = 4).
	pos, err := ParseFEN("rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1")
	if err != nil {
		t.Fatal(err)
	}
	x := Encode(pos)
	if x[17*64+4] != 1 || x[17*64+7*8+4] != 1 {
		t.Fatal("ep file column not set")
	}
	if x[12*64] != 0 {
		t.Fatal("side-to-move plane should be empty for black")
	}
}

func TestPromotionFolding(t *testing.T) {
	// White pawn on a7 promotes: index must round-trip to a7a8q.
	pos, err := ParseFEN("8/P6k/8/8/8/8/8/K7 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	var promo *chess.Move
	for _, m := range pos.ValidMoves() {
		if m.Promo() == chess.Queen {
			promo = m
			break
		}
	}
	if promo == nil {
		t.Fatal("no queen promotion found")
	}
	if got := IndexUCI(MoveIndex(promo), pos); got != "a7a8q" {
		t.Fatalf("got %q, want a7a8q", got)
	}
}

func TestMaterialPlaysLegalMoves(t *testing.T) {
	m := NewMaterial()
	uci, err := m.BestMove(startFEN)
	if err != nil {
		t.Fatal(err)
	}
	pos, _ := ParseFEN(startFEN)
	for _, lm := range pos.ValidMoves() {
		if IndexUCI(MoveIndex(lm), pos) == uci {
			return
		}
	}
	t.Fatalf("%q is not legal from the start position", uci)
}

func TestMaterialTakesHangingQueen(t *testing.T) {
	// Black queen hangs on d5 with white knight on c3; Nxd5 wins material.
	m := NewMaterial()
	uci, err := m.BestMove("k7/8/8/3q4/8/2N5/8/K7 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if uci != "c3d5" {
		t.Fatalf("got %q, want c3d5", uci)
	}
}

func TestONNXPlaysLegalMoves(t *testing.T) {
	if _, err := os.Stat("../../models/blundernet.onnx"); err != nil {
		t.Skip("model artifact not present")
	}
	eng, err := NewONNX("../../models/blundernet.onnx")
	if err != nil {
		t.Skipf("onnxruntime unavailable: %v", err)
	}
	for _, fen := range []string{
		startFEN,
		"rnbqkbnr/pppppppp/8/8/4P3/8/PPPP1PPP/RNBQKBNR b KQkq e3 0 1",
		"8/P6k/8/8/8/8/8/K7 w - - 0 1", // promotion position
	} {
		uci, err := eng.BestMove(fen)
		if err != nil {
			t.Fatalf("%s: %v", fen, err)
		}
		pos, _ := ParseFEN(fen)
		legal := false
		for _, lm := range pos.ValidMoves() {
			if IndexUCI(MoveIndex(lm), pos) == uci {
				legal = true
				break
			}
		}
		if !legal {
			t.Fatalf("engine played illegal %q in %q", uci, fen)
		}
	}
}

// The bot eases off when it is winning and bears down when it is losing, and
// never by more than two rungs: the player picked a level and should still be
// getting roughly that game.
func TestAdaptMovesTowardsACloseGame(t *testing.T) {
	cases := []struct {
		name  string
		base  int
		value float64
		want  int
	}{
		{"level game", 3, 0.05, 3},
		{"slightly ahead", 3, 0.4, 2},
		{"well ahead", 3, 0.9, 1},
		{"slightly behind", 3, -0.4, 4},
		{"well behind", 3, -0.9, 5},
		{"cannot go below the floor", 1, 0.9, 1},
		{"cannot go above the ceiling", 6, -0.9, 6},
	}
	for _, c := range cases {
		if got := Adapt(c.base, c.value); got != c.want {
			t.Errorf("%s: Adapt(%d, %v) = %d, want %d", c.name, c.base, c.value, got, c.want)
		}
	}
}
