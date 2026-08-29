package review

import (
	"context"
	"math"
	"testing"

	"github.com/leozh0u/blundernet/internal/engine"
)

// fixed answers a position with whatever the test says, keyed by how many
// positions have been asked about. A real engine would make these tests slow
// and, worse, would make a failure ambiguous: this way a wrong number is
// arithmetic or judgement, never Stockfish having an opinion.
type fixed struct {
	evals []engine.Analysis
	at    int
}

func (f *fixed) Analyse(context.Context, string) (engine.Analysis, error) {
	e := f.evals[f.at]
	f.at++
	return e, nil
}

func TestWinPercentIsSymmetric(t *testing.T) {
	even := WinPercent(engine.Analysis{CP: 0})
	if math.Abs(even-50) > 0.001 {
		t.Errorf("an equal position is %.2f%%, want 50", even)
	}
	up := WinPercent(engine.Analysis{CP: 300})
	down := WinPercent(engine.Analysis{CP: -300})
	if math.Abs((up+down)-100) > 0.001 {
		t.Errorf("+300 gives %.2f and -300 gives %.2f, which should sum to 100", up, down)
	}
	if WinPercent(engine.Analysis{Mate: 1}) != 100 || WinPercent(engine.Analysis{Mate: -1}) != 0 {
		t.Error("a forced mate should be certainty, not a point on the curve")
	}
}

// The thing this package exists to get right: the same centipawn swing means
// different things depending on where the game already stood.
func TestTheSameCentipawnSwingIsJudgedDifferently(t *testing.T) {
	// Completely winning, and gives some of it back: still winning.
	winning := WinPercent(engine.Analysis{CP: 900}) - WinPercent(engine.Analysis{CP: 600})
	// Balanced, and gives the same 300 away: the game is gone.
	level := WinPercent(engine.Analysis{CP: 100}) - WinPercent(engine.Analysis{CP: -200})

	if judge(winning, "a", "b") == Blunder {
		t.Errorf("going from +9 to +6 lost %.1f%% and was called a blunder", winning)
	}
	if judge(level, "a", "b") != Blunder {
		t.Errorf("going from +1 to -2 lost %.1f%% and was not called a blunder", level)
	}
}

func TestPlayingTheEnginesMoveIsBest(t *testing.T) {
	if j := judge(0, "e2e4", "e2e4"); j != Best {
		t.Errorf("the engine's own move was judged %q", j)
	}
	// Even a move that gives nothing away is not "best" if it was not the move.
	if j := judge(0, "d2d4", "e2e4"); j != Excellent {
		t.Errorf("a different but harmless move was judged %q, want excellent", j)
	}
}

// Perspective is the one mistake here that would be obvious in the output and
// invisible in the code, so it gets its own test: white plays a good move, and
// the engine's opinion of the position afterwards belongs to black.
func TestScoresAreReadFromTheMoversSide(t *testing.T) {
	// Position 0: white to move, level. Position 1: black to move, and black
	// is much worse, so the engine reports a big negative for black. That is
	// white having improved, not white having blundered.
	f := &fixed{evals: []engine.Analysis{
		{CP: 0, Best: "e2e4"},
		{CP: -400, Best: "e7e5"},
	}}
	got, err := Game(context.Background(), f, []string{"e2e4"})
	if err != nil {
		t.Fatal(err)
	}
	m := got.Moves[0]
	if m.WinAfter <= m.WinBefore {
		t.Fatalf("white improved but the review says %.1f%% fell to %.1f%%", m.WinBefore, m.WinAfter)
	}
	if m.Judgement != Best {
		t.Errorf("the engine's own move was judged %q", m.Judgement)
	}
}

func TestABlunderIsCaughtAndNamed(t *testing.T) {
	// White is fine, plays something the engine did not want, and hands the
	// game over.
	f := &fixed{evals: []engine.Analysis{
		{CP: 50, Best: "d2d4"},   // before white's move
		{CP: 600, Best: "e7e5"},  // after: black to move and much better
		{CP: -600, Best: "d1h5"}, // after black's reply
	}}
	got, err := Game(context.Background(), f, []string{"e2e4", "e7e5"})
	if err != nil {
		t.Fatal(err)
	}
	white := got.Moves[0]
	if white.Judgement != Blunder {
		t.Errorf("handing over the game was judged %q", white.Judgement)
	}
	if white.Better != "d2d4" {
		t.Errorf("the better move offered was %q, want d2d4", white.Better)
	}
	if white.BetterSAN != "d4" {
		t.Errorf("the better move in algebraic was %q, want d4", white.BetterSAN)
	}
	if got.White.Blunder != 1 {
		t.Errorf("white's blunder count is %d, want 1", got.White.Blunder)
	}
	if len(got.Worst) != 1 || got.Worst[0].SAN != "e4" {
		t.Errorf("worst moves are %+v, want the one blunder", got.Worst)
	}
}

// A clean game should not have its least good move called out. "Nothing went
// wrong" is a real answer.
func TestACleanGameHasNoWorstMoves(t *testing.T) {
	f := &fixed{evals: []engine.Analysis{
		{CP: 20, Best: "e2e4"},
		{CP: -20, Best: "e7e5"},
		{CP: 20, Best: "g1f3"},
	}}
	got, err := Game(context.Background(), f, []string{"e2e4", "e7e5"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Worst) != 0 {
		t.Errorf("a clean game reported %d worst moves", len(got.Worst))
	}
}

// One catastrophe must not be averaged away by a pile of quiet moves, which is
// the reason for the harmonic mean.
func TestOneBlunderDragsAccuracyDown(t *testing.T) {
	perfect := []float64{100, 100, 100, 100, 100, 100, 100, 100, 100, 100}
	withOne := append([]float64{}, perfect...)
	withOne[5] = 10

	plain := 0.0
	for _, v := range withOne {
		plain += v
	}
	plain /= float64(len(withOne))

	h := harmonic(withOne)
	if h >= plain {
		t.Errorf("harmonic mean %.1f did not punish the blunder more than a plain mean %.1f", h, plain)
	}
	if h > 70 {
		t.Errorf("a game with a 10%% move still scored %.1f", h)
	}
}

func TestAccuracyIsBoundedAndFalls(t *testing.T) {
	if a := accuracyOf(0); a < 99 {
		t.Errorf("giving nothing away scored %.1f", a)
	}
	if a := accuracyOf(-5); a > 100 {
		t.Errorf("improving your position scored %.1f, above 100", a)
	}
	if accuracyOf(30) >= accuracyOf(5) {
		t.Error("a worse move scored at least as well as a better one")
	}
	if a := accuracyOf(100); a < 0 {
		t.Errorf("losing everything scored %.1f, below zero", a)
	}
}

func TestAGameThatWillNotReplayIsRefused(t *testing.T) {
	f := &fixed{evals: []engine.Analysis{{CP: 0, Best: "e2e4"}}}
	if _, err := Game(context.Background(), f, []string{"e2e5"}); err == nil {
		t.Error("an illegal first move was accepted")
	}
}
