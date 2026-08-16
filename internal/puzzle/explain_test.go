package puzzle

import (
	"strings"
	"testing"
)

func explain(t *testing.T, rec []string) Explanation {
	t.Helper()
	p, err := Parse(rec)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := Explain(p)
	if !ok {
		t.Fatal("the solution did not replay from the position")
	}
	return e
}

func TestExplainMate(t *testing.T) {
	// A real mate in one: black's queen comes to c3 and the king has nothing.
	e := explain(t, []string{
		"YeLzl", "r5k1/pp1b2pp/1r6/q3n3/3NQ3/2PB4/P1P2PPP/1K1R3R w - - 3 17",
		"b1a1 a5c3", "1010", "75", "95", "500",
		"mate mateIn1 middlegame oneMove", "https://lichess.org/UoHhMm7z#33", "",
	})
	if e.Headline != "Checkmate in 1." {
		t.Errorf("headline = %q", e.Headline)
	}
}

func TestExplainReportsWhatTheMovedPieceAttacks(t *testing.T) {
	// White plays Nxf7, hitting the king on h8 and the queen on d8 at once.
	// The rook on f8 is not one of them, which is the point of deriving this
	// from the board rather than from the fork tag.
	e := explain(t, []string{
		"fork1", "3q1r1k/pppb1ppp/8/4N3/8/8/PPPP1PPP/R1BQ1RK1 b - - 0 1",
		"d7c6 e5f7", "1500", "75", "95", "500",
		"fork middlegame short", "", "",
	})
	if !strings.Contains(e.Headline, "forks") {
		t.Errorf("headline = %q, expected a fork", e.Headline)
	}
	if !strings.Contains(e.Headline, "king on h8") || !strings.Contains(e.Headline, "queen on d8") {
		t.Errorf("headline = %q, expected both targets named", e.Headline)
	}
	joined := strings.Join(e.Points, " ")
	if !strings.Contains(joined, "knight on f7") {
		t.Errorf("points = %v, expected the square the knight landed on", e.Points)
	}
}

func TestExplainCountsMaterial(t *testing.T) {
	// Black wins a rook: the queen takes on a1, which nothing defends.
	e := explain(t, []string{
		"win1", "6k1/5ppp/8/8/8/8/q4PPP/R5K1 w - - 0 1",
		"h2h3 a2a1", "1200", "75", "95", "500",
		"endgame hangingPiece short", "", "",
	})
	if e.Headline != "Wins a rook." {
		t.Errorf("headline = %q, want a rook", e.Headline)
	}
}

func TestExplainFallsBackToTheTheme(t *testing.T) {
	// A quiet move that neither mates nor wins material this line.
	e := explain(t, []string{
		"quiet1", "6k1/5ppp/8/8/8/8/5PPP/6K1 w - - 0 1",
		"g1f1 g8f8", "1200", "75", "95", "500",
		"endgame quietMove short", "", "",
	})
	if !strings.HasPrefix(e.Headline, "A quiet move") {
		t.Errorf("headline = %q, expected the theme sentence", e.Headline)
	}
	if len(e.Themes) != 1 || e.Themes[0] != "quietMove" {
		t.Errorf("themes = %v, want the tactic tags only", e.Themes)
	}
}

func TestExplainRefusesABrokenRow(t *testing.T) {
	p := Puzzle{
		FEN:   "6k1/5ppp/8/8/8/8/5PPP/6K1 w - - 0 1",
		Moves: []string{"g1f1", "a1a8"}, // no piece on a1
	}
	if _, ok := Explain(p); ok {
		t.Error("a move that does not play should produce no explanation at all")
	}
}
