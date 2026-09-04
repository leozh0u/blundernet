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

func TestParseLine(t *testing.T) {
	cases := []struct {
		line string
		rank int
		cp   int
		mate int
		move string
		ok   bool
	}{
		// An engine on MultiPV 1 prints no multipv field, so the rank has to
		// default rather than be read.
		{"info depth 20 score cp -45 nodes 1000 pv e2e4", 1, -45, 0, "e2e4", true},
		{"info depth 3 score mate -2 pv e2e4", 1, 0, -2, "e2e4", true},
		{"info depth 18 multipv 2 score cp 12 nodes 900 pv d2d4 d7d5", 2, 12, 0, "d2d4", true},
		{"info depth 1 currmove e2e4 currmovenumber 1", 0, 0, 0, "", false},
		{"bestmove e2e4", 0, 0, 0, "", false},
	}
	for _, c := range cases {
		rank, l, ok := parseLine(c.line)
		if rank != c.rank || l.CP != c.cp || l.Mate != c.mate || l.Move != c.move || ok != c.ok {
			t.Errorf("parseLine(%q) = %d, %+v, %v; want %d, {CP:%d Mate:%d Move:%q}, %v",
				c.line, rank, l, ok, c.rank, c.cp, c.mate, c.move, c.ok)
		}
	}
}

// The runner up, read from a real engine rather than from a fixture.
//
// This is here because the parsing is the only part of MultiPV that can go
// wrong quietly: with the field misread, every position reports a runner up
// identical to the best move, the review stops handing out "great", and
// nothing errors.
func TestMultiPVReportsARunnerUp(t *testing.T) {
	if _, err := exec.LookPath("stockfish"); err != nil {
		t.Skip("stockfish is not installed")
	}
	s, err := NewStockfish(StockfishOptions{MoveTime: 300 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	a, err := s.Analyse(context.Background(), "rnbqkbnr/pppppppp/8/8/8/8/PPPPPPPP/RNBQKBNR w KQkq - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if a.Best == "" {
		t.Fatal("no best move from the opening position")
	}
	if a.Second == nil {
		t.Fatal("no runner up, so the multipv field is not being read")
	}
	if a.Second.Move == a.Best {
		t.Errorf("runner up %q is the same move as best", a.Second.Move)
	}

	// A mate score alongside a multipv field, since the two are parsed in the
	// same pass and a mate must not come back as a centipawn score of zero.
	m, err := s.Analyse(context.Background(), "6k1/5ppp/8/8/8/8/8/R5K1 w - - 0 1")
	if err != nil {
		t.Fatal(err)
	}
	if m.Mate == 0 {
		t.Errorf("a forced mate reported cp %d, mate %d", m.CP, m.Mate)
	}
}
