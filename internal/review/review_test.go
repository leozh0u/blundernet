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

// The two labels that are claims about the position rather than about the size
// of a mistake. Both are easy to hand out too freely, and a "brilliant" that
// fires on an ordinary recapture is worse than not having the label at all.

// Legal's Mate, where White gives up the queen and mates with three minor
// pieces. If any position in chess should be called brilliant, it is Qxf7.
func TestAQueenSacrificeThatMatesIsBrilliant(t *testing.T) {
	moves := []string{
		"e2e4", "e7e5", "g1f3", "d7d6", "f1c4", "c8g4", "b1c3", "g7g6",
		"f3e5", "g4d1", "c4f7", "e8e7", "c3d5",
	}
	// Every position is called even until the sacrifice, which is deliberate:
	// it forces the label to come from the material and the result rather than
	// from the evaluation swinging.
	evals := make([]engine.Analysis, len(moves)+1)
	for i := range evals {
		evals[i] = engine.Analysis{CP: 0, Best: bestAt(moves, i)}
	}
	// The mating move, and the position after it, which is checkmate.
	evals[len(moves)-1] = engine.Analysis{Mate: 1, Best: moves[len(moves)-1]}

	got, err := Game(context.Background(), &fixed{evals: evals}, moves)
	if err != nil {
		t.Fatal(err)
	}
	// The sacrifice is Nxe5, which offers the queen. The mate three moves
	// later is the payoff and costs nothing by itself, so the label belongs on
	// the move that gave the material away rather than the one that finished.
	var sac *Move
	for i := range got.Moves {
		if got.Moves[i].SAN == "Nxe5" {
			sac = &got.Moves[i]
		}
	}
	if sac == nil {
		t.Fatal("the game did not replay as expected, no Nxe5")
	}
	if sac.Judgement != Brilliant {
		t.Errorf("offering the queen was judged %q, want brilliant", sac.Judgement)
	}
	if got.White.Brilliant != 1 {
		t.Errorf("White has %d brilliant moves, want exactly 1", got.White.Brilliant)
	}
	// The mate itself takes no material risk, so it must not also be dressed up.
	if last := got.Moves[len(got.Moves)-1]; last.Judgement == Brilliant {
		t.Errorf("%s was called brilliant, but it sacrifices nothing", last.SAN)
	}
}

// The label has to cost something to earn. An ordinary recapture gives up
// nothing once the dust settles, so it stays Best.
func TestAnEvenTradeIsNotBrilliant(t *testing.T) {
	// 1. e4 d5 2. exd5 Qxd5: White wins a pawn and Black takes it straight
	// back. Nobody sacrificed anything.
	moves := []string{"e2e4", "d7d5", "e4d5", "d8d5"}
	evals := make([]engine.Analysis, len(moves)+1)
	for i := range evals {
		evals[i] = engine.Analysis{CP: 0, Best: bestAt(moves, i)}
	}
	got, err := Game(context.Background(), &fixed{evals: evals}, moves)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range got.Moves {
		if m.Judgement == Brilliant {
			t.Errorf("%s was called brilliant in a game with no sacrifice", m.SAN)
		}
	}
}

// Great is "the only move that held". It needs a runner up that is clearly
// worse, and it must not fire in a position that is already decided.
func TestGreatNeedsTheAlternativesToBeWorse(t *testing.T) {
	moves := []string{"e2e4", "e7e5"}

	// A close position where the second best move throws the balance away.
	only := []engine.Analysis{
		{CP: 20, Best: "e2e4", Second: &engine.Line{CP: -400, Move: "f2f3"}},
		{CP: -20, Best: "e7e5"},
		{CP: 20, Best: "g1f3"},
	}
	got, err := Game(context.Background(), &fixed{evals: only}, moves)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moves[0].Judgement != Great {
		t.Errorf("the only move that held was judged %q, want great", got.Moves[0].Judgement)
	}

	// The same shape, but there was a perfectly good alternative.
	spoilt := []engine.Analysis{
		{CP: 20, Best: "e2e4", Second: &engine.Line{CP: 15, Move: "d2d4"}},
		{CP: -20, Best: "e7e5"},
		{CP: 20, Best: "g1f3"},
	}
	got, err = Game(context.Background(), &fixed{evals: spoilt}, moves)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moves[0].Judgement != Best {
		t.Errorf("a move with a fine alternative was judged %q, want best", got.Moves[0].Judgement)
	}

	// Already completely winning, so no single move is holding anything up.
	decided := []engine.Analysis{
		{Mate: 3, Best: "e2e4", Second: &engine.Line{CP: 200, Move: "d2d4"}},
		{CP: -20, Best: "e7e5"},
		{CP: 20, Best: "g1f3"},
	}
	got, err = Game(context.Background(), &fixed{evals: decided}, moves)
	if err != nil {
		t.Fatal(err)
	}
	if got.Moves[0].Judgement == Great {
		t.Error("a move in an already won position was called great")
	}
}

// bestAt makes the engine agree with whatever was played, so a test about
// material is not accidentally a test about the move being second choice.
func bestAt(moves []string, i int) string {
	if i >= len(moves) {
		return ""
	}
	return moves[i]
}
