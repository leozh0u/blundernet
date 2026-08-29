package engine

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// These run against a real Stockfish when one is on PATH and skip otherwise,
// the same bargain the database tests make: a fake UCI engine would only prove
// the fake agrees with itself.
func testEngine(t *testing.T) *Stockfish {
	t.Helper()
	if _, err := exec.LookPath("stockfish"); err != nil {
		t.Skip("stockfish is not installed")
	}
	sf, err := NewStockfish(StockfishOptions{MoveTime: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sf.Close() })
	return sf
}

func TestFindsMateInOne(t *testing.T) {
	sf := testEngine(t)
	// Back rank mate: white plays Ra8.
	a, err := sf.Analyse(context.Background(), "6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Best != "a1a8" {
		t.Errorf("best move is %q, want a1a8", a.Best)
	}
	if a.Mate != 1 {
		t.Errorf("mate is %d, want 1", a.Mate)
	}
}

// The score is from the side to move, which is the assumption every
// calculation downstream rests on.
func TestScoreIsFromTheSideToMove(t *testing.T) {
	sf := testEngine(t)
	// White is a queen up, white to move.
	white, err := sf.Analyse(context.Background(), "6k1/5ppp/8/8/8/8/5PPP/3Q2K1 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	// The same position with black to move: the same material, other side.
	black, err := sf.Analyse(context.Background(), "6k1/5ppp/8/8/8/8/5PPP/3Q2K1 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if white.CP <= 0 && white.Mate <= 0 {
		t.Errorf("white to move a queen up scored %+v, want clearly positive", white)
	}
	if black.CP >= 0 && black.Mate >= 0 {
		t.Errorf("black to move a queen down scored %+v, want clearly negative", black)
	}
}

func TestAFinishedPositionHasNoMove(t *testing.T) {
	sf := testEngine(t)
	// Black is already mated.
	a, err := sf.Analyse(context.Background(), "6k1/5ppp/8/8/8/8/8/R5K1 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	_ = a
	// Stalemate: black to move with no legal move.
	done, err := sf.Analyse(context.Background(), "7k/5Q2/6K1/8/8/8/8/8 b - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if !done.Mated() {
		t.Errorf("a position with no legal move returned %q", done.Best)
	}
}

func TestParseScore(t *testing.T) {
	cases := []struct {
		line string
		cp   int
		mate int
		ok   bool
	}{
		{"info depth 20 score cp -45 nodes 1000 pv e2e4", -45, 0, true},
		{"info depth 3 score mate -2 pv e2e4", 0, -2, true},
		{"info depth 1 currmove e2e4 currmovenumber 1", 0, 0, false},
		{"bestmove e2e4", 0, 0, false},
	}
	for _, c := range cases {
		cp, mate, ok := parseScore(c.line)
		if cp != c.cp || mate != c.mate || ok != c.ok {
			t.Errorf("parseScore(%q) = %d, %d, %v; want %d, %d, %v",
				c.line, cp, mate, ok, c.cp, c.mate, c.ok)
		}
	}
}
